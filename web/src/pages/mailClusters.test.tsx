// @vitest-environment jsdom
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { getState, refresh } from "../api/store";
import type { MailCluster } from "../api/types";
import { reset, signedIn, type PanelApi } from "../test/panelApi";
import { ServersPage } from "./ServersPage";

let api: PanelApi;
beforeEach(async () => { api = await signedIn("administrator"); });
afterEach(() => {
  cleanup();
  reset();
});

/** Three servers with the email role and no mail server, in the answers of the API. */
async function freeServers() {
  api.change<{ data: Record<string, unknown>[] }>("/servers", (body) => {
    const sample = body.data.find((s) => s.name === "web-2")!;
    for (const i of [1, 2, 3]) body.data.push({ ...sample, id: `srv-eu-${i}`, name: `eu-${i}`, roles: ["email"], mail: null, panel: false, agentAddress: `198.51.100.${i}:7443` });
  });
  await refresh();
}

const cluster = (over: Partial<MailCluster> = {}): MailCluster => ({
  id: "mc-1", name: "eu", hostname: "mail.example.net", status: "setting_up", phase: "join", error: null, domains: 0, createdAt: null,
  members: [1, 2, 3].map((i) => ({ serverId: `srv-eu-${i}`, serverName: `eu-${i}`, serverState: "healthy", number: i, hostname: `m${i}.example.net`, endpoint: `198.51.100.${i}`, address: `10.77.16.${i}`, region: i - 1, step: i === 1 ? "joined" : "prepared" })),
  records: [{ type: "A", name: "mail.example.net", value: "198.51.100.1" }, { type: "A", name: "m1.example.net", value: "198.51.100.1" }],
  ...over,
});

const page = (path = "/servers") => render(
  <MemoryRouter initialEntries={[path]}>
    <Routes><Route path="/servers" element={<ServersPage />} /><Route path="/servers/:id" element={<ServersPage />} /></Routes>
  </MemoryRouter>,
);

describe("mail clusters", () => {
  it("needs three free servers for a new cluster", () => {
    page();
    const card = screen.getByRole("heading", { name: "Mail clusters" }).closest("section")!;
    // The sample data has one server with the email role, and it has its own mail server.
    expect((within(card).getByRole("button", { name: "New mail cluster" }) as HTMLButtonElement).disabled).toBe(true);
    expect(card.textContent).toContain("No server can be a member now.");
  });

  it("makes a cluster from the servers in the order of selection", async () => {
    await freeServers();
    api.on("POST /mail-clusters", () => [202, cluster({ phase: "prepare" })]);
    page();
    fireEvent.click(screen.getByRole("button", { name: "New mail cluster" }));
    const dialog = screen.getByRole("dialog");
    const submit = within(dialog).getByRole("button", { name: "Make the cluster" }) as HTMLButtonElement;
    fireEvent.change(within(dialog).getByLabelText("Name"), { target: { value: "eu" } });
    fireEvent.change(within(dialog).getByLabelText("Shared name"), { target: { value: "Mail.Example.net" } });
    for (const name of ["eu-2", "eu-1"]) fireEvent.click(within(dialog).getByRole("checkbox", { name: new RegExp(`^${name}`) }));
    // Two servers are not a cluster.
    expect(submit.disabled).toBe(true);
    fireEvent.click(within(dialog).getByRole("checkbox", { name: /^eu-3/ }));
    expect(submit.disabled).toBe(false);
    // The address of the agent fills the public address.
    const hosts = within(dialog).getAllByLabelText("Host name");
    expect((within(dialog).getAllByLabelText("Public address")[0] as HTMLInputElement).value).toBe("198.51.100.1");
    hosts.forEach((h, i) => fireEvent.change(h, { target: { value: `m${i + 1}.example.net` } }));
    fireEvent.click(submit);
    await waitFor(() => expect(api.changes.map((c) => `${c.method} ${c.path}`)).toContain("POST /mail-clusters"));
    expect(api.changes.at(-1)!.body).toEqual({
      name: "eu", hostname: "mail.example.net",
      members: [
        { serverId: "srv-eu-2", hostname: "m2.example.net", endpoint: "198.51.100.2", region: 0 },
        { serverId: "srv-eu-1", hostname: "m1.example.net", endpoint: "198.51.100.1", region: 0 },
        { serverId: "srv-eu-3", hostname: "m3.example.net", endpoint: "198.51.100.3", region: 0 },
      ],
    });
  });

  it("shows the errors of each member next to the member", async () => {
    await freeServers();
    api.on("POST /mail-clusters", () => [422, { message: "This name is in use.", errors: { "members.1.hostname": ["This name is in use."] } }]);
    page("/servers?newCluster=1");
    const dialog = screen.getByRole("dialog");
    fireEvent.change(within(dialog).getByLabelText("Name"), { target: { value: "eu" } });
    fireEvent.change(within(dialog).getByLabelText("Shared name"), { target: { value: "mail.example.net" } });
    for (const name of ["eu-1", "eu-2", "eu-3"]) fireEvent.click(within(dialog).getByRole("checkbox", { name: new RegExp(`^${name}`) }));
    fireEvent.click(within(dialog).getByRole("button", { name: "Make the cluster" }));
    await waitFor(() => expect(within(dialog).getAllByLabelText("Host name")[1].getAttribute("aria-invalid")).toBe("true"));
    expect(within(dialog).queryByRole("alert")).toBeNull();
  });

  it("shows the steps of the setup and starts a failed step again", async () => {
    await freeServers();
    api.change<{ data: MailCluster[] }>("/mail-clusters", (body) => { body.data.push(cluster({ status: "failed", error: "eu-2: the agent stopped" })); });
    await refresh();
    api.on("POST /mail-clusters/mc-1/retry", () => [202, cluster()]);
    page();
    fireEvent.click(screen.getByRole("button", { name: "Details of eu" }));
    const dialog = screen.getByRole("dialog");
    const steps = within(dialog).getByRole("list", { name: "Steps of the setup" });
    expect(within(steps).getAllByRole("listitem").map((li) => li.textContent)).toEqual([
      "Each server makes its keysDone", "The first server starts the databaseDone", "The other servers join the databaseFailed",
      "The first mail server startsNot started", "The other mail servers startNot started",
    ]);
    expect(dialog.textContent).toContain("eu-2: the agent stopped");
    expect(dialog.textContent).toContain("10.77.16.2");
    fireEvent.click(within(dialog).getByRole("button", { name: "Start the step again" }));
    await waitFor(() => expect(api.changes.map((c) => `${c.method} ${c.path}`)).toContain("POST /mail-clusters/mc-1/retry"));
  });

  it("says on the server page that the cluster controls the mail server", async () => {
    api.change<{ data: Record<string, unknown>[] }>("/servers", (body) => {
      const s = body.data.find((x) => x.name === "mail-1")!;
      s.mail = { hostname: "m1.example.net", status: "running", domains: 0, clusterId: "mc-1" };
    });
    api.change<{ data: MailCluster[] }>("/mail-clusters", (body) => { body.data.push(cluster({ status: "running", phase: "done" })); });
    await refresh();
    const id = getState().servers.find((s) => s.name === "mail-1")!.id;
    page(`/servers/${id}`);
    expect(screen.getByText(/is a member of the mail cluster/).textContent).toContain("eu");
    expect(screen.queryByLabelText("Host name of the mail server")).toBeNull();
  });
});
