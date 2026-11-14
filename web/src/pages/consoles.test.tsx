// @vitest-environment jsdom
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { afterEach, describe, expect, it } from "vitest";
import { App } from "../App";
import { getState } from "../api/store";
import type { Role } from "../api/types";
import { canOpen, NAVIGATION } from "../lib/roles";
import { reset, signedIn, website, type PanelApi } from "../test/panelApi";

afterEach(() => {
  cleanup();
  reset();
});

/** Signs in as the sample user of the role, and opens the panel at the path. */
async function open(role: Role, path: string | (() => string)): Promise<PanelApi> {
  const api = await signedIn(role);
  // A path with the ID of a website is known only after the sign-in.
  render(<MemoryRouter initialEntries={[typeof path === "string" ? path : path()]}><App /></MemoryRouter>);
  return api;
}
const heading = () => screen.getByRole("heading", { level: 1 }).textContent;
const menu = () => within(screen.getByRole("navigation", { name: "Main" })).getAllByRole("link").map((a) => a.textContent?.replace(/\d+ open$/, "").trim()).slice(1);
const ADMIN_PAGES = ["/overview", "/setup", "/servers", "/updates"];

describe("the console of each role", () => {
  it("gives each role its own first page and its own menu", async () => {
    await open("administrator", "/");
    expect(heading()).toBe("Overview");
    expect(menu()).toEqual(["Overview", "Finish setup", "Servers", "Images and updates", "Customers", "Plans", "Websites", "Databases", "Backups", "Apps", "Jobs and activity", "Settings"]);
    cleanup();
    reset();

    await open("reseller", "/");
    expect(heading()).toBe("Customers");
    expect(menu()).toEqual(["Customers", "Plans", "Websites", "Databases", "Backups", "Apps", "Jobs and activity", "My account", "Settings"]);
    cleanup();
    reset();

    await open("owner", "/");
    expect(heading()).toBe("My websites");
    expect(menu()).toEqual(["My websites", "Databases", "Backups", "Apps", "Activity", "My account", "Settings"]);
  });

  it("has no link from the menu of an owner or a reseller to a page that the role cannot open", () => {
    for (const role of ["administrator", "reseller", "owner"] as const) {
      for (const item of NAVIGATION[role].flatMap((s) => s.items)) expect(canOpen(role, item.to), `${role} ${item.to}`).toBe(true);
    }
    for (const path of [...ADMIN_PAGES, "/servers/abc"]) {
      expect(canOpen("owner", path)).toBe(false);
      expect(canOpen("reseller", path)).toBe(false);
    }
    expect(canOpen("owner", "/customers")).toBe(false);
    expect(canOpen("owner", "/plans")).toBe(false);
    expect(canOpen("administrator", "/account")).toBe(false);
  });

  it("sends an owner who opens an administrator page to the websites (audit item U1)", async () => {
    for (const path of [...ADMIN_PAGES, "/customers", "/plans", "/customers/users"]) {
      await open("owner", path);
      expect(heading(), path).toBe("My websites");
      cleanup();
      reset();
    }
    await open("reseller", "/servers");
    expect(heading()).toBe("Customers");
  });

  it("shows no administrator word on the pages of an owner", async () => {
    for (const path of ["/websites", "/databases", "/backups", "/apps", "/jobs", "/account", "/settings", `/websites/SITE`, `/websites/SITE/settings`, `/websites/SITE/backups`]) {
      await open("owner", "/");
      cleanup();
      render(<MemoryRouter initialEntries={[path.replace("SITE", website("shop.acme.example.com").id)]}><App /></MemoryRouter>);
      const text = document.body.textContent ?? "";
      expect(text, path).not.toMatch(/administrator|undefined|cgroup|Servers|Finish setup/i);
      cleanup();
      reset();
    }
  });
});

describe("the account page (audit item U1)", () => {
  it("shows an owner the plan, the use of the plan, the provider and the users", async () => {
    await open("owner", "/account");
    expect(heading()).toBe("My account");
    const main = screen.getByRole("main");
    expect(main.textContent).toContain("Plan: Business");
    expect(main.textContent).toContain("5 of 10");
    expect(main.textContent).toContain("The operator of this panel");
    expect(within(main).getByText("owner@example.com")).toBeTruthy();
    expect(main.textContent).not.toContain("undefined");
  });

  it("shows a reseller its own account, with the number of its customers", async () => {
    await open("reseller", "/account");
    expect(screen.getByRole("main").textContent).toContain("You have 2 customers.");
  });
});

