// @vitest-environment jsdom
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { afterEach, describe, expect, it } from "vitest";
import { App } from "../App";
import type { WebsiteSecurity } from "../api/types";
import { apiJob, reset, signedIn, website, type PanelApi } from "../test/panelApi";

afterEach(() => {
  cleanup();
  reset();
});

/** Signs in as an owner and opens the Security tab of a website of the sample data. */
async function open(name: string): Promise<{ api: PanelApi; id: string }> {
  const api = await signedIn("owner");
  const id = website(name).id;
  render(<MemoryRouter initialEntries={[`/websites/${id}/security`]}><App /></MemoryRouter>);
  await screen.findByRole("heading", { name: "Firewall" });
  return { api, id };
}

/** The answer of the API to a change: the protection of the sample data and a job. */
function answer(api: PanelApi, id: string) {
  let security: WebsiteSecurity | undefined;
  api.change<{ security: WebsiteSecurity }>(`/websites/${id}/security`, (body) => { security = body.security; });
  api.on(`PATCH /websites/${id}/security`, () => [202, { security, job: apiJob({ title: "Set website protection", action: "SetWebsiteSecurity", websiteId: id }) }]);
}

describe("the Security tab", () => {
  it("shows the protection of the website and the firewall events", async () => {
    await open("acme.example.com");
    expect((screen.getByRole("radio", { name: /Detect/ }) as HTMLInputElement).checked).toBe(true);
    expect((screen.getByLabelText("Blocked addresses") as HTMLTextAreaElement).value).toBe("198.51.100.23\n203.0.113.0/24");
    expect((screen.getByLabelText("Requests in one minute") as HTMLInputElement).value).toBe("10");
    const paths = screen.getByRole("heading", { name: "Password-protected paths" }).closest("section")!;
    expect(within(paths).getByRole("cell", { name: "/reports/" })).toBeTruthy();
    expect(within(paths).getByRole("cell", { name: "acme" })).toBeTruthy();
    // The demo agent gives sample events. An event of the mode detect has no host.
    const events = (await screen.findAllByText("Detected")).length;
    expect(events).toBeGreaterThan(0);
    expect(screen.queryByText("Blocked")).toBeNull();
  });

  it("saves the firewall mode with the rules and the paths", async () => {
    const { api, id } = await open("acme.example.com");
    answer(api, id);
    fireEvent.click(screen.getByRole("radio", { name: /Block/ }));
    fireEvent.change(screen.getByLabelText("Rules that are off"), { target: { value: "942100, 920450\n" } });
    fireEvent.change(screen.getByLabelText("Paths with no firewall"), { target: { value: "/wp-admin/\n\n/api/" } });
    fireEvent.click(screen.getByRole("button", { name: "Save firewall" }));
    await waitFor(() => expect(api.changes.at(-1)?.method).toBe("PATCH"));
    expect(api.changes.at(-1)).toEqual({ method: "PATCH", path: `/websites/${id}/security`, body: { firewall: { mode: "block", excludedRules: [942100, 920450], skippedPaths: ["/wp-admin/", "/api/"] } } });
  });

  it("turns off a rule of a firewall event after a confirmation", async () => {
    const { api, id } = await open("acme.example.com");
    answer(api, id);
    const [button] = await screen.findAllByRole("button", { name: /^Turn off this rule/ });
    const rule = Number(button.textContent!.replace(/\D/g, ""));
    fireEvent.click(button);
    fireEvent.click(within(screen.getByRole("dialog")).getByRole("button", { name: "Turn off" }));
    await waitFor(() => expect(api.changes.at(-1)?.method).toBe("PATCH"));
    expect(api.changes.at(-1)?.body).toEqual({ firewall: { mode: "detect", excludedRules: [rule] } });
  });

  it("shows the error of the API next to the value that is not correct", async () => {
    const { api, id } = await open("acme.example.com");
    api.on(`PATCH /websites/${id}/security`, () => [422, { message: "Enter an IP address or a range.", errors: { "ipRules.block.1": ["Enter an IP address or a range, for example 203.0.113.10."] } }]);
    fireEvent.change(screen.getByLabelText("Blocked addresses"), { target: { value: "198.51.100.23\n203.0.113.300" } });
    fireEvent.click(screen.getByRole("button", { name: "Save addresses" }));
    expect(await screen.findByText("203.0.113.300: Enter an IP address or a range, for example 203.0.113.10.")).toBeTruthy();
    expect(api.changes.at(-1)?.body).toEqual({ ipRules: { allow: [], block: ["198.51.100.23", "203.0.113.300"] } });
  });

  it("adds a user to a protected path and keeps the passwords of the other users", async () => {
    const { api, id } = await open("acme.example.com");
    answer(api, id);
    const paths = screen.getByRole("heading", { name: "Password-protected paths" }).closest("section")!;
    fireEvent.click(within(paths).getByRole("button", { name: /^Change\s*\/reports\/$/ }));
    const dialog = screen.getByRole("dialog");
    fireEvent.click(within(dialog).getByRole("button", { name: "Add a user" }));
    const names = within(dialog).getAllByLabelText("User name");
    fireEvent.change(names[1], { target: { value: "auditor" } });
    fireEvent.change(within(dialog).getByLabelText("Password"), { target: { value: "correct horse" } });
    fireEvent.click(within(dialog).getByRole("button", { name: "Save" }));
    await waitFor(() => expect(api.changes.at(-1)?.method).toBe("PATCH"));
    expect(api.changes.at(-1)?.body).toEqual({ protectedPaths: [{ path: "/reports/", users: [{ name: "acme" }, { name: "auditor", password: "correct horse" }] }] });
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
  });

  it("offers the sign-in paths of WordPress", async () => {
    const { api, id } = await open("shop.acme.example.com");
    answer(api, id);
    const limit = screen.getByLabelText("Requests in one minute");
    expect((limit as HTMLInputElement).value).toBe("0");
    expect((screen.getByLabelText("Sign-in paths") as HTMLTextAreaElement).value).toBe("/wp-login.php\n/xmlrpc.php");
    fireEvent.change(limit, { target: { value: "10" } });
    fireEvent.click(screen.getByRole("button", { name: "Save limit" }));
    await waitFor(() => expect(api.changes.at(-1)?.method).toBe("PATCH"));
    expect(api.changes.at(-1)?.body).toEqual({ signInLimit: { requestsPerMinute: 10, paths: ["/wp-login.php", "/xmlrpc.php"] } });
  });
});

describe("a website with no domains", () => {
  it("has no Security tab", async () => {
    await signedIn("owner");
    const worker = (await import("../api/store")).getState().websites.find((w) => w.kind !== "public");
    expect(worker).toBeTruthy();
    render(<MemoryRouter initialEntries={[`/websites/${worker!.id}`]}><App /></MemoryRouter>);
    const tabs = await screen.findByRole("navigation", { name: "Website sections" });
    expect(within(tabs).queryByRole("link", { name: "Security" })).toBeNull();
  });
});
