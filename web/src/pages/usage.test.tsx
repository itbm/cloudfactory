import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { afterEach, describe, expect, it } from "vitest";
import { App } from "../App";
import type { Role } from "../api/types";
import { reset, signedIn, usageHistory, website, type PanelApi } from "../test/panelApi";

afterEach(() => {
  cleanup();
  reset();
});

async function open(role: Role, path: string | ((w: typeof website) => string)): Promise<PanelApi> {
  const api = await signedIn(role);
  render(<MemoryRouter initialEntries={[typeof path === "string" ? path : path(website)]}><App /></MemoryRouter>);
  return api;
}

describe("the usage graphs and the usage alerts", () => {
  it("shows the graphs of a website with the bandwidth of the month, and reads a range", async () => {
    const api = await open("owner", (w) => `/websites/${w("shop.acme.example.com").id}/usage`);
    const ranges: string[] = [];
    api.on("GET /websites/*/usage?range=*", (_b, path) => {
      ranges.push(path.split("range=")[1]);
      return usageHistory(path);
    });
    const main = screen.getByRole("main");
    await waitFor(() => expect(within(main).getByRole("heading", { name: "Traffic" })).toBeTruthy());
    for (const name of ["CPU", "Memory", "Disk"]) expect(within(main).getByRole("heading", { name })).toBeTruthy();
    expect(main.textContent).toContain("12 GB of 500 GB");
    // Each graph tells its last value to a screen reader.
    expect(within(main).getByRole("group", { name: /^Memory\. Last value 512 MB/ })).toBeTruthy();
    fireEvent.click(within(main).getByRole("button", { name: "30 days" }));
    await waitFor(() => expect(ranges).toContain("30d"));
  });

  it("shows an owner the open alerts of the customer, with no alert of a server", async () => {
    await open("owner", "/alerts");
    const main = screen.getByRole("main");
    expect(screen.getByRole("heading", { level: 1 }).textContent).toBe("Usage alerts");
    const row = within(main).getByText("shop.acme.example.com").closest("tr")!;
    expect(row.textContent).toContain("Warning");
    expect(row.textContent).toContain("Memory");
    expect(main.textContent).not.toContain("harbourbooks");
    expect(within(main).queryByRole("button", { name: "Save thresholds" })).toBeNull();
    // The overview of the website tells about the alert.
    cleanup();
    render(<MemoryRouter initialEntries={[`/websites/${website("shop.acme.example.com").id}`]}><App /></MemoryRouter>);
    expect(screen.getByRole("main").textContent).toContain("Memory at 85% of the limit.");
  });

  it("lets an administrator see all alerts, the critical first, and change the thresholds", async () => {
    const api = await open("administrator", "/alerts");
    const main = screen.getByRole("main");
    const rows = within(main).getAllByRole("row").slice(1);
    expect(rows[0].textContent).toContain("Critical");
    expect(rows[0].textContent).toContain("harbourbooks.example.net");
    api.on("PUT /usage-alerts/settings", (body) => body);
    fireEvent.change(within(main).getByLabelText("Warning at (%)"), { target: { value: "70" } });
    fireEvent.click(within(main).getByRole("button", { name: "Save thresholds" }));
    await waitFor(() => expect(api.changes).toContainEqual({ method: "PUT", path: "/usage-alerts/settings", body: { warningPercent: 70, criticalPercent: 95 } }));
  });

  it("shows the graphs of a server to an administrator", async () => {
    await open("administrator", "/servers");
    cleanup();
    const { getState } = await import("../api/store");
    render(<MemoryRouter initialEntries={[`/servers/${getState().servers[0].id}`]}><App /></MemoryRouter>);
    const main = screen.getByRole("main");
    await waitFor(() => expect(within(main).getByRole("heading", { name: "Network traffic" })).toBeTruthy());
    expect(within(main).getByRole("group", { name: /^Disk\. Last value 30%/ })).toBeTruthy();
  });
});
