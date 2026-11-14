// @vitest-environment jsdom
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { getState } from "../api/store";
import type { Server } from "../api/types";
import { apiJob, reset, signedIn, type PanelApi } from "../test/panelApi";
import { ServersPage } from "./ServersPage";

let api: PanelApi;
beforeEach(async () => { api = await signedIn("administrator"); });
afterEach(() => {
  cleanup();
  reset();
});

/** A server of the sample data. web-1 has websites and is the panel server. web-3 has one failed website. */
const server = (name = "web-2") => getState().servers.find((s) => s.name === name)!;
/** Changes the server in the answers of the API, for the load after a change. */
const inApi = (name: string, change: (s: Record<string, unknown>) => void) =>
  api.change<{ data: Record<string, unknown>[] }>("/servers", (body) => change(body.data.find((s) => s.name === name)!));
const packages = (list: Server["pendingUpdates"]) => { server().pendingUpdates = list; inApi("web-2", (s) => { s.pendingUpdates = list; }); };

const page = (name = "web-2") => render(
  <MemoryRouter initialEntries={[`/servers/${server(name).id}`]}>
    <Routes><Route path="/servers/:id" element={<ServersPage />} /></Routes>
  </MemoryRouter>,
);
const packageRows = () => screen.getAllByRole("row").filter((r) => r.textContent?.startsWith("pkg-"));

describe("the server page", () => {
  it("shows a long list of packages in a short form, with the security updates first", () => {
    packages(Array.from({ length: 20 }, (_, i) => ({ name: `pkg-${i}`, from: "1.0", to: "1.1", security: i >= 15 })));
    page();

    expect(packageRows()).toHaveLength(8);
    expect(packageRows()[0].textContent).toContain("pkg-15");
    expect(packageRows()[0].textContent).toContain("Security");
    fireEvent.click(screen.getByRole("button", { name: "Show all 20 packages" }));
    expect(packageRows()).toHaveLength(20);
    fireEvent.click(screen.getByRole("button", { name: "Show fewer packages" }));
    expect(packageRows()).toHaveLength(8);
  });

  it("shows no button for a short list of packages", () => {
    page();
    expect(screen.queryByRole("button", { name: /packages$/ })).toBeNull();
  });

  it("does not give a time for the last update before the first update", () => {
    // web-3 of the sample data installed no update.
    page("web-3");
    // Before, the page showed "just now" for a server that installed no update.
    expect(screen.getByRole("row", { name: /Last update/ }).textContent).toBe("Last updateNever");
  });

  it("does not let the user start the updates again while a job waits for the server", () => {
    getState().jobs.unshift({ id: "job_wait", title: "Install updates on web-2", state: "queued", steps: ["Download packages", "Install", "Health check"], step: 0, serverId: server().id, createdAt: new Date().toISOString() });
    page();
    expect((screen.getByRole("button", { name: "Install security updates" }) as HTMLButtonElement).disabled).toBe(true);
  });

  it("tells which packages an update installs", async () => {
    server().updatePolicy = "security";
    inApi("web-2", (s) => { s.updatePolicy = "security"; });
    packages([
      { name: "pkg-security", from: "1.0", to: "1.1", security: true },
      { name: "pkg-other", from: "1.0", to: "1.1", security: false },
    ]);
    // The API starts a job. When the job is complete, the server has only the packages that the policy leaves.
    api.on("POST /servers/*/install-updates", (body) => {
      const policy = (body as { policy?: string } | undefined)?.policy ?? "security";
      inApi("web-2", (s) => { s.pendingUpdates = policy === "all" ? [] : [{ name: "pkg-other", from: "1.0", to: "1.1", security: false }]; });
      return [202, { job: apiJob({ action: "InstallHostUpdates", title: "Install updates on web-2", serverId: server().id }) }];
    });
    page();
    // The policy of the server is "security": the default action leaves the other package.
    expect(screen.getByText(/The other package stays until you select Install all updates/)).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Install updates now" })).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "Install security updates" }));
    await waitFor(() => expect(server().pendingUpdates.map((p) => p.name)).toEqual(["pkg-other"]));

    // Only the other package is left: the page still gives a way to install it.
    await waitFor(() => expect((screen.getByRole("button", { name: "Install all updates" }) as HTMLButtonElement).disabled).toBe(false));
    expect(screen.queryByRole("button", { name: "Install security updates" })).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "Install all updates" }));
    await waitFor(() => expect(server().pendingUpdates).toEqual([]));
    expect(api.changes.filter((c) => c.path.endsWith("/install-updates")).map((c) => c.body)).toEqual([{ policy: "security" }, { policy: "all" }]);
  });

  it("has one action for a server that installs all updates", () => {
    server().updatePolicy = "all";
    packages([{ name: "pkg-other", from: "1.0", to: "1.1", security: false }]);
    page();
    expect(screen.getByRole("button", { name: "Install updates now" })).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Install all updates" })).toBeNull();
  });

  it("shows why the panel cannot connect to a server, and tests the connection again", async () => {
    Object.assign(server(), { state: "degraded", stateDetail: "Degraded: the panel cannot connect", connectionError: "Could not resolve host: web-2", agentAddress: "web-2:7443" });
    page();
    const alert = screen.getByRole("alert");
    expect(alert.textContent).toContain("The panel cannot connect to this server.");
    expect(alert.textContent).toContain("web-2:7443: Could not resolve host: web-2.");
    // The text for a service that does not run is not there: the cause is the connection.
    expect(screen.queryByText("This server is degraded.")).toBeNull();

    // The API tests the connection, and gives the server with the result.
    let answer: unknown;
    inApi("web-2", (s) => { answer = s; });
    api.on("POST /servers/*/check-connection", () => answer);
    fireEvent.click(screen.getByRole("button", { name: "Test the connection" }));
    await waitFor(() => expect(screen.queryByRole("alert")).toBeNull());
    expect(server().state).toBe("healthy");
  });

  it("removes a server only when it has no websites", async () => {
    page();
    // web-2 has websites.
    expect((screen.getByRole("button", { name: "Remove server" }) as HTMLButtonElement).disabled).toBe(true);
    expect(screen.getByText(/Delete the websites before you remove the server/)).toBeTruthy();
    cleanup();

    const id = server().id;
    const state = getState();
    state.websites = state.websites.filter((w) => w.serverId !== id);
    api.on("DELETE /servers/*", () => {
      api.change<{ data: { id: string }[] }>("/servers", (body) => { body.data = body.data.filter((s) => s.id !== id); });
      return [204, null];
    });
    page();
    fireEvent.click(screen.getByRole("button", { name: "Remove server" }));
    fireEvent.click(within(screen.getByRole("dialog")).getByRole("button", { name: "Remove server" }));
    await waitFor(() => expect(getState().servers.some((s) => s.id === id)).toBe(false));
    expect(api.changes.at(-1)).toMatchObject({ method: "DELETE", path: `/servers/${id}` });
  });

  it("has no remove action for the server of the panel", () => {
    page("web-1");
    expect(screen.queryByRole("button", { name: "Remove server" })).toBeNull();
  });
});

