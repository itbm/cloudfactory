// @vitest-environment jsdom
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { afterEach, describe, expect, it } from "vitest";
import { App } from "../App";
import type { Role } from "../api/types";
import type { UptimeCheck } from "../api/uptime";
import { duration, intervalText, uptimeText } from "../api/uptime";
import { reset, signedIn, website, type PanelApi } from "../test/panelApi";

afterEach(() => {
  cleanup();
  reset();
});

async function open(role: Role, path: string | ((w: typeof website) => string)): Promise<PanelApi> {
  const api = await signedIn(role);
  render(<MemoryRouter initialEntries={[typeof path === "string" ? path : path(website)]}><App /></MemoryRouter>);
  return api;
}

const main = () => screen.getByRole("main");
/** The changes of the uptime checks, with no sign-in. */
const changes = (api: PanelApi) => api.changes.filter((c) => c.path.includes("/uptime-checks"));
/** The card with the heading. */
const card = (name: string) => within(main()).getByRole("heading", { name, level: 2 }).closest("section")!;

describe("the uptime checks", () => {
  it("shows the checks of a website, the details of a check and the result of a check now", async () => {
    const api = await open("owner", (w) => `/websites/${w("shop.acme.example.com").id}/uptime`);
    await waitFor(() => expect(within(main()).getByRole("button", { name: /^https:\/\/shop\.acme\.example\.com\/wp-json\// })).toBeTruthy());
    const rows = within(main()).getAllByRole("row").slice(1);
    expect(rows).toHaveLength(2);
    expect(rows[0].textContent).toContain("Up");
    expect(rows[0].textContent).toContain("Every 1 minute · Status 200 to 399");
    // The first check shows its details: the last 24 hours, the last 30 days and the incidents.
    await waitFor(() => expect(within(main()).getByRole("heading", { name: "Incidents" })).toBeTruthy());
    expect(within(main()).getByRole("heading", { name: "Last 24 hours" })).toBeTruthy();
    expect(within(main()).getByRole("heading", { name: "Response time" })).toBeTruthy();
    expect(main().textContent).toContain("HTTP status 502.");
    expect(main().textContent).toContain("4 minutes");

    const check = (await (await fetch(`/api/v1/websites/${website("shop.acme.example.com").id}/uptime-checks`)).json() as { data: UptimeCheck[] }).data[0];
    api.on("POST /websites/*/uptime-checks/*/run", () => ({ uptimeCheck: check, result: { ok: true, status: 200, responseMs: 120, error: null } }));
    fireEvent.click(within(main()).getByRole("button", { name: "Check now" }));
    await waitFor(() => expect(main().textContent).toContain("The check succeeded: HTTP status 200 in 120 ms."));
    expect(changes(api).map((c) => `${c.method} ${c.path}`)).toEqual([`POST /websites/${check.websiteId}/uptime-checks/${check.id}/run`]);
  });

  it("shows a check that is down with its cause, also on the overview of the website", async () => {
    await open("owner", (w) => `/websites/${w("storefront.acme.example.com").id}/uptime`);
    await waitFor(() => expect(within(main()).getByRole("alert").textContent).toContain("This address is down."));
    expect(within(main()).getByRole("alert").textContent).toContain('The answer does not have the text "Order now".');
    expect(within(main()).getAllByRole("row")[1].textContent).toContain('Status 200 to 399 and the text "Order now"');
    cleanup();
    render(<MemoryRouter initialEntries={[`/websites/${website("storefront.acme.example.com").id}`]}><App /></MemoryRouter>);
    await waitFor(() => expect(card("Uptime").textContent).toContain("Down"));
    expect(card("Uptime").textContent).toContain("Order now");
  });

  it("adds the first check of a website with one click, and shows the errors of the form", async () => {
    const api = await open("administrator", (w) => `/websites/${w("reports.northwind.example.com").id}/uptime`);
    const add = await within(main()).findByRole("button", { name: "Check https://reports.northwind.example.com/ every 5 minutes" });
    api.on("POST /websites/*/uptime-checks", (body) => [201, { uptimeCheck: { id: "01new", ...(body as object) } }]);
    fireEvent.click(add);
    await waitFor(() => expect(changes(api)).toHaveLength(1));
    expect(changes(api)[0].body).toEqual({ domain: "reports.northwind.example.com", scheme: "https", path: "/", intervalSeconds: 300, timeoutSeconds: 10, expectedStatus: null, keyword: null, failuresBeforeAlert: 2, enabled: true });

    api.on("POST /websites/*/uptime-checks", () => [422, { message: "The path is not valid.", errors: { path: ['Write a path that starts with "/", for example "/health". Do not use spaces or "#".'] } }]);
    fireEvent.click(within(main()).getByRole("button", { name: "Add a different check" }));
    const dialog = screen.getByRole("dialog", { name: "Add an uptime check" });
    fireEvent.change(within(dialog).getByLabelText("Path"), { target: { value: "health" } });
    fireEvent.change(within(dialog).getByLabelText("Expected HTTP status"), { target: { value: "204" } });
    fireEvent.change(within(dialog).getByLabelText("Interval"), { target: { value: "60" } });
    fireEvent.click(within(dialog).getByRole("button", { name: "Add check" }));
    await waitFor(() => expect(within(dialog).getByLabelText("Path").getAttribute("aria-invalid")).toBe("true"));
    expect(dialog.textContent).toContain('Write a path that starts with "/"');
    expect(changes(api)[1].body).toMatchObject({ path: "health", expectedStatus: 204, intervalSeconds: 60 });
  });

  it("changes, turns off and removes a check", async () => {
    const api = await open("owner", (w) => `/websites/${w("api.acme.example.com").id}/uptime`);
    const menu = await within(main()).findByRole("button", { name: "Actions for https://api.acme.example.com/up" });
    api.on("PATCH /websites/*/uptime-checks/*", (body) => ({ uptimeCheck: body }));
    api.on("DELETE /websites/*/uptime-checks/*", () => [204, null]);

    fireEvent.click(menu);
    fireEvent.click(screen.getByRole("menuitem", { name: "Edit" }));
    const dialog = screen.getByRole("dialog", { name: "Change the uptime check" });
    expect((within(dialog).getByLabelText("Path") as HTMLInputElement).value).toBe("/up");
    fireEvent.change(within(dialog).getByLabelText("Text in the answer"), { target: { value: "ok" } });
    fireEvent.click(within(dialog).getByRole("button", { name: "Save" }));
    await waitFor(() => expect(changes(api)).toHaveLength(1));
    expect(changes(api)[0]).toMatchObject({ method: "PATCH", body: { path: "/up", keyword: "ok", intervalSeconds: 60 } });

    fireEvent.click(within(main()).getByRole("button", { name: "Actions for https://api.acme.example.com/up" }));
    fireEvent.click(screen.getByRole("menuitem", { name: "Turn off" }));
    await waitFor(() => expect(changes(api)).toHaveLength(2));
    expect(changes(api)[1].body).toEqual({ enabled: false });

    fireEvent.click(within(main()).getByRole("button", { name: "Actions for https://api.acme.example.com/up" }));
    fireEvent.click(screen.getByRole("menuitem", { name: "Remove" }));
    fireEvent.click(within(screen.getByRole("dialog", { name: "Remove this uptime check?" })).getByRole("button", { name: "Remove" }));
    await waitFor(() => expect(changes(api)).toHaveLength(3));
    expect(changes(api)[2].method).toBe("DELETE");
  });

  it("has no uptime tab for a worker", async () => {
    await open("owner", (w) => `/websites/${w("queue-00b1.internal").id}`);
    const tabs = within(screen.getByRole("navigation", { name: "Website sections" })).getAllByRole("link").map((a) => a.textContent);
    expect(tabs).not.toContain("Uptime");
    expect(tabs).toContain("Usage");
  });

  it("shows the checks of all websites, the checks that are down first, and the incidents", async () => {
    await open("administrator", "/uptime");
    expect(screen.getByRole("heading", { level: 1 }).textContent).toBe("Uptime checks");
    await waitFor(() => expect(within(main()).getAllByRole("row").length).toBeGreaterThan(5));
    expect(within(main()).getByRole("region", { name: "Down" }).textContent).toContain("1of 10 checks");
    const checks = within(card("Checks")).getAllByRole("row").slice(1);
    expect(checks).toHaveLength(10);
    expect(checks[0].textContent).toContain("storefront.acme.example.com");
    expect(checks[0].textContent).toContain("Down");
    fireEvent.click(within(main()).getByRole("button", { name: "Paused (2)" }));
    expect(within(card("Checks")).getAllByRole("row").slice(1).map((r) => r.querySelector("a")?.textContent)).toEqual(["dashboard.acme.example.com", "old.acme.example.com"]);

    await waitFor(() => expect(within(card("Incidents")).getAllByRole("row").length).toBeGreaterThan(2));
    const incidents = card("Incidents");
    const first = within(incidents).getAllByRole("row")[1];
    expect(first.textContent).toContain("Still down");
    expect(first.textContent).toContain("storefront.acme.example.com");
    expect(incidents.textContent).toContain("41 minutes");
  });

  it("formats the times and the percents", () => {
    expect(duration(30)).toBe("less than 1 minute");
    expect(duration(3900)).toBe("1 hour 5 minutes");
    expect(duration(90061)).toBe("1 day 1 hour");
    expect(intervalText(60)).toBe("1 minute");
    expect(intervalText(1800)).toBe("30 minutes");
    expect(intervalText(3600)).toBe("1 hour");
    expect(uptimeText(100)).toBe("100%");
    expect(uptimeText(99.5)).toBe("99.50%");
    expect(uptimeText(null)).toBe("No data");
  });
});
