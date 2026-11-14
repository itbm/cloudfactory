// @vitest-environment jsdom
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, describe, expect, it } from "vitest";
import { getState } from "../api/store";
import type { Role } from "../api/types";
import { apiJob, reset, signedIn, type PanelApi } from "../test/panelApi";
import { EmailPage } from "./EmailPage";
import { ServersPage } from "./ServersPage";

afterEach(() => {
  cleanup();
  reset();
});

// The owner of the sample data is a user of Acme Ltd. Acme has the mail domain acme.example.com.
const open = async (role: Role): Promise<PanelApi> => {
  const api = await signedIn(role);
  render(<MemoryRouter><EmailPage /></MemoryRouter>);
  return api;
};
const row = (name: string) => screen.getAllByRole("row").find((r) => r.querySelector("strong")?.textContent === name)!;
const menu = (r: HTMLElement, item: string) => {
  fireEvent.click(within(r).getByRole("button", { name: /^Actions for/ }));
  fireEvent.click(within(r).getByRole("menuitem", { name: item }));
};
const domain = (name: string) => getState().mailDomains.find((d) => d.name === name)!;
type List = { data: Record<string, unknown>[] };
const apiDomain = (name: string, over: Record<string, unknown> = {}) => ({ ...domain(name), dnsCheckedAt: domain(name).dnsCheckedAt ?? null, ...over });

