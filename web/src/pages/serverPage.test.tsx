// @vitest-environment jsdom
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, describe, expect, it } from "vitest";
import { getState, resetState, timing } from "../api/store";
import { ServersPage } from "./ServersPage";

afterEach(cleanup);

const page = (id = "srv_web01") => render(
  <MemoryRouter initialEntries={[`/servers/${id}`]}>
    <Routes><Route path="/servers/:id" element={<ServersPage />} /></Routes>
  </MemoryRouter>,
);
const packageRows = () => screen.getAllByRole("row").filter((r) => r.textContent?.startsWith("pkg-"));

describe("the server page", () => {
  it("shows a long list of packages in a short form, with the security updates first", () => {
    resetState();
    const server = getState().servers.find((s) => s.id === "srv_web01")!;
    server.pendingUpdates = Array.from({ length: 20 }, (_, i) => ({ name: `pkg-${i}`, from: "1.0", to: "1.1", security: i >= 15 }));
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
    resetState();
    page();
    expect(screen.queryByRole("button", { name: /packages$/ })).toBeNull();
  });

  it("does not give a time for the last update before the first update", () => {
    resetState();
    getState().servers.find((s) => s.id === "srv_web01")!.lastUpdateAt = "";
    page();
    // Before, the page showed "just now" for a server that installed no update.
    expect(screen.getByRole("row", { name: /Last update/ }).textContent).toBe("Last updateNever");
  });

  it("does not let the user start the updates again while a job waits for the server", () => {
    resetState();
    const state = getState();
    state.jobs.unshift({ id: "job_wait", title: "Install updates on web-01", state: "queued", steps: ["Download packages", "Install", "Health check"], step: 0, serverId: "srv_web01", createdAt: new Date().toISOString() });
    page();
    expect((screen.getByRole("button", { name: "Install security updates" }) as HTMLButtonElement).disabled).toBe(true);
  });

  it("tells which packages an update installs", async () => {
    resetState();
    timing.stepMs = 0;
    const server = () => getState().servers.find((s) => s.id === "srv_web01")!;
    server().pendingUpdates = [
      { name: "pkg-security", from: "1.0", to: "1.1", security: true },
      { name: "pkg-other", from: "1.0", to: "1.1", security: false },
    ];
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
  });

  it("has one action for a server that installs all updates", () => {
    resetState();
    const server = getState().servers.find((s) => s.id === "srv_web01")!;
    server.updatePolicy = "all";
    server.pendingUpdates = [{ name: "pkg-other", from: "1.0", to: "1.1", security: false }];
    page();
    expect(screen.getByRole("button", { name: "Install updates now" })).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Install all updates" })).toBeNull();
  });

  it("shows why the panel cannot connect to a server, and tests the connection again", async () => {
    resetState();
    const server = () => getState().servers.find((s) => s.id === "srv_web01")!;
    Object.assign(server(), { state: "degraded", connectionError: "Could not resolve host: web-01", agentAddress: "web-01:7443" });
    page();
    const alert = screen.getByRole("alert");
    expect(alert.textContent).toContain("The panel cannot connect to this server.");
    expect(alert.textContent).toContain("web-01:7443: Could not resolve host: web-01.");
    // The text for a service that does not run is not there: the cause is the connection.
    expect(screen.queryByText("This server is degraded.")).toBeNull();

    fireEvent.click(screen.getByRole("button", { name: "Test the connection" }));
    await waitFor(() => expect(screen.queryByRole("alert")).toBeNull());
    expect(server().state).toBe("healthy");
  });

  it("removes a server only when it has no websites", async () => {
    resetState();
    page();
    // web-01 has websites.
    expect((screen.getByRole("button", { name: "Remove server" }) as HTMLButtonElement).disabled).toBe(true);
    expect(screen.getByText(/Delete the websites before you remove the server/)).toBeTruthy();
    cleanup();

    const state = getState();
    state.websites = state.websites.filter((w) => w.serverId !== "srv_web01");
    page();
    fireEvent.click(screen.getByRole("button", { name: "Remove server" }));
    fireEvent.click(within(screen.getByRole("dialog")).getByRole("button", { name: "Remove server" }));
    await waitFor(() => expect(getState().servers.some((s) => s.id === "srv_web01")).toBe(false));
  });

  it("has no remove action for the server of the panel", () => {
    resetState();
    getState().servers.find((s) => s.id === "srv_web01")!.panel = true;
    page();
    expect(screen.queryByRole("button", { name: "Remove server" })).toBeNull();
  });
});

describe("the database containers of each customer", () => {
  it("moves the databases of the websites of an earlier release", async () => {
    resetState();
    timing.stepMs = 0;
    // The demo data has two websites of an earlier release on web-02.
    getState().servers.find((s) => s.id === "srv_web02")!.state = "healthy";
    const shared = () => getState().websites.filter((w) => w.sharedDatabases);
    page("srv_web02");
    expect(screen.getByText("2 websites on this server use the shared database containers of an earlier release.")).toBeTruthy();
    expect(screen.getByText(/Each website stops during the copy of its databases/)).toBeTruthy();

    fireEvent.click(screen.getByRole("button", { name: "Move the databases" }));
    const dialog = screen.getByRole("dialog");
    expect(dialog.textContent).toContain("Move the databases on web-02?");
    expect(dialog.textContent).toContain("If a job fails, the website starts again with the shared containers");
    fireEvent.click(within(dialog).getByRole("button", { name: "Move the databases" }));

    await waitFor(() => expect(shared()).toEqual([]));
    await waitFor(() => expect(screen.queryByText(/shared database containers of an earlier release/)).toBeNull());
    expect(getState().jobs.filter((j) => j.serverId === "srv_web02" && j.title.startsWith("Move the databases of ")).map((j) => j.state)).toEqual(["succeeded", "succeeded"]);
  });

  it("shows nothing for a server with no website of an earlier release, and for an agent of an earlier release", () => {
    resetState();
    page();
    expect(screen.queryByRole("button", { name: "Move the databases" })).toBeNull();
    cleanup();
    // The agent of an earlier release cannot move the databases. The page offers the update of the agent.
    getState().servers.find((s) => s.id === "srv_web02")!.databaseInstances = false;
    page("srv_web02");
    expect(screen.queryByRole("button", { name: "Move the databases" })).toBeNull();
  });

  it("counts the websites that the panel API reports", () => {
    resetState();
    // In live mode, the list of the panel can have a part of the websites only.
    Object.assign(getState().servers.find((s) => s.id === "srv_web01")!, { sharedDatabaseWebsites: 1 });
    page();
    expect(screen.getByText("1 website on this server uses the shared database containers of an earlier release.")).toBeTruthy();
  });
});
