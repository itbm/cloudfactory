// @vitest-environment jsdom
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { getState } from "../api/store";
import type { Mesh } from "../api/types";
import { canOpen, NAVIGATION } from "../lib/roles";
import { apiJob, reset, signedIn, type PanelApi } from "../test/panelApi";
import { NetworkPage } from "./NetworkPage";

let api: PanelApi;
beforeEach(async () => { api = await signedIn("administrator"); });
afterEach(() => {
  cleanup();
  reset();
});

/** The mesh of the sample data: web-1 and web-2 have one tunnel. The agent of web-3 is from an earlier release. */
const mesh = () => getState().mesh!;
const server = (name: string) => mesh().servers.find((s) => s.name === name)!;
/** Changes the mesh in the answers of the API, for the load after a change. */
const inApi = (change: (m: Mesh) => void) => api.change<Mesh>("/mesh", change);
const page = () => render(<MemoryRouter><NetworkPage /></MemoryRouter>);
const row = (name: RegExp) => screen.getByRole("row", { name });
/** The row of a server in the table of the servers. */
const serverRow = (name: string) => within(screen.getByRole("heading", { name: "Servers" }).closest("section")!).getByRole("row", { name: new RegExp(`^${name}`) });
/** Opens the menu of a table row and selects an action. */
const select = (menu: string, action: string) => {
  fireEvent.click(screen.getByRole("button", { name: menu }));
  fireEvent.click(screen.getByRole("menuitem", { name: action }));
};
/** The changes that the test made after the sign-in. */
const changes = () => api.changes.filter((c) => !c.path.startsWith("/auth/"));
/** A job of the mesh. The list of the jobs of the API has it also, for the load after the job. */
const meshJob = (over: Record<string, unknown> = {}) => {
  const job = apiJob({ action: "SetMesh", title: "Set the tunnels of web-1", ...over });
  api.change<{ data: unknown[] }>("/jobs?perPage=50", (body) => { body.data.unshift(job); });
  return job;
};

