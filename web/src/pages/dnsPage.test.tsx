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
const job = (id: string) => ({ id, title: "Send DNS zone", ref: null, action: "SetDnsZones", state: "succeeded", steps: [], step: 0, error: null, websiteId: null, serverId: null, createdAt: "2026-11-14T10:00:00+00:00", finishedAt: "2026-11-14T10:00:01+00:00" });

describe("the DNS page", () => {
  it("shows an owner only the zones of the customer, with the name servers for the registrar", async () => {
    await open("owner", "/dns");
    const page = screen.getByRole("main");
    const main = screen.getByRole("heading", { name: "DNS zones" }).closest("section")!;
    expect(screen.getByRole("heading", { level: 1 }).textContent).toBe("DNS");
    expect(within(main).getByRole("link", { name: "acme.example.com" })).toBeTruthy();
    expect(within(main).getByRole("link", { name: "acme-outlet.example.com" })).toBeTruthy();
    expect(main.textContent).not.toContain("northwind.example.com");
    expect(main.textContent).toContain("Acme Ltd has 2 of");
    expect(main.textContent).toContain("Uses other name servers");
    expect(main.textContent).toContain("ns1.registrar.example, ns2.registrar.example");
    expect(within(page).getByLabelText("Name server 1").textContent).toBe("ns1.panel.example");
    expect(within(page).getByLabelText("Name server 2").textContent).toBe("ns2.panel.example");
    // A domain of a website has a link to its zone.
    const domains = screen.getByRole("heading", { name: "Domains of websites" }).closest("section")!;
    expect(within(domains).getAllByRole("link", { name: "acme.example.com" }).length).toBeGreaterThan(0);
  });

  it("shows an administrator the zones of all customers and the failed zone", async () => {
    await open("administrator", "/dns");
    const main = screen.getByRole("heading", { name: "DNS zones" }).closest("section")!;
    for (const name of ["acme.example.com", "northwind.example.com", "cornerbakery.example.org", "harbourbooks.example.net"]) {
      expect(within(main).getByRole("link", { name })).toBeTruthy();
    }
    const row = within(main).getByRole("link", { name: "harbourbooks.example.net" }).closest("tr")!;
    expect(row.textContent).toContain("Failed");
    expect(row.textContent).toContain("Harbour Books");
  });

  it("tells an administrator what to do when the panel has no DNS server", async () => {
    const api = await signedIn("administrator");
    api.change<{ data: unknown[]; nameServers: string[] }>("/dns-zones", (b) => { b.data = []; b.nameServers = []; });
    const { refresh } = await import("../api/store");
    await refresh();
    render(<MemoryRouter initialEntries={["/dns"]}><App /></MemoryRouter>);
    expect(screen.getByRole("main").textContent).toContain("This panel has no DNS server.");
    expect(screen.queryByRole("button", { name: "New DNS zone" })).toBeNull();
  });

  it("makes a zone and opens it", async () => {
    const api = await open("owner", "/dns");
    const id = zone("acme-outlet.example.com").id;
    api.on("POST /dns-zones", () => [202, { zone: { ...(JSON.parse(JSON.stringify(zone("acme-outlet.example.com"))) as object), delegation: { state: "unknown", nameServers: [], checkedAt: null }, dnssec: { enabled: false, ds: null, dnskey: null } }, jobs: [] }]);
    fireEvent.click(screen.getByRole("button", { name: "New DNS zone" }));
    fireEvent.change(screen.getByLabelText("Domain"), { target: { value: " Acme-Outlet.example.com. " } });
    fireEvent.click(screen.getByRole("button", { name: "Create zone" }));
    await waitFor(() => expect(screen.getByRole("heading", { level: 1 }).textContent).toBe("acme-outlet.example.com"));
    expect(api.changes.at(-1)).toMatchObject({ method: "POST", path: "/dns-zones", body: { name: "acme-outlet.example.com" } });
    expect(id).toBeTruthy();
  });

  it("shows the error of the API for a domain of a different customer", async () => {
    const api = await open("owner", "/dns");
    api.on("POST /dns-zones", () => [422, { message: "x", errors: { name: ["A different customer has this zone. If you control the domain, add a TXT record _cloudfactory-challenge.taken.example with the value abc at your current DNS provider, and then try again."] } }]);
    fireEvent.click(screen.getByRole("button", { name: "New DNS zone" }));
    fireEvent.change(screen.getByLabelText("Domain"), { target: { value: "taken.example" } });
    fireEvent.click(screen.getByRole("button", { name: "Create zone" }));
    await waitFor(() => expect(screen.getByRole("dialog").textContent).toContain("_cloudfactory-challenge.taken.example"));
  });
});

