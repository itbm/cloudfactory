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
const clone = <T,>(value: T): T => JSON.parse(JSON.stringify(value)) as T;
const accounts = () => screen.getByRole("heading", { name: "External DNS providers" }).closest("section")!;

describe("external DNS providers", () => {
  it("shows an owner the accounts that the customer can use, with no credentials of the panel", async () => {
    await open("owner", "/dns");
    const card = accounts();
    const own = within(card).getByText("Acme Cloudflare").closest("tr")!;
    expect(own.textContent).toContain("it@acme.example.com");
    expect(own.textContent).toContain("Acme Ltd");
    const panel = within(card).getByText("Panel Cloudflare").closest("tr")!;
    expect(panel.textContent).toContain("Panel: each customer");
    // A customer cannot change or remove an account of the panel.
    expect(within(panel).queryByRole("button", { name: /Actions for the DNS account/ })).toBeNull();
    expect(within(own).getByRole("button", { name: "Actions for the DNS account Acme Cloudflare" })).toBeTruthy();
  });

  it("adds an account after the plugin checks the credentials", async () => {
    const api = await open("owner", "/dns");
    api.on("POST /dns-accounts", () => [422, { message: "x", errors: { credentials: ["Cloudflare does not accept the API token."] } }]);
    fireEvent.click(within(accounts()).getByRole("button", { name: "Add account" }));
    const dialog = screen.getByRole("dialog");
    expect(within(dialog).getByLabelText("API token").getAttribute("type")).toBe("password");
    expect(dialog.textContent).toContain("Zone: Read and DNS: Edit");
    fireEvent.change(within(dialog).getByLabelText("Name"), { target: { value: " Shop Cloudflare " } });
    fireEvent.change(within(dialog).getByLabelText("API token"), { target: { value: "bad" } });
    fireEvent.click(within(dialog).getByRole("button", { name: "Add account" }));
    await waitFor(() => expect(screen.getByRole("dialog").textContent).toContain("Cloudflare does not accept the API token."));

    api.on("POST /dns-accounts", () => [201, {}]);
    fireEvent.change(within(dialog).getByLabelText("API token"), { target: { value: "good" } });
    fireEvent.click(within(dialog).getByRole("button", { name: "Add account" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    expect(api.changes.at(-1)).toMatchObject({
      method: "POST", path: "/dns-accounts",
      body: { name: "Shop Cloudflare", provider: "cloudflare", credentials: { apiToken: "good", accountId: "" }, customerId: zone("acme.example.com").customerId },
    });
  });

  it("lets an administrator add an account of the panel", async () => {
    const api = await open("administrator", "/dns");
    api.on("POST /dns-accounts", () => [201, {}]);
    fireEvent.click(within(accounts()).getByRole("button", { name: "Add account" }));
    const dialog = screen.getByRole("dialog");
    expect((within(dialog).getByLabelText("Owner") as HTMLSelectElement).value).toBe("");
    fireEvent.change(within(dialog).getByLabelText("Name"), { target: { value: "Second Cloudflare" } });
    fireEvent.change(within(dialog).getByLabelText("API token"), { target: { value: "token" } });
    fireEvent.click(within(dialog).getByRole("button", { name: "Add account" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    expect(api.changes.at(-1)).toMatchObject({ method: "POST", path: "/dns-accounts", body: { customerId: null } });
  });

  it("does not remove an account that zones use", async () => {
    await open("owner", "/dns");
    fireEvent.click(within(accounts()).getByRole("button", { name: "Actions for the DNS account Acme Cloudflare" }));
    fireEvent.click(screen.getByRole("menuitem", { name: "Remove" }));
    const dialog = screen.getByRole("dialog");
    expect(dialog.textContent).toContain("1 zone uses this account.");
    expect((within(dialog).getByRole("button", { name: "Remove account" }) as HTMLButtonElement).disabled).toBe(true);
  });

  it("makes a zone at Cloudflare with the records that Cloudflare has", async () => {
    const api = await open("owner", "/dns");
    const made = clone(zone("acmeshop.example.com"));
    api.on("POST /dns-zones", () => [202, { zone: { ...made, id: "new-zone", name: "acme-new.example.com" }, jobs: [] }]);
    api.on("GET /dns-zones/new-zone", () => ({ ...made, id: "new-zone", name: "acme-new.example.com" }));
    fireEvent.click(screen.getByRole("button", { name: "New DNS zone" }));
    const dialog = screen.getByRole("dialog");
    fireEvent.change(within(dialog).getByLabelText("Domain"), { target: { value: "acme-new.example.com" } });
    fireEvent.change(within(dialog).getByLabelText("Name servers"), { target: { value: made.provider!.accountId } });
    expect(dialog.textContent).toContain("The zone goes to Cloudflare.");
    expect((within(dialog).getByLabelText("Copy the records that Cloudflare has for the domain now") as HTMLInputElement).checked).toBe(true);
    fireEvent.click(within(dialog).getByRole("button", { name: "Create zone" }));
    await waitFor(() => expect(screen.getByRole("heading", { level: 1 }).textContent).toBe("acme-new.example.com"));
    expect(api.changes.at(-1)).toMatchObject({ method: "POST", path: "/dns-zones", body: { name: "acme-new.example.com", accountId: made.provider!.accountId, importRecords: true } });
  });

  it("shows the state at Cloudflare and the name servers for the registrar", async () => {
    await open("owner", () => `/dns/${zone("acmeshop.example.com").id}`);
    await waitFor(() => expect(screen.getByRole("heading", { level: 1 }).textContent).toBe("acmeshop.example.com"));
    const main = screen.getByRole("main");
    expect(main.textContent).toContain("at Cloudflare");
    const card = screen.getByRole("heading", { name: "Name servers" }).closest("section")!;
    expect(card.textContent).toContain("Account: Acme Cloudflare");
    expect(card.textContent).toContain("Has the last change");
    expect(card.textContent).toContain("Uses Cloudflare");
    expect(within(card).getByLabelText("Name server 1 of Cloudflare")).toBeTruthy();
    expect(card.textContent).toContain("4 records from Cloudflare");
    expect(card.textContent).toContain("1 not copied");
    expect(card.textContent).not.toContain("The panel has no DNS server.");
    const dnssec = screen.getByRole("heading", { name: "DNSSEC" }).closest("section")!;
    expect(dnssec.textContent).toContain("With DNSSEC, Cloudflare signs each answer of the zone");
  });

  it("tells the user when Cloudflare does not have the last change, and sends it again", async () => {
    const api = await open("owner", "/dns");
    const id = zone("acmeshop.example.com").id;
    api.change<{ status: string; provider: { state: string; error: string } }>(`/dns-zones/${id}`, (z) => {
      z.status = "failed";
      z.provider.state = "failed";
      z.provider.error = "Cloudflare does not accept the API token.";
    });
    api.on(`POST /dns-zones/${id}/sync`, () => [202, { jobs: [] }]);
    cleanup();
    render(<MemoryRouter initialEntries={[`/dns/${id}`]}><App /></MemoryRouter>);
    const alert = await screen.findByRole("alert");
    expect(alert.textContent).toContain("Cloudflare does not have the last change.");
    expect(alert.textContent).toContain("Cloudflare does not accept the API token.");
    fireEvent.click(within(alert).getByRole("button", { name: "Send again now" }));
    await waitFor(() => expect(api.changes.at(-1)).toMatchObject({ method: "POST", path: `/dns-zones/${id}/sync` }));
  });

  it("waits for the TXT record of the panel at Cloudflare before the zone counts as in use", async () => {
    const api = await signedIn("owner");
    const id = zone("acmeshop.example.com").id;
    api.change<{ delegation: { state: string; nameServers: string[] } }>(`/dns-zones/${id}`, (z) => {
      z.delegation.state = "unknown";
      z.delegation.nameServers = ["ada.ns.cloudflare.com", "bob.ns.cloudflare.com"];
    });
    render(<MemoryRouter initialEntries={[`/dns/${id}`]}><App /></MemoryRouter>);
    const status = await screen.findByText(/does not answer with this zone yet/);
    const alert = status.closest("[role=status]")!;
    expect(alert.textContent).toContain("The registrar has the name servers of Cloudflare, but Cloudflare does not answer with this zone yet.");
    expect(alert.textContent).toContain("_cloudfactory-challenge.acmeshop.example.com");
    expect(alert.textContent).not.toContain("have no effect until");
  });

  it("moves a zone of the panel to Cloudflare", async () => {
    const api = await open("owner", () => `/dns/${zone("acme-outlet.example.com").id}`);
    const before = clone(zone("acme-outlet.example.com"));
    const account = getState().dnsAccounts.find((a) => a.name === "Acme Cloudflare")!;
    const moved = { ...before, provider: { ...clone(zone("acmeshop.example.com").provider!), handover: { endsAt: null } } };
    api.on(`POST /dns-zones/${before.id}/move`, () => [202, { zone: moved, jobs: [] }]);
    api.on(`GET /dns-zones/${before.id}`, () => moved);
    fireEvent.click(await screen.findByRole("button", { name: "Move" }));
    const dialog = screen.getByRole("dialog");
    expect((within(dialog).getByLabelText("Move to") as HTMLSelectElement).value).toBe(account.id);
    expect(dialog.textContent).toContain("The name servers of this panel continue to answer for the zone");
    fireEvent.click(within(dialog).getByRole("button", { name: "Move zone" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    expect(api.changes.at(-1)).toMatchObject({ method: "POST", path: `/dns-zones/${before.id}/move`, body: { accountId: account.id, importRecords: true } });
    expect(getState().dnsAccounts.find((a) => a.id === account.id)!.zones).toBe(account.zones + 1);
    expect(screen.getByRole("main").textContent).toContain("They stop two days after the registrar has the new name servers.");
  });

  it("does not move a signed zone", async () => {
    await open("owner", () => `/dns/${zone("acme.example.com").id}`);
    fireEvent.click(await screen.findByRole("button", { name: "Move" }));
    const dialog = screen.getByRole("dialog");
    expect(dialog.textContent).toContain("Turn off DNSSEC first.");
    expect((within(dialog).getByRole("button", { name: "Move zone" }) as HTMLButtonElement).disabled).toBe(true);
  });

  it("removes a zone from the panel and keeps it at Cloudflare", async () => {
    const api = await open("owner", () => `/dns/${zone("acmeshop.example.com").id}`);
    const id = zone("acmeshop.example.com").id;
    api.on(`DELETE /dns-zones/${id}`, () => [202, { jobs: [] }]);
    fireEvent.click(await screen.findByRole("button", { name: "Remove zone" }));
    const dialog = screen.getByRole("dialog");
    expect(dialog.textContent).toContain("The panel removes the zone and its");
    fireEvent.click(within(dialog).getByLabelText("Keep the zone at Cloudflare"));
    expect(dialog.textContent).toContain("The zone stays at Cloudflare with its records");
    fireEvent.click(within(dialog).getByRole("button", { name: "Remove zone" }));
    await waitFor(() => expect(api.changes.at(-1)).toMatchObject({ method: "DELETE", path: `/dns-zones/${id}?keepAtProvider=1` }));
  });

  it("shows a reseller the handover of a zone that moves to Cloudflare", async () => {
    await open("reseller", () => `/dns/${zone("northwind.example.org").id}`);
    await waitFor(() => expect(screen.getByRole("heading", { level: 1 }).textContent).toBe("northwind.example.org"));
    const card = screen.getByRole("heading", { name: "Name servers" }).closest("section")!;
    expect(card.textContent).toContain("Handover");
    expect(card.textContent).toContain("The name servers of this panel still answer for the zone");
  });
});
