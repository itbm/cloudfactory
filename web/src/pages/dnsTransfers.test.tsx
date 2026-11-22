import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { afterEach, describe, expect, it } from "vitest";
import { App } from "../App";
import { getState } from "../api/store";
import type { Role } from "../api/types";
import { reset, signedIn, type PanelApi } from "../test/panelApi";

afterEach(() => {
  cleanup();
  reset();
});

async function open(role: Role, path: string | (() => string)): Promise<PanelApi> {
  const api = await signedIn(role);
  render(<MemoryRouter initialEntries={[typeof path === "string" ? path : path()]}><App /></MemoryRouter>);
  return api;
}
const zone = (name: string) => getState().dnsZones.find((z) => z.name === name)!;
const card = (title: string) => screen.getByRole("heading", { name: title }).closest("section")!;
const secret = "c2VjcmV0c2VjcmV0c2VjcmV0c2VjcmV0c2VjcmV0MTI=";

describe("secondary DNS servers of the panel", () => {
  it("shows an administrator the secondaries and the servers to copy from", async () => {
    await open("administrator", "/dns");
    const secondaries = card("Secondary DNS servers");
    const row = within(secondaries).getByText("Backup DNS service").closest("tr")!;
    expect(row.textContent).toContain("198.51.100.53");
    expect(row.textContent).toContain("ns3.backup-dns.example");
    expect(row.textContent).toContain("cf-backup-dns");
    expect(row.textContent).toContain("All zones and the catalog zone");
    expect(secondaries.textContent).toContain("ns1.panel.example (203.0.113.10, 2001:db8:10::1)");
    expect(secondaries.textContent).toContain("catalog.cloudfactory.invalid");
    expect(secondaries.textContent).toContain("Has the list");
    // The secret is not in the page until the administrator asks for it.
    expect(document.body.textContent).not.toContain(secret);
  });

  it("does not show the secondaries to an owner", async () => {
    await open("owner", "/dns");
    expect(screen.queryByRole("heading", { name: "Secondary DNS servers" })).toBeNull();
  });

  it("adds a secondary with a TSIG key that the panel makes", async () => {
    const api = await open("administrator", "/dns");
    api.on("POST /dns-secondaries", () => [422, { message: "x", errors: { addresses: ["10.0.0 is not an IP address."] } }]);
    fireEvent.click(within(card("Secondary DNS servers")).getByRole("button", { name: "Add secondary" }));
    const dialog = screen.getByRole("dialog");
    fireEvent.change(within(dialog).getByLabelText("Name"), { target: { value: " Office secondary " } });
    fireEvent.change(within(dialog).getByLabelText("Addresses"), { target: { value: "10.0.0" } });
    fireEvent.click(within(dialog).getByRole("button", { name: "Add secondary" }));
    await waitFor(() => expect(screen.getByRole("dialog").textContent).toContain("10.0.0 is not an IP address."));

    api.on("POST /dns-secondaries", () => [202, { jobs: [] }]);
    fireEvent.change(within(dialog).getByLabelText("Addresses"), { target: { value: "10.0.0.53\n2001:db8::53, 10.0.0.54" } });
    fireEvent.change(within(dialog).getByLabelText("Name server (optional)"), { target: { value: "NS4.Office.example." } });
    fireEvent.click(within(dialog).getByLabelText(/Also copy the catalog zone/));
    expect(dialog.textContent).toContain("each DNS server of the panel restarts");
    fireEvent.click(within(dialog).getByRole("button", { name: "Add secondary" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    expect(api.changes.at(-1)).toMatchObject({
      method: "POST", path: "/dns-secondaries",
      body: { name: "Office secondary", addresses: ["10.0.0.53", "2001:db8::53", "10.0.0.54"], nameServer: "ns4.office.example", tsig: true, algorithm: "hmac-sha256", keyName: null, secret: null, notify: true, catalog: true },
    });
  });

  it("shows the TSIG key only when the administrator asks for it", async () => {
    const api = await open("administrator", "/dns");
    const id = getState().dnsSecondaries!.data[0].id;
    api.on(`GET /dns-secondaries/${id}/secret`, () => ({ name: "cf-backup-dns", algorithm: "hmac-sha256", secret }));
    fireEvent.click(within(card("Secondary DNS servers")).getByRole("button", { name: "Actions for the secondary DNS server Backup DNS service" }));
    fireEvent.click(screen.getByRole("menuitem", { name: "Show the TSIG key" }));
    const dialog = screen.getByRole("dialog");
    expect(dialog.textContent).not.toContain(secret);
    fireEvent.click(within(dialog).getByRole("button", { name: "Show the secret" }));
    await waitFor(() => expect(within(dialog).getByLabelText("Secret").textContent).toBe(secret));
    expect(within(dialog).getByLabelText("Key for BIND").textContent).toBe(`key "cf-backup-dns" { algorithm hmac-sha256; secret "${secret}"; };`);
  });

  it("warns before it removes a secondary that is a name server", async () => {
    const api = await open("administrator", "/dns");
    const id = getState().dnsSecondaries!.data[0].id;
    api.on(`DELETE /dns-secondaries/${id}`, () => [202, { jobs: [] }]);
    fireEvent.click(within(card("Secondary DNS servers")).getByRole("button", { name: "Actions for the secondary DNS server Backup DNS service" }));
    fireEvent.click(screen.getByRole("menuitem", { name: "Remove" }));
    const dialog = screen.getByRole("dialog");
    expect(dialog.textContent).toContain("First remove ns3.backup-dns.example at the registrar");
    fireEvent.click(within(dialog).getByRole("button", { name: "Remove secondary" }));
    await waitFor(() => expect(api.changes.at(-1)).toMatchObject({ method: "DELETE", path: `/dns-secondaries/${id}` }));
  });
});

describe("zone transfers of one zone", () => {
  it("shows the owner the secondaries of the zone and the addresses to copy from", async () => {
    await open("owner", () => `/dns/${zone("acme.example.com").id}`);
    const transfers = await waitFor(() => card("Zone transfers"));
    expect(transfers.textContent).toContain("192.0.2.200");
    expect(transfers.textContent).toContain("transfer.acme.example.com");
    expect(transfers.textContent).toContain("ns.acme-backup.example");
    expect(transfers.textContent).toContain("203.0.113.10");
    expect(transfers.textContent).toContain("Get NOTIFY after each change");
  });

  it("sets up the transfers of a zone with no transfers", async () => {
    const api = await open("owner", () => `/dns/${zone("acme-outlet.example.com").id}`);
    const transfers = await waitFor(() => card("Zone transfers"));
    expect(transfers.textContent).toContain("Your own secondary DNS servers can copy this zone");
    api.on(`PUT /dns-zones/${zone("acme-outlet.example.com").id}/transfers`, () => [422, { message: "x", errors: { addresses: ["10.1.2.3 is not a public address. Enter the public addresses of your secondary DNS servers."] } }]);
    fireEvent.click(within(transfers).getByRole("button", { name: "Set up" }));
    const dialog = screen.getByRole("dialog");
    fireEvent.change(within(dialog).getByLabelText("Addresses of your secondary DNS servers"), { target: { value: "10.1.2.3" } });
    fireEvent.click(within(dialog).getByRole("button", { name: "Save" }));
    await waitFor(() => expect(screen.getByRole("dialog").textContent).toContain("10.1.2.3 is not a public address."));
    expect(api.changes.at(-1)).toMatchObject({
      method: "PUT", body: { addresses: ["10.1.2.3"], notify: true, nameServers: [], tsig: true, algorithm: "hmac-sha256", secret: null, newSecret: false },
    });
  });

  it("warns before it stops the transfers of a zone with name servers of its secondaries", async () => {
    const api = await open("owner", () => `/dns/${zone("acme.example.com").id}`);
    const id = zone("acme.example.com").id;
    const transfers = await waitFor(() => card("Zone transfers"));
    api.on(`DELETE /dns-zones/${id}/transfers`, () => [202, { zone: { ...structuredClone(getState().dnsZones.find((z) => z.id === id)), transfers: { addresses: [], notify: false, nameServers: [], tsig: null, primaries: [] }, delegation: { state: "here", nameServers: [], checkedAt: null }, dnssec: { enabled: false, ds: null, dnskey: null }, createdAt: null }, jobs: [] }]);
    fireEvent.click(within(transfers).getByRole("button", { name: "Stop the transfers" }));
    const dialog = screen.getByRole("dialog");
    expect(dialog.textContent).toContain("First remove ns.acme-backup.example at the registrar.");
    fireEvent.click(within(dialog).getByRole("button", { name: "Stop the transfers" }));
    await waitFor(() => expect(api.changes.at(-1)).toMatchObject({ method: "DELETE", path: `/dns-zones/${id}/transfers` }));
  });

  it("has no transfers for a zone at an external DNS provider", async () => {
    const provider = () => getState().dnsZones.find((z) => z.provider && !z.provider.handover)!;
    await open("owner", () => `/dns/${provider().id}`);
    await waitFor(() => expect(screen.getByRole("heading", { name: "Records" })).toBeTruthy());
    expect(screen.queryByRole("heading", { name: "Zone transfers" })).toBeNull();
  });
});