describe("the database servers of each customer", () => {
  /** Two websites of web-2 use the shared database containers of an earlier release. */
  const earlier = () => {
    const sites = getState().websites.filter((w) => w.serverId === server().id).slice(0, 2);
    sites.forEach((w) => { w.sharedDatabases = true; });
    Object.assign(server(), { sharedDatabaseWebsites: 2 });
    return sites;
  };

  it("moves the databases of the websites of an earlier release", async () => {
    const sites = earlier();
    api.on("POST /servers/*/move-databases", () => [202, { jobs: sites.map((w, i) => apiJob({ id: `move-${i}`, action: "MoveDatabases", title: `Move the databases of ${w.name}`, websiteId: w.id, serverId: server().id })) }]);
    page();
    expect(screen.getByText("2 websites on this server use the shared database containers of an earlier release.")).toBeTruthy();
    expect(screen.getByText(/Each website stops during the copy of its databases/)).toBeTruthy();

    fireEvent.click(screen.getByRole("button", { name: "Move the databases" }));
    const dialog = screen.getByRole("dialog");
    expect(dialog.textContent).toContain("Move the databases on web-2?");
    expect(dialog.textContent).toContain("If a job fails, the website starts again with the shared containers");
    fireEvent.click(within(dialog).getByRole("button", { name: "Move the databases" }));

    // The answers of the API have no website with the shared containers.
    await waitFor(() => expect(getState().websites.filter((w) => w.sharedDatabases)).toEqual([]));
    await waitFor(() => expect(screen.queryByText(/shared database containers of an earlier release/)).toBeNull());
    expect(api.changes.at(-1)).toMatchObject({ method: "POST", path: `/servers/${server().id}/move-databases` });
  });

  it("shows nothing for a server with no website of an earlier release, and for an agent of an earlier release", () => {
    page();
    expect(screen.queryByRole("button", { name: "Move the databases" })).toBeNull();
    cleanup();
    // The agent of an earlier release cannot move the databases. The page offers the update of the agent.
    earlier();
    server().databaseInstances = false;
    page();
    expect(screen.queryByRole("button", { name: "Move the databases" })).toBeNull();
  });

  it("counts the websites that the panel API reports", () => {
    // With many websites, the list of the panel can have a part of the websites only.
    Object.assign(server(), { sharedDatabaseWebsites: 1 });
    page();
    expect(screen.getByText("1 website on this server uses the shared database containers of an earlier release.")).toBeTruthy();
  });
});
