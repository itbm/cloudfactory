// @vitest-environment jsdom
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { afterEach, describe, expect, it } from "vitest";
import { App } from "../App";
import type { MalwareScans, WebsiteSecurity } from "../api/types";
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

describe("the malware scan", () => {
  /** The card of the malware scan, when it has loaded. */
  async function card() {
    const heading = await screen.findByRole("heading", { name: "Malware scan" });
    const section = heading.closest("section")!;
    await within(section).findByLabelText("Schedule");
    return section;
  }

  it("shows the file that the last scan found, and deletes it after a confirmation", async () => {
    const { api, id } = await open("shop.acme.example.com");
    const scans = await card();
    expect(within(scans).getAllByText("Malware found").length).toBeGreaterThan(0);
    expect(within(scans).getByRole("cell", { name: "/public/uploads/invoice-2026-11.php" })).toBeTruthy();
    expect(within(scans).getByRole("cell", { name: "Eicar-Test-Signature" })).toBeTruthy();
    // The earlier scan of the shop found no malware.
    expect(within(scans).getByRole("heading", { name: "Earlier scans" })).toBeTruthy();
    expect(within(scans).getByText("No malware")).toBeTruthy();

    api.on(`DELETE /websites/${id}/files?path=*`, () => {
      api.change<MalwareScans>(`/websites/${id}/malware-scans`, (body) => { body.scans[0].findings[0].removed = true; });
      return [204, null];
    });
    fireEvent.click(within(scans).getByRole("button", { name: /^Delete file/ }));
    fireEvent.click(within(screen.getByRole("dialog")).getByRole("button", { name: "Delete file" }));
    await waitFor(() => expect(api.changes.at(-1)).toEqual({ method: "DELETE", path: `/websites/${id}/files?path=%2Fpublic%2Fuploads%2Finvoice-2026-11.php`, body: undefined }));
    expect(await within(scans).findByText("Deleted")).toBeTruthy();
  });

  it("starts a scan now and changes the schedule", async () => {
    const { api, id } = await open("acme.example.com");
    const scans = await card();
    expect(within(scans).getAllByText("No malware")).toHaveLength(2);
    expect(within(scans).getByText(/Next scan/)).toBeTruthy();
    const scan = { id: "01scan", trigger: "manual", state: "running", scannedFiles: 0, infectedFiles: 0, errors: 0, signatures: 0, engine: null, findings: [], truncated: false, error: null, jobId: "01job", startedAt: new Date().toISOString(), finishedAt: null };
    api.on(`POST /websites/${id}/malware-scans`, () => [202, { scan, job: apiJob({ title: "Scan for malware", action: "ScanWebsite", websiteId: id }) }]);
    fireEvent.click(within(scans).getByRole("button", { name: "Scan now" }));
    await waitFor(() => expect(api.changes.at(-1)).toEqual({ method: "POST", path: `/websites/${id}/malware-scans`, body: undefined }));

    api.on(`PATCH /websites/${id}/malware-scans`, (body) => ({ schedule: (body as { schedule: string }).schedule, nextScanAt: null, scans: [] }));
    fireEvent.change(within(scans).getByLabelText("Schedule"), { target: { value: "off" } });
    await waitFor(() => expect(api.changes.at(-1)).toEqual({ method: "PATCH", path: `/websites/${id}/malware-scans`, body: { schedule: "off" } }));
    expect(await within(scans).findByText("No scheduled scans.")).toBeTruthy();
  });
});

describe("a website with no domains", () => {
  it("has only the malware scan on the Security tab", async () => {
    await signedIn("owner");
    const worker = (await import("../api/store")).getState().websites.find((w) => w.kind !== "public");
    expect(worker).toBeTruthy();
    render(<MemoryRouter initialEntries={[`/websites/${worker!.id}/security`]}><App /></MemoryRouter>);
    const tabs = await screen.findByRole("navigation", { name: "Website sections" });
    expect(within(tabs).getByRole("link", { name: "Security" })).toBeTruthy();
    expect(within(tabs).queryByRole("link", { name: "Domains" })).toBeNull();
    expect(await screen.findByRole("heading", { name: "Malware scan" })).toBeTruthy();
    expect(screen.getByRole("heading", { name: "No protection in the edge proxy" })).toBeTruthy();
    expect(screen.queryByRole("heading", { name: "Firewall" })).toBeNull();
  });
});