describe("the network page", () => {
  it("is a page of the administrator only", () => {
    expect(NAVIGATION.administrator.flatMap((s) => s.items).map((i) => i.to)).toContain("/network");
    expect(canOpen("administrator", "/network")).toBe(true);
    expect(canOpen("reseller", "/network")).toBe(false);
    expect(canOpen("owner", "/network")).toBe(false);
  });

  it("shows each tunnel with its state, and each server with its addresses", () => {
    page();
    const tunnel = row(/web-1 and web-2/);
    expect(tunnel.textContent).toContain("Up");
    expect(tunnel.textContent).toContain("7.6 ms");
    expect(serverRow("web-1").textContent).toContain("10.207.0.1");
    expect(serverRow("web-1").textContent).toContain("203.0.113.10");
    expect(serverRow("web-1").textContent).toContain("In the mesh");
    // The agent of web-3 cannot have tunnels. The page tells the administrator what to do.
    expect(serverRow("web-3").textContent).toContain("Update the agent");
    expect(screen.getByRole("status").textContent).toContain("web-3 cannot have tunnels.");
    // The range and the port cannot change while the mesh has a tunnel.
    expect((screen.getByLabelText("Address range") as HTMLInputElement).disabled).toBe(true);
  });

  it("tells which server gets no answer through a tunnel that is down", () => {
    const t = mesh().tunnels[0];
    Object.assign(t, { state: "down", roundTripMs: null });
    Object.assign(t.sides[1], { reachable: false, roundTripMs: null });
    page();
    expect(row(/web-1 and web-2/).textContent).toContain("web-2 gets no answer through the tunnel.");
  });

  it("repairs a tunnel", async () => {
    api.on("POST /mesh/tunnels/*/repair", () => [202, { jobs: [meshJob({ id: "job-a" }), meshJob({ id: "job-b", title: "Set the tunnels of web-2" })] }]);
    page();
    select("Actions for the tunnel between web-1 and web-2", "Repair");
    await waitFor(() => expect(changes().map((c) => `${c.method} ${c.path}`)).toEqual([`POST /mesh/tunnels/${mesh().tunnels[0].id}/repair`]));
    // The page shows the two jobs.
    await waitFor(() => expect(screen.getByText("Set the tunnels of web-2")).toBeTruthy());
  });

  it("keeps a tunnel that a website uses", () => {
    // In the demo data, a website on one server uses a database server on the other.
    expect(mesh().tunnels[0].inUse).toBe(true);
    mesh().servers[1].networks = false;
    page();
    expect(row(/web-1 and web-2/).textContent).toContain("A website uses a database server on the other server.");
    fireEvent.click(screen.getByRole("button", { name: "Actions for the tunnel between web-1 and web-2" }));
    expect(screen.getByRole("menuitem", { name: "Remove" }).getAttribute("aria-disabled") ?? String((screen.getByRole("menuitem", { name: "Remove" }) as HTMLButtonElement).disabled)).toBe("true");
    expect((screen.getByRole("menuitem", { name: "Repair" }) as HTMLButtonElement).disabled).toBe(false);
    // The agent of a server from before the networks: the page tells the administrator.
    expect(screen.getAllByRole("status").map((e) => e.textContent).join(" ")).toContain(`${mesh().servers[1].name} cannot use the network names of database servers.`);
  });

  it("names a tunnel that the panel made for a network", () => {
    Object.assign(mesh().tunnels[0], { pinned: false, inUse: true });
    page();
    expect(row(/web-1 and web-2/).textContent).toContain("The panel made it: a website uses a database server on the other server.");
  });

  it("removes a tunnel after a question", async () => {
    Object.assign(mesh().tunnels[0], { inUse: false });
    const id = mesh().tunnels[0].id;
    api.on("DELETE /mesh/tunnels/*", () => {
      inApi((m) => { m.tunnels = []; });
      return [202, { jobs: [meshJob()] }];
    });
    page();
    select("Actions for the tunnel between web-1 and web-2", "Remove");
    expect(changes()).toEqual([]);
    fireEvent.click(within(screen.getByRole("dialog")).getByRole("button", { name: "Remove tunnel" }));
    await waitFor(() => expect(mesh().tunnels).toEqual([]));
    expect(changes().map((c) => `${c.method} ${c.path}`)).toEqual([`DELETE /mesh/tunnels/${id}`]);
    expect(screen.getByText(/No tunnels yet/)).toBeTruthy();
    // With no tunnel, the administrator can change the range and the port.
    expect((screen.getByLabelText("Address range") as HTMLInputElement).disabled).toBe(false);
  });

  it("makes a tunnel only between two servers that have none", async () => {
    // web-3 gets a current agent.
    Object.assign(server("web-3"), { supported: true, publicKey: "c2VydmVyLXRocmVlLXB1YmxpYy1rZXktMDAwMDAwMDA=" });
    api.on("POST /mesh/tunnels", () => [201, { tunnel: {}, jobs: [meshJob()] }]);
    page();
    fireEvent.click(screen.getByRole("button", { name: "Make tunnel" }));
    const dialog = within(screen.getByRole("dialog"));
    const make = dialog.getByRole("button", { name: "Make tunnel" }) as HTMLButtonElement;
    // The first two servers have a tunnel already.
    expect(make.disabled).toBe(true);
    expect(dialog.getByText("These two servers have a tunnel already.")).toBeTruthy();
    fireEvent.change(dialog.getByLabelText("Second server"), { target: { value: server("web-3").id } });
    expect(make.disabled).toBe(false);
    fireEvent.click(make);
    await waitFor(() => expect(changes().map((c) => c.body)).toEqual([{ servers: [server("web-1").id, server("web-3").id] }]));
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
  });

  it("keeps the dialog open when the API refuses a tunnel address", async () => {
    api.on("PATCH /mesh/servers/*", () => [422, { message: "The tunnel address must be a public IP address.", errors: { tunnelAddress: ["The tunnel address must be a public IP address."] } }]);
    page();
    select("Actions for web-2", "Set tunnel address");
    const dialog = within(screen.getByRole("dialog"));
    fireEvent.change(dialog.getByLabelText(/Public IP address/), { target: { value: "10.0.0.5" } });
    fireEvent.click(dialog.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(dialog.getByText("The tunnel address must be a public IP address.")).toBeTruthy());
    expect(changes().map((c) => c.body)).toEqual([{ tunnelAddress: "10.0.0.5" }]);

    // An empty field gives the choice of the address back to the panel.
    api.on("PATCH /mesh/servers/*", () => {
      inApi((m) => { m.servers.find((s) => s.name === "web-2")!.tunnelAddress = null; });
      return { ...structuredClone(mesh()), servers: mesh().servers.map((s) => (s.name === "web-2" ? { ...s, tunnelAddress: null } : s)), jobs: [] };
    });
    fireEvent.change(dialog.getByLabelText(/Public IP address/), { target: { value: " " } });
    fireEvent.click(dialog.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    expect(changes().at(-1)?.body).toEqual({ tunnelAddress: null });
    expect(serverRow("web-2").textContent).toContain("Automatic");
  });

  it("makes a new key after a question that tells the effect", async () => {
    api.on("POST /mesh/servers/*/rotate-key", () => [202, { job: meshJob({ action: "RotateMeshKey", title: "Make a new tunnel key on web-1" }) }]);
    page();
    select("Actions for web-1", "Make new key");
    const dialog = within(screen.getByRole("dialog"));
    expect(dialog.getByText(/The tunnel of this server will stop for a short time/)).toBeTruthy();
    fireEvent.click(dialog.getByRole("button", { name: "Make new key" }));
    await waitFor(() => expect(changes().map((c) => `${c.method} ${c.path}`)).toEqual([`POST /mesh/servers/${server("web-1").id}/rotate-key`]));
    await waitFor(() => expect(screen.getByText("Make a new tunnel key on web-1")).toBeTruthy());
  });

  it("changes the range and the port while the mesh has no tunnel", async () => {
    mesh().tunnels = [];
    api.on("PATCH /mesh", (body) => ({ ...structuredClone(mesh()), ...(body as object) }));
    page();
    fireEvent.change(screen.getByLabelText("Address range"), { target: { value: "10.99.0.0/16" } });
    fireEvent.change(screen.getByLabelText("UDP port"), { target: { value: "51900" } });
    fireEvent.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(mesh().range).toBe("10.99.0.0/16"));
    expect(changes().map((c) => c.body)).toEqual([{ range: "10.99.0.0/16", port: 51900 }]);
  });
});
