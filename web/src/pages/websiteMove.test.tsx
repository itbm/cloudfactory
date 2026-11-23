// @vitest-environment jsdom
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { afterEach, describe, expect, it } from "vitest";
import { App } from "../App";
import { getState } from "../api/store";
import type { Role } from "../api/types";
import { apiJob, fixture, reset, signedIn, website, type PanelApi } from "../test/panelApi";

afterEach(() => {
  cleanup();
  reset();
});

// acme.example.com is on web-1, with SFTP and with databases on web-1.
const open = async (role: Role): Promise<PanelApi> => {
  const api = await signedIn(role);
  render(<MemoryRouter initialEntries={[`/websites/${website("acme.example.com").id}`]}><App /></MemoryRouter>);
  fireEvent.click(await screen.findByRole("button", { name: "More actions for acme.example.com" }));
  return api;
};
const items = () => screen.getAllByRole("menuitem").map((b) => b.textContent);
const server = (name: string) => getState().servers.find((s) => s.name === name)!;
// The websites as the API gives them.
const websites = fixture<{ data: { id: string }[] }>("administrator", "GET /websites?perPage=100&page=1").body.data;

describe("the move of a website", () => {
  it("is only for an administrator", async () => {
    await open("owner");
    expect(items()).not.toContain("Move to a different server");
  });

  it("moves a website to a different server", async () => {
    const api = await open("administrator");
    const site = website("acme.example.com");
    api.on(`POST /websites/${site.id}/move`, () => [202, {
      website: { ...websites.find((w) => w.id === site.id), status: "moving", movingToServerId: server("web-2").id },
      jobs: [apiJob({ action: "ReceiveWebsite", websiteId: site.id, state: "running" }), apiJob({ id: "job-2", action: "SendWebsite", websiteId: site.id, state: "running" })],
    }]);
    fireEvent.click(screen.getByRole("menuitem", { name: "Move to a different server" }));
    const dialog = screen.getByRole("dialog");
    // The list has the other servers with the web role. A healthy server is first.
    expect(within(dialog).getAllByRole("option").map((o) => o.textContent)).toEqual(["web-2", "web-3 · degraded"]);
    expect((within(dialog).getByLabelText("New server") as HTMLSelectElement).value).toBe(server("web-2").id);
    // The user reads what changes: the DNS records, the database host names and the SFTP host.
    expect(dialog.textContent).toContain("203.0.113.20");
    expect(dialog.textContent).toContain("loses the connection");
    expect(dialog.textContent).toContain("The SFTP host changes to the host of the new server.");
    fireEvent.click(within(dialog).getByRole("button", { name: "Move website" }));

    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    expect(api.changes.find((c) => c.path === `/websites/${site.id}/move`)).toEqual({ method: "POST", path: `/websites/${site.id}/move`, body: { serverId: server("web-2").id } });
  });

  it("shows a website that moves, with no changes", async () => {
    await signedIn("administrator");
    const site = website("acme.example.com");
    Object.assign(site, { status: "moving", movingToServerId: server("web-2").id });
    render(<MemoryRouter initialEntries={[`/websites/${site.id}`]}><App /></MemoryRouter>);
    expect(await screen.findByText(/This website moves to the server web-2 now/)).toBeTruthy();
    expect(screen.getAllByText("Moving").length).toBeGreaterThan(0);
    fireEvent.click(screen.getByRole("button", { name: "More actions for acme.example.com" }));
    for (const name of ["Start", "Restart", "Make staging copy", "Move to a different server", "Remove website"]) {
      expect((screen.getByRole("menuitem", { name }) as HTMLButtonElement).disabled).toBe(true);
    }
  });
});