describe("the setup checklist", () => {
  it("shows each open item with its effect and a link to the page that completes it", async () => {
    await open("administrator", "/setup");
    const main = screen.getByRole("main");
    expect(main.textContent).toContain("1 of 4 items are done.");
    const items = within(main).getAllByRole("listitem");
    expect(items.map((i) => within(i).getByRole("heading").textContent)).toEqual([
      "Turn on two-step sign-in for each administrator Open",
      "Turn on disk limits on each server Open",
      "Use a certificate that browsers trust for the panel Open",
      "Set the object storage for backups Done",
    ]);
    expect(items[1].textContent).toContain("While this is open: Without project quotas on the file system, a server does not apply the disk limit of a plan.");
    expect(within(items[1]).getByRole("link").getAttribute("href")).toBe(`/servers/${getState().servers.find((s) => s.name === "web-1")!.id}`);
    // A done item has no effect text and no action.
    expect(items[3].textContent).not.toContain("While this is open");
    expect(within(items[3]).queryByRole("link")).toBeNull();
  });

  it("opens the section of the settings that an item names", async () => {
    await open("administrator", "/settings/panel");
    expect(screen.getByRole("link", { current: "page", name: "Settings" })).toBeTruthy();
    expect(screen.getByRole("button", { current: "page" }).textContent).toBe("Panel certificate");
  });

  it("has no link in the menu when the setup is complete", async () => {
    const api = await signedIn("administrator");
    api.change<{ complete: boolean; open: number }>("/setup", (body) => { body.complete = true; body.open = 0; });
    const { refresh } = await import("../api/store");
    await refresh();
    render(<MemoryRouter initialEntries={["/overview"]}><App /></MemoryRouter>);
    expect(menu()).not.toContain("Finish setup");
    expect(screen.getByRole("main").textContent).not.toContain("setup items are open");
  });
});

describe("the overview of the administrator", () => {
  it("shows the servers, what needs attention and the failed jobs that are not resolved", async () => {
    await open("administrator", "/overview");
    const main = screen.getByRole("main");
    expect(main.textContent).toContain("3 setup items are open");
    expect(within(main).getByRole("link", { name: "Servers: 3. 1 needs attention" })).toBeTruthy();
    expect(within(main).getByRole("link", { name: "Failed jobs: 1. Not resolved" })).toBeTruthy();
    expect(main.textContent).toContain("web-3 is degraded: cf-sftp failed.");
    expect(main.textContent).toContain("Node.js app could not be checked for updates.");
    // The failure of the database job has a later successful job, so it is not in the list.
    const jobs = within(main).getByRole("heading", { name: "Jobs" }).closest("section")!;
    expect(within(jobs).getAllByRole("listitem")).toHaveLength(1);
    expect(jobs.textContent).toContain("Create website events.harbourbooks.example.net");
    expect(jobs.textContent).toContain("Details for administrators");
  });
});