describe("the email page", () => {
  it("shows the mail domains, the mailboxes and the addresses of the customer", async () => {
    await open("owner");
    const acme = row("acme.example.com");
    expect(acme.textContent).toContain("Active");
    expect(acme.textContent).toContain("Ready");
    const info = row("info@acme.example.com");
    expect(info.textContent).toContain("1.2 GB of 5 GB");
    expect(within(info).getByRole("meter", { name: "Storage: 1.2 GB of 5 GB" })).toBeTruthy();
    expect(row("hello@acme.example.com").textContent).toContain("Alias");
    expect(row("team@acme.example.com").textContent).toContain("morgan@partner.example.net");
    expect(screen.getByText(/Acme Ltd has \d+ of 100 mailboxes/)).toBeTruthy();
    // An owner does not see the hint for the administrator.
    expect(screen.queryByText(/No server has email turned on/)).toBeNull();
  });

  it("shows the DNS records of a domain and the settings for a mail program", async () => {
    const api = await open("administrator");
    // The sample data has a domain with no DKIM record in the DNS.
    const bakery = row("cornerbakery.example.org");
    expect(bakery.textContent).toMatch(/\d of \d records found/);
    menu(bakery, "Show DNS records");
    const dialog = screen.getByRole("dialog");
    expect(dialog.textContent).toContain("v=spf1 mx -all");
    expect(dialog.textContent).toContain("Missing");
    expect(dialog.textContent).toContain("IMAP, server mail-1.panel.example, port 993");
    api.on("POST /mail/domains/*/check", () => apiDomain("cornerbakery.example.org", { dnsReady: true, records: domain("cornerbakery.example.org").records.map((r) => ({ ...r, found: true })) }));
    fireEvent.click(within(dialog).getByRole("button", { name: "Check DNS now" }));
    await waitFor(() => expect(row("cornerbakery.example.org").textContent).toContain("Ready"));
    expect(screen.getByRole("dialog").textContent).not.toContain("Missing");
  });

  it("adds a mail domain, and shows the DNS record that proves who controls the domain", async () => {
    const api = await open("owner");
    fireEvent.click(screen.getByRole("button", { name: "Add mail domain" }));
    const dialog = screen.getByRole("dialog");
    api.on("POST /mail/domains", () => [422, { message: "Show that you control this domain.", errors: { name: ["Show that you control this domain."] }, verification: { type: "TXT", name: "_cloudfactory-challenge.shop.example.com", value: "cf-123" } }]);
    fireEvent.change(within(dialog).getByLabelText("Domain"), { target: { value: "Shop.example.com" } });
    fireEvent.click(within(dialog).getByRole("button", { name: "Add mail domain" }));
    await waitFor(() => expect(dialog.textContent).toContain("_cloudfactory-challenge.shop.example.com"));
    expect(dialog.textContent).toContain("cf-123");

    const added = apiDomain("acme.example.com", { id: "new-domain", name: "shop.example.com", status: "pending", records: [], dnsReady: false, mailboxes: 0, aliases: 0 });
    // The panel loads its lists again after each change.
    api.change<List>("/mail/domains", (b) => b.data.push(added));
    api.on("POST /mail/domains", () => [202, { mailDomain: added, job: apiJob({ id: "job-md", action: "SetMailDomain", state: "running", finishedAt: null }) }]);
    fireEvent.click(within(dialog).getByRole("button", { name: "Add domain again" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    expect(api.changes.at(-1)).toMatchObject({ method: "POST", path: "/mail/domains", body: { name: "shop.example.com" } });
    expect(row("shop.example.com").textContent).toContain("Setting up");
  });

  it("makes a mailbox and shows its password one time", async () => {
    const api = await open("owner");
    const box = getState().mailboxes[0];
    const added = { ...box, id: "new-box", address: "jo@acme.example.com", localPart: "jo", status: "pending", usedBytes: null, quotaBytes: 2147483648 };
    api.change<List>("/mail/mailboxes", (b) => b.data.push(added));
    api.on("POST /mail/mailboxes", () => [202, { mailbox: added, password: "tidy-lamp-river-42", job: apiJob({ id: "job-mb", action: "SetMailbox", state: "running", finishedAt: null }) }]);
    fireEvent.click(screen.getByRole("button", { name: "New mailbox" }));
    const dialog = screen.getByRole("dialog");
    fireEvent.change(within(dialog).getByLabelText("Address"), { target: { value: "Jo" } });
    fireEvent.change(within(dialog).getByLabelText("Size (GB)"), { target: { value: "2" } });
    fireEvent.click(within(dialog).getByRole("button", { name: "Create mailbox" }));
    await waitFor(() => expect(screen.getByRole("dialog").textContent).toContain("tidy-lamp-river-42"));
    expect(screen.getByRole("dialog").textContent).toContain("Password of jo@acme.example.com");
    expect(api.changes.at(-1)).toMatchObject({ body: { domainId: domain("acme.example.com").id, localPart: "jo", quotaBytes: 2147483648 } });
    fireEvent.click(screen.getByRole("button", { name: "I copied the password" }));
    expect(screen.queryByText("tidy-lamp-river-42")).toBeNull();
    expect(row("jo@acme.example.com").textContent).toContain("Setting up");
  });

  it("gives a mailbox a new password, and removes a mailbox", async () => {
    const api = await open("owner");
    const info = getState().mailboxes.find((m) => m.address === "info@acme.example.com")!;
    api.on(`PATCH /mail/mailboxes/${info.id}`, () => [202, { mailbox: info, password: "new-secret-77", job: apiJob({ id: "job-pw", action: "SetMailbox" }) }]);
    menu(row("info@acme.example.com"), "New password");
    await waitFor(() => expect(screen.getByRole("dialog").textContent).toContain("new-secret-77"));
    expect(api.changes.at(-1)).toMatchObject({ method: "PATCH", body: { newPassword: true } });
    fireEvent.click(screen.getByRole("button", { name: "I copied the password" }));

    api.change<List>("/mail/mailboxes", (b) => { b.data.find((m) => m.id === info.id)!.status = "removing"; });
    api.on(`DELETE /mail/mailboxes/${info.id}`, () => [202, { job: apiJob({ id: "job-rm", action: "DeleteMailbox", state: "running", finishedAt: null }) }]);
    menu(row("info@acme.example.com"), "Remove");
    expect(screen.getByRole("dialog").textContent).toContain("all its messages");
    fireEvent.click(screen.getByRole("button", { name: "Remove mailbox" }));
    await waitFor(() => expect(row("info@acme.example.com").textContent).toContain("Removing"));
  });

  it("makes an alias for mailboxes, and a forwarder for other addresses", async () => {
    const api = await open("owner");
    const alias = getState().mailAliases[0];
    api.on("POST /mail/aliases", (body) => {
      const input = body as { localPart: string };
      const added = { ...alias, id: `new-${input.localPart}`, address: `${input.localPart}@acme.example.com`, ...input, status: "pending" };
      api.change<List>("/mail/aliases", (b) => b.data.push(added));
      return [202, { mailAlias: added, job: apiJob({ id: "job-al", action: "SetMailAlias", state: "running", finishedAt: null }) }];
    });
    fireEvent.click(screen.getByRole("button", { name: "New alias or forwarder" }));
    let dialog = screen.getByRole("dialog");
    // With no destination, the form does not send.
    expect((within(dialog).getByRole("button", { name: "Create address" }) as HTMLButtonElement).disabled).toBe(true);
    fireEvent.change(within(dialog).getByLabelText("Address"), { target: { value: "sales" } });
    fireEvent.click(within(dialog).getByLabelText("info@acme.example.com"));
    fireEvent.click(within(dialog).getByRole("button", { name: "Create address" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    expect(api.changes.at(-1)).toMatchObject({ body: { localPart: "sales", kind: "alias", destinations: ["info@acme.example.com"] } });

    fireEvent.click(screen.getByRole("button", { name: "New alias or forwarder" }));
    dialog = screen.getByRole("dialog");
    fireEvent.change(within(dialog).getByLabelText("Address"), { target: { value: "away" } });
    fireEvent.click(within(dialog).getByLabelText(/Forwarder/));
    fireEvent.change(within(dialog).getByLabelText("Addresses that get the mail"), { target: { value: "Jo@Example.org\nsam@example.net" } });
    fireEvent.click(within(dialog).getByRole("button", { name: "Create address" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    expect(api.changes.at(-1)).toMatchObject({ body: { localPart: "away", kind: "forwarder", destinations: ["jo@example.org", "sam@example.net"] } });
    expect(row("away@acme.example.com").textContent).toContain("Forwarder");
  });

  it("does not remove a mail domain that has mailboxes", async () => {
    const api = await open("owner");
    menu(row("acme.example.com"), "Remove");
    expect(screen.getByRole("dialog").textContent).toContain("Remove them first");
    expect(api.changes.filter((c) => c.method === "DELETE")).toEqual([]);
  });
});

describe("email on a server", () => {
  const openServer = async (name: string) => {
    const api = await signedIn("administrator");
    const id = getState().servers.find((s) => s.name === name)!.id;
    render(<MemoryRouter initialEntries={[`/servers/${id}`]}><Routes><Route path="servers/:id" element={<ServersPage />} /></Routes></MemoryRouter>);
    return { api, id };
  };

  it("shows the mail server of a server with email", async () => {
    await openServer("mail-1");
    const card = screen.getByRole("heading", { name: "Email" }).closest(".card") as HTMLElement;
    expect(card.textContent).toContain("mail-1.panel.example");
    expect(card.textContent).toContain("Running");
  });

  it("turns on email with a host name", async () => {
    const { api, id } = await openServer("web-2");
    const card = screen.getByRole("heading", { name: "Email" }).closest(".card") as HTMLElement;
    expect(card.textContent).toContain("Email is off on this server");
    api.on(`PUT /servers/${id}/mail`, () => [202, { job: apiJob({ id: "job-mail", action: "SetMailServer", serverId: id }) }]);
    fireEvent.change(within(card).getByLabelText("Host name of the mail server"), { target: { value: "Mail.Example.net" } });
    fireEvent.click(within(card).getByRole("button", { name: "Turn on email" }));
    await waitFor(() => expect(api.changes.at(-1)).toMatchObject({ method: "PUT", path: `/servers/${id}/mail`, body: { hostname: "mail.example.net" } }));
  });
});