describe("the page of a DNS zone", () => {
  it("shows the records, the DS record and the state on each name server", async () => {
    await open("owner", () => `/dns/${zone("acme.example.com").id}`);
    const main = screen.getByRole("main");
    await waitFor(() => expect(main.textContent).toContain("mx1.mail.example"));
    expect(screen.getByRole("heading", { level: 1 }).textContent).toBe("acme.example.com");
    expect(within(main).getByLabelText("DS record").textContent).toMatch(/^acme\.example\.com\. IN DS \d+ 13 2 [0-9A-F]{64}$/);
    expect(main.textContent).toContain("Uses these name servers");
    expect(main.textContent).toContain("Has the last change");
    expect(main.textContent).toContain("Priority 10");
    expect(main.textContent).toContain("Panel: website");
    // The panel keeps the NS records: they have no menu.
    const ns = within(main).getAllByText("Panel: name server")[0].closest("tr")!;
    expect(within(ns).queryByRole("button")).toBeNull();
    // The filter shows one type.
    fireEvent.click(within(main).getByRole("button", { name: "MX" }));
    expect(main.textContent).not.toContain("Panel: name server");
    expect(main.textContent).toContain("mx2.mail.example");
  });

  it("tells the owner to change the name servers when the domain uses other name servers", async () => {
    const api = await open("owner", () => `/dns/${zone("acme-outlet.example.com").id}`);
    const main = screen.getByRole("main");
    expect(main.textContent).toContain("The domain uses other name servers.");
    expect(main.textContent).toContain("ns1.registrar.example, ns2.registrar.example");
    const id = zone("acme-outlet.example.com").id;
    api.on(`POST /dns-zones/${id}/check`, () => ({ ...(JSON.parse(JSON.stringify(zone("acme-outlet.example.com"))) as object), delegation: { state: "here", nameServers: ["ns1.panel.example", "ns2.panel.example"], checkedAt: "2026-11-14T10:00:00+00:00" }, dnssec: { enabled: false, ds: null, dnskey: null } }));
    fireEvent.click(within(main).getByRole("button", { name: "Check again" }));
    await waitFor(() => expect(main.textContent).not.toContain("The domain uses other name servers."));
    expect(main.textContent).toContain("Uses these name servers");
  });

  it("adds an MX record with a priority", async () => {
    const api = await open("owner", () => `/dns/${zone("acme.example.com").id}`);
    const id = zone("acme.example.com").id;
    await waitFor(() => expect(screen.getByRole("main").textContent).toContain("mx1.mail.example"));
    api.on(`POST /dns-zones/${id}/records`, (body) => {
      const record = { id: "new", ...(body as object), managed: null };
      // The panel reads the zone again when the job has ended.
      api.change<{ records: unknown[] }>(`/dns-zones/${id}`, (z) => { z.records.push(record); });
      return [202, { record, jobs: [job("j1")] }];
    });
    api.on("GET /jobs/j1", () => ({ data: job("j1") }));
    fireEvent.click(screen.getByRole("button", { name: "Add record" }));
    const dialog = screen.getByRole("dialog");
    fireEvent.change(within(dialog).getByLabelText("Type"), { target: { value: "MX" } });
    fireEvent.change(within(dialog).getByLabelText("Name"), { target: { value: "news" } });
    expect(dialog.textContent).toContain("news.acme.example.com");
    fireEvent.change(within(dialog).getByLabelText("Mail server"), { target: { value: "mx3.mail.example" } });
    fireEvent.change(within(dialog).getByLabelText("Priority"), { target: { value: "30" } });
    fireEvent.click(within(dialog).getByRole("button", { name: "Add record" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    expect(api.changes.at(-1)).toMatchObject({ method: "POST", path: `/dns-zones/${id}/records`, body: { type: "MX", name: "news", value: "mx3.mail.example", ttl: 3600, priority: 30 } });
    expect(screen.getByRole("main").textContent).toContain("mx3.mail.example");
  });

  it("shows the error of a field of a record", async () => {
    const api = await open("owner", () => `/dns/${zone("acme.example.com").id}`);
    const id = zone("acme.example.com").id;
    api.on(`POST /dns-zones/${id}/records`, () => [422, { message: "x", errors: { value: ["Type an IPv4 address, for example 203.0.113.10."] } }]);
    fireEvent.click(screen.getByRole("button", { name: "Add record" }));
    const dialog = screen.getByRole("dialog");
    fireEvent.change(within(dialog).getByLabelText("IPv4 address"), { target: { value: "nope" } });
    fireEvent.click(within(dialog).getByRole("button", { name: "Add record" }));
    await waitFor(() => expect(dialog.textContent).toContain("Type an IPv4 address"));
    expect((api.changes.at(-1)!.body as { priority?: number }).priority).toBeUndefined();
  });

  it("warns before it turns off DNSSEC", async () => {
    const api = await open("owner", () => `/dns/${zone("acme.example.com").id}`);
    const id = zone("acme.example.com").id;
    api.on(`PUT /dns-zones/${id}/dnssec`, () => [202, { zone: { ...(JSON.parse(JSON.stringify(zone("acme.example.com"))) as object), delegation: { state: "here", nameServers: [], checkedAt: null }, dnssec: { enabled: false, ds: null, dnskey: null }, records: [] }, jobs: [] }]);
    fireEvent.click(screen.getByRole("button", { name: "Turn off" }));
    const dialog = screen.getByRole("dialog");
    expect(dialog.textContent).toContain("First remove the DS record at the registrar");
    fireEvent.click(within(dialog).getByRole("button", { name: "Turn off DNSSEC" }));
    await waitFor(() => expect(screen.getByRole("button", { name: "Turn on" })).toBeTruthy());
    expect(api.changes.at(-1)).toMatchObject({ method: "PUT", body: { enabled: false } });
  });

  it("shows the cause and sends a failed zone again", async () => {
    const api = await open("administrator", () => `/dns/${zone("harbourbooks.example.net").id}`);
    const id = zone("harbourbooks.example.net").id;
    const main = screen.getByRole("main");
    expect(main.textContent).toContain("A DNS server does not have the last change.");
    expect(main.textContent).toContain("Could not load the last change");
    api.on(`POST /dns-zones/${id}/sync`, () => [202, { zone: { ...(JSON.parse(JSON.stringify(zone("harbourbooks.example.net"))) as object), status: "updating", delegation: { state: "here", nameServers: [], checkedAt: null }, dnssec: { enabled: false, ds: null, dnskey: null } }, jobs: [] }]);
    fireEvent.click(within(main).getByRole("button", { name: "Send again" }));
    await waitFor(() => expect(main.textContent).not.toContain("A DNS server does not have the last change."));
    expect(api.changes.at(-1)).toMatchObject({ method: "POST", path: `/dns-zones/${id}/sync` });
  });

  it("does not show an owner the zone of a different customer", async () => {
    const admin = await signedIn("administrator");
    void admin;
    const other = zone("northwind.example.com").id;
    reset();
    await open("owner", `/dns/${other}`);
    expect(screen.getByRole("main").textContent).toContain("This DNS zone does not exist");
  });
});