describe("the UI items of the audit", () => {
  it("shows an owner no command of the server for a failed job (U2)", async () => {
    await open("owner", () => `/websites/${website("shop.acme.example.com").id}`);
    const jobs = screen.getByRole("heading", { name: "Jobs" }).closest("section")!;
    expect(jobs.textContent).toContain("A later try was successful. No action is necessary.");
    expect(jobs.textContent).toContain("Failed, then done");
    expect(jobs.textContent).toMatch(/Reference J-[0-9A-Z]{8}/);
    expect(document.body.textContent).not.toMatch(/podman|runuser|\.sock/);
  });

  it("changes the plan of a customer only after a dialog with the effect (U11)", async () => {
    const api = await open("administrator", "/customers");
    const customer = getState().customers.find((c) => c.name === "Corner Bakery")!;
    const business = getState().plans.find((p) => p.name === "Business")!;
    api.on(`PATCH /customers/${customer.id}`, () => ({}));
    const row = screen.getByText("Corner Bakery").closest("tr")!;
    expect(within(row).queryByRole("combobox")).toBeNull();
    fireEvent.click(within(row).getByRole("button", { name: "Actions for Corner Bakery" }));
    fireEvent.click(screen.getByRole("menuitem", { name: "Change plan…" }));
    const dialog = screen.getByRole("dialog");
    fireEvent.change(within(dialog).getByRole("combobox"), { target: { value: business.id } });
    expect(dialog.textContent).toContain("New websites of Corner Bakery get the limits of the Business plan.");
    expect(dialog.textContent).toContain("The website that Corner Bakery has now keeps its plan and limits.");
    // Nothing is sent before the administrator confirms.
    expect(api.changes.filter((c) => c.method === "PATCH")).toEqual([]);
    fireEvent.click(within(dialog).getByRole("button", { name: "Change to Business" }));
    await waitFor(() => expect(api.changes.filter((c) => c.method === "PATCH")).toEqual([{ method: "PATCH", path: `/customers/${customer.id}`, body: { planId: business.id } }]));
  });

  it("refuses a plan that is too small for the websites of the customer (U11)", async () => {
    await open("administrator", "/customers");
    const starter = getState().plans.find((p) => p.name === "Starter")!;
    fireEvent.click(screen.getByRole("button", { name: "Actions for Acme Ltd" }));
    fireEvent.click(screen.getByRole("menuitem", { name: "Change plan…" }));
    const dialog = screen.getByRole("dialog");
    fireEvent.change(within(dialog).getByRole("combobox"), { target: { value: starter.id } });
    expect(dialog.textContent).toContain("This plan is too small.");
    expect((within(dialog).getByRole("button", { name: "Change to Starter" }) as HTMLButtonElement).disabled).toBe(true);
  });

  it("shows no mailbox words (U13)", async () => {
    await open("administrator", "/plans");
    expect(screen.getByRole("main").textContent).not.toMatch(/mailbox/i);
  });

  it("does not show Up to date after a failed check of an image (U9)", async () => {
    await open("administrator", "/updates");
    const row = screen.getByText("Node.js app").closest("tr")!;
    expect(row.textContent).toContain("Unknown: the last check failed");
    expect(row.textContent).not.toContain("Up to date");
  });

  it("names a certificate of the panel CA (U12)", async () => {
    await open("owner", () => `/websites/${website("api.acme.example.com").id}/domains`);
    expect(screen.getByText("api.acme.test").closest("tr")!.textContent).toContain("Certificate valid (panel CA, not trusted by browsers)");
    expect(screen.getAllByText("api.acme.example.com").at(-1)!.closest("tr")!.textContent).toContain("Certificate valid until");
  });

  it("shows the true isolation of a website (U3)", async () => {
    await open("administrator", () => `/websites/${website("shop.acme.example.com").id}/settings`);
    const text = screen.getByRole("main").textContent ?? "";
    expect(text).toContain("Own network namespace (pasta)");
    expect(text).toContain("The container of the website serves all files");
    expect(text).not.toMatch(/net-shopacme|edge proxy serves/);
  });

  it("shows that backups are not set up, with no button that can only fail (U5)", async () => {
    const api = await signedIn("owner");
    api.change<{ backups: { available: boolean } }>("/account", (body) => { body.backups.available = false; });
    const { refresh } = await import("../api/store");
    await refresh();
    render(<MemoryRouter initialEntries={[`/websites/${website("shop.acme.example.com").id}`]}><App /></MemoryRouter>);
    const card = screen.getByRole("heading", { name: "Backups" }).closest("section")!;
    expect(card.textContent).toContain("Backups are not set up");
    expect(within(card).queryByRole("button")).toBeNull();
  });

  it("shows the digest of the image, or that the image has none (U8)", async () => {
    await open("owner", () => `/websites/${website("shop.acme.example.com").id}`);
    expect(screen.getByRole("heading", { name: "Runtime" }).closest("section")!.textContent).toMatch(/sha256:[0-9a-f]{64}/);
    cleanup();
    reset();
    const api = await signedIn("owner");
    api.change<{ data: { name: string; runtime: { digest: string | null } }[] }>("/websites?perPage=100&page=1", (body) => { body.data.find((w) => w.name === "shop.acme.example.com")!.runtime.digest = null; });
    const { refresh } = await import("../api/store");
    await refresh();
    render(<MemoryRouter initialEntries={[`/websites/${website("shop.acme.example.com").id}`]}><App /></MemoryRouter>);
    expect(screen.getByRole("heading", { name: "Runtime" }).closest("section")!.textContent).toContain("No digest (tag only)");
  });
});
