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

const tab = (name: RegExp) => fireEvent.click(screen.getByRole("button", { name }));
const boxes = () => screen.getAllByRole("row").filter((r) => r.querySelector(".mailbox-name")).map((r) => r.querySelector("strong")!.textContent);

describe("the email page", () => {
  it("shows each mailbox with its aliases, and the forwarders and the domains in their own tabs", async () => {
    await open("owner");
    const info = row("info@acme.example.com");
    expect(info.textContent).toContain("1.2 GB of 5 GB");
    expect(within(info).getByRole("meter", { name: "Storage: 1.2 GB of 5 GB" })).toBeTruthy();
    expect(screen.getByText(/Acme Ltd has \d+ of 100 mailboxes/)).toBeTruthy();
    // The alias hello@ is an address of the mailbox info@: it is with that mailbox.
    expect(screen.queryByText("hello@acme.example.com")).toBeNull();
    fireEvent.click(within(info).getByRole("button", { name: /1 alias/ }));
    expect(within(screen.getByRole("group", { name: "Aliases of info@acme.example.com" })).getByText("hello@acme.example.com")).toBeTruthy();
    expect(row("olu@acme.example.com").textContent).toContain("Encrypted");

    tab(/^Forwarders \(1\)/);
    expect(row("team@acme.example.com").textContent).toContain("morgan@partner.example.net");
    tab(/^Domains \(1\)/);
    const acme = row("acme.example.com");
    expect(acme.textContent).toContain("Active");
    expect(acme.textContent).toContain("Ready");
    // An owner does not see the hint for the administrator.
    expect(screen.queryByText(/No server has email turned on/)).toBeNull();
  });

  it("shows 25 mailboxes on a page, and finds a mailbox by its address or by an alias", async () => {
    await open("administrator");
    expect(boxes()).toHaveLength(25);
    expect(screen.getByRole("navigation", { name: "Pages of mailboxes" }).textContent).toContain("1 to 25 of 187 mailboxes");
    fireEvent.click(screen.getByRole("button", { name: "Next" }));
    expect(screen.getByRole("navigation", { name: "Pages of mailboxes" }).textContent).toContain("Page 2 of 8");

    const search = screen.getByRole("searchbox", { name: "Search mailboxes and aliases" });
    fireEvent.change(search, { target: { value: "Okoro" } });
    expect(boxes()).toHaveLength(12);
    expect(screen.getByRole("navigation", { name: "Pages of mailboxes" }).textContent).toContain("12 mailboxes found in 187");
    // The alias "support" is an address of ada.adams: the search shows that mailbox with its aliases.
    fireEvent.change(search, { target: { value: "support" } });
    expect(boxes()).toEqual(["ada.adams@northwind.example.com"]);
    expect(screen.getByRole("group", { name: "Aliases of ada.adams@northwind.example.com" }).textContent).toContain("support@northwind.example.com");
    fireEvent.change(search, { target: { value: "nobody-has-this" } });
    expect(screen.getByText(/No mailbox and no alias has/)).toBeTruthy();

    // The filter shows the mailboxes of one domain.
    fireEvent.change(search, { target: { value: "" } });
    fireEvent.change(screen.getByRole("combobox", { name: "Mail domain" }), { target: { value: domain("cornerbakery.example.org").id } });
    expect(boxes()).toEqual(["hello@cornerbakery.example.org", "orders@cornerbakery.example.org"]);
    // The number of mailboxes of a domain opens the mailboxes of that domain.
    tab(/^Domains/);
    fireEvent.click(screen.getByRole("button", { name: "Show the 4 mailboxes of acme.example.com" }));
    expect(boxes()).toHaveLength(4);
  });

  it("shows the DNS records of a domain and the settings for a mail program", async () => {
    const api = await open("administrator");
    tab(/^Domains/);
    // The sample data has a domain with no DKIM record in the DNS.
    const bakery = row("cornerbakery.example.org");
    expect(bakery.textContent).toMatch(/\d of \d records found/);
    menu(bakery, "Show DNS records");
    const dialog = screen.getByRole("dialog");
    expect(dialog.textContent).toContain("v=spf1 mx -all");
    expect(dialog.textContent).toContain("Missing");
    expect(dialog.textContent).toContain("IMAP, server mail-1.panel.example, port 993");
    // The webmail is on the host name of the mail server.
    expect(within(dialog).getByRole("link", { name: "https://mail-1.panel.example" }).getAttribute("href")).toBe("https://mail-1.panel.example/");
    api.on("POST /mail/domains/*/check", () => apiDomain("cornerbakery.example.org", { dnsReady: true, records: domain("cornerbakery.example.org").records.map((r) => ({ ...r, found: true })) }));
    fireEvent.click(within(dialog).getByRole("button", { name: "Check DNS now" }));
    await waitFor(() => expect(row("cornerbakery.example.org").textContent).toContain("Ready"));
    expect(screen.getByRole("dialog").textContent).not.toContain("Missing");
  });

  it("adds a mail domain, and shows the DNS record that proves who controls the domain", async () => {
    const api = await open("owner");
    tab(/^Domains/);
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
    api.on("POST /mail/domains", () => [202, { mailDomain: added, job: apiJob({ id: "job-md", action: "SetMailDomain" }) }]);
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
    api.on("POST /mail/mailboxes", () => [202, { mailbox: added, password: "tidy-lamp-river-42", job: apiJob({ id: "job-mb", action: "SetMailbox" }) }]);
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
    // The aliases of the mailbox go with it.
    expect(screen.getByRole("dialog").textContent).toContain("also removes the aliases of the mailbox: hello@acme.example.com");
    fireEvent.click(screen.getByRole("button", { name: "Remove mailbox" }));
    await waitFor(() => expect(row("info@acme.example.com").textContent).toContain("Removing"));
  });

  it("adds an alias to a mailbox, and removes an alias", async () => {
    const api = await open("owner");
    const alias = getState().mailAliases.find((a) => a.address === "hello@acme.example.com")!;
    api.on("POST /mail/aliases", (body) => {
      const input = body as { localPart: string };
      const added = { ...alias, id: `new-${input.localPart}`, address: `${input.localPart}@acme.example.com`, ...input, status: "pending" };
      api.change<List>("/mail/aliases", (b) => b.data.push(added));
      return [202, { mailAlias: added, job: apiJob({ id: "job-al", action: "SetMailAlias", state: "running", finishedAt: null }) }];
    });
    menu(row("sales@acme.example.com"), "Add alias");
    const panel = screen.getByRole("group", { name: "Aliases of sales@acme.example.com" });
    // With no name, the form does not send.
    expect((within(panel).getByRole("button", { name: "Add alias" }) as HTMLButtonElement).disabled).toBe(true);
    fireEvent.change(within(panel).getByLabelText("New alias"), { target: { value: "Orders" } });
    fireEvent.click(within(panel).getByRole("button", { name: "Add alias" }));
    await waitFor(() => expect(panel.textContent).toContain("orders@acme.example.com"));
    expect(api.changes.at(-1)).toMatchObject({ body: { domainId: domain("acme.example.com").id, localPart: "orders", kind: "alias", destinations: ["sales@acme.example.com"] } });
    expect(row("sales@acme.example.com").textContent).toContain("1 alias");

    api.on(`DELETE /mail/aliases/${alias.id}`, () => {
      api.change<List>("/mail/aliases", (b) => { b.data = b.data.filter((a) => a.id !== alias.id); });
      return [202, { job: apiJob({ id: "job-ar", action: "DeleteMailAlias" }) }];
    });
    fireEvent.click(within(row("info@acme.example.com")).getByRole("button", { name: /1 alias/ }));
    fireEvent.click(screen.getByRole("button", { name: "Remove the alias hello@acme.example.com" }));
    await waitFor(() => expect(row("info@acme.example.com").textContent).toContain("No aliases"));
  });

  it("makes a forwarder for other addresses", async () => {
    const api = await open("owner");
    const alias = getState().mailAliases[0];
    api.on("POST /mail/aliases", (body) => {
      const input = body as { localPart: string };
      const added = { ...alias, id: `new-${input.localPart}`, address: `${input.localPart}@acme.example.com`, ...input, status: "pending" };
      api.change<List>("/mail/aliases", (b) => b.data.push(added));
      return [202, { mailAlias: added, job: apiJob({ id: "job-al", action: "SetMailAlias", state: "running", finishedAt: null }) }];
    });
    tab(/^Forwarders/);
    fireEvent.click(screen.getByRole("button", { name: "New forwarder" }));
    const dialog = screen.getByRole("dialog");
    // With no destination, the form does not send.
    expect((within(dialog).getByRole("button", { name: "Create forwarder" }) as HTMLButtonElement).disabled).toBe(true);
    fireEvent.change(within(dialog).getByLabelText("Address"), { target: { value: "away" } });
    fireEvent.change(within(dialog).getByLabelText("Addresses that get the mail"), { target: { value: "Jo@Example.org\nsam@example.net" } });
    fireEvent.click(within(dialog).getByRole("button", { name: "Create forwarder" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    expect(api.changes.at(-1)).toMatchObject({ body: { localPart: "away", kind: "forwarder", destinations: ["jo@example.org", "sam@example.net"] } });
    expect(row("away@acme.example.com").textContent).toContain("sam@example.net");
  });

  it("makes the keys of a mailbox, shows the new password one time and gives the private key", async () => {
    const api = await open("owner");
    const info = getState().mailboxes.find((m) => m.address === "info@acme.example.com")!;
    const encryption = { fingerprints: ["9E1777E56AFB330404EC710E0868736DF95B6518"], keyType: "OpenPGP v4: X25519", expiresAt: null, warnings: [] };
    menu(row("info@acme.example.com"), "Encryption");
    const dialog = screen.getByRole("dialog");
    expect(dialog.textContent).toContain("The panel makes a pair of OpenPGP keys for the mailbox");
    expect(within(dialog).queryByLabelText("OpenPGP public key")).toBeNull();
    api.on(`PATCH /mail/mailboxes/${info.id}`, () => {
      api.change<List>("/mail/mailboxes", (b) => { Object.assign(b.data.find((m) => m.id === info.id)!, { encryption, privateKey: { passwordCurrent: true } }); });
      return [202, { mailbox: info, password: "new horse battery", job: apiJob({ id: "job-make", action: "SetMailbox" }) }];
    });
    fireEvent.click(within(dialog).getByRole("button", { name: "Turn on encryption" }));
    await waitFor(() => expect(screen.getByRole("dialog").textContent).toContain("9E17 77E5 6AFB 3304 04EC 710E 0868 736D F95B 6518"));
    expect(api.changes.at(-1)).toMatchObject({ method: "PATCH", body: { encrypt: true, newPassword: true } });
    expect(screen.getByRole("dialog").textContent).toContain("new horse battery");
    expect(screen.getByRole("dialog").textContent).toContain("The panel made the keys of this mailbox");

    // The private key comes from the panel only when the user asks for it.
    let asked = 0;
    const made: Blob[] = [];
    URL.createObjectURL = (blob: Blob) => { made.push(blob); return "blob:key"; };
    URL.revokeObjectURL = () => {};
    api.on(`GET /mail/mailboxes/${info.id}/private-key`, () => { asked++; return [200, { address: info.address, privateKey: "-----BEGIN PGP PRIVATE KEY BLOCK-----\n\nxYYE\n-----END PGP PRIVATE KEY BLOCK-----\n", passwordCurrent: true }]; });
    expect(asked).toBe(0);
    fireEvent.click(within(screen.getByRole("dialog")).getByRole("button", { name: "Download private key" }));
    await waitFor(() => expect(made).toHaveLength(1));
    expect(await made[0].text()).toContain("BEGIN PGP PRIVATE KEY BLOCK");
    fireEvent.click(within(screen.getByRole("dialog")).getAllByRole("button", { name: "Close" }).at(-1)!);

    // A new password for a mailbox with keys of the panel: the old password moves the private key.
    menu(row("info@acme.example.com"), "New password");
    const reset = screen.getByRole("dialog");
    api.on(`PATCH /mail/mailboxes/${info.id}`, () => [202, { mailbox: { ...info, encryption, privateKey: { passwordCurrent: true } }, password: "second horse", job: null }]);
    fireEvent.change(within(reset).getByLabelText("Current password (optional)"), { target: { value: "new horse battery" } });
    fireEvent.click(within(reset).getByRole("button", { name: "Make new password" }));
    await waitFor(() => expect(screen.getByRole("dialog").textContent).toContain("This password also opens the private key"));
    expect(api.changes.at(-1)).toMatchObject({ body: { newPassword: true, currentPassword: "new horse battery" } });
  });

  it("makes a new mailbox with encryption", async () => {
    const api = await open("owner");
    const added = { ...getState().mailboxes[0], id: "vault-box", address: "vault@acme.example.com", localPart: "vault", status: "pending", usedBytes: null, privateKey: { passwordCurrent: true } };
    api.change<List>("/mail/mailboxes", (b) => b.data.push(added));
    api.on("POST /mail/mailboxes", () => [202, { mailbox: added, password: "calm-stone-harbour-17", job: apiJob({ id: "job-vault", action: "SetMailbox" }) }]);
    fireEvent.click(screen.getByRole("button", { name: "New mailbox" }));
    const dialog = screen.getByRole("dialog");
    fireEvent.change(within(dialog).getByLabelText("Address"), { target: { value: "vault" } });
    fireEvent.click(within(dialog).getByRole("checkbox", { name: /Encrypt the stored mail/ }));
    fireEvent.click(within(dialog).getByRole("button", { name: "Create mailbox" }));
    await waitFor(() => expect(screen.getByRole("dialog").textContent).toContain("calm-stone-harbour-17"));
    expect(api.changes.at(-1)).toMatchObject({ method: "POST", body: { localPart: "vault", encrypt: true } });
    // The dialog with the password also gives the private key.
    expect(screen.getByRole("dialog").textContent).toContain("This password also opens the private key");
    expect(within(screen.getByRole("dialog")).getByRole("button", { name: "Download private key" })).toBeTruthy();
  });

  it("turns on the encryption of a mailbox with a public key of the owner, and shows the fingerprint", async () => {
    const api = await open("owner");
    const info = getState().mailboxes.find((m) => m.address === "info@acme.example.com")!;
    const key = "-----BEGIN PGP PUBLIC KEY BLOCK-----\n\nmDME\n-----END PGP PUBLIC KEY BLOCK-----";
    const encryption = { fingerprints: ["9E1777E56AFB330404EC710E0868736DF95B6518"], keyType: "OpenPGP v4: X25519", expiresAt: null, warnings: ["addressMismatch"] };
    menu(row("info@acme.example.com"), "Encryption");
    let dialog = screen.getByRole("dialog");
    fireEvent.click(within(dialog).getByRole("button", { name: "I have my own key" }));
    expect(dialog.textContent).toContain("The private key stays with you");
    expect((within(dialog).getByRole("button", { name: "Use this key" }) as HTMLButtonElement).disabled).toBe(true);

    // The mail server refuses a key that cannot encrypt: the job fails, and the dialog tells why.
    api.on(`PATCH /mail/mailboxes/${info.id}`, () => [202, { mailbox: info, password: null, job: apiJob({ id: "job-key", action: "SetMailbox", state: "failed", error: "Turn on encryption: the mail server refused the public key: The key contains no key usable for encryption." }) }]);
    fireEvent.change(within(dialog).getByLabelText("OpenPGP public key"), { target: { value: key } });
    fireEvent.click(within(dialog).getByRole("button", { name: "Use this key" }));
    await waitFor(() => expect(dialog.textContent).toContain("The mail server refused the key. The key contains no key usable for encryption."));

    api.on(`PATCH /mail/mailboxes/${info.id}`, () => {
      api.change<List>("/mail/mailboxes", (b) => { b.data.find((m) => m.id === info.id)!.encryption = encryption; });
      return [202, { mailbox: info, password: null, job: apiJob({ id: "job-key2", action: "SetMailbox" }) }];
    });
    fireEvent.click(within(dialog).getByRole("button", { name: "Use this key" }));
    await waitFor(() => expect(screen.getByRole("dialog").textContent).toContain("9E17 77E5 6AFB 3304 04EC 710E 0868 736D F95B 6518"));
    expect(api.changes.at(-1)).toMatchObject({ method: "PATCH", body: { publicKey: key } });
    dialog = screen.getByRole("dialog");
    expect(dialog.textContent).toContain("Encryption is on");
    expect(dialog.textContent).toContain("The key does not name the address of this mailbox");
    expect(row("info@acme.example.com").textContent).toContain("Encrypted");

    api.on(`PATCH /mail/mailboxes/${info.id}`, () => {
      api.change<List>("/mail/mailboxes", (b) => { b.data.find((m) => m.id === info.id)!.encryption = null; });
      return [202, { mailbox: info, password: null, job: apiJob({ id: "job-key3", action: "SetMailbox" }) }];
    });
    fireEvent.click(within(dialog).getByRole("button", { name: "Turn off encryption" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    expect(api.changes.at(-1)).toMatchObject({ method: "PATCH", body: { publicKey: null } });
    expect(row("info@acme.example.com").textContent).not.toContain("Encrypted");
  });

  it("does not remove a mail domain that has mailboxes", async () => {
    const api = await open("owner");
    tab(/^Domains/);
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

  it("shows and changes the spam filter and the outbound relay", async () => {
    const { api, id } = await openServer("mail-1");
    const card = screen.getByRole("heading", { name: "Email" }).closest(".card") as HTMLElement;
    expect(card.textContent).toContain("On. Junk folder at score 5. Refused at score 15.");
    expect(card.textContent).toContain("Through the relay smtp.relay.example.net:587, as mail-1.");
    api.on(`PUT /servers/${id}/mail/settings`, () => [202, { job: apiJob({ id: "job-settings", action: "SetMailSettings", serverId: id }) }]);

    // The same user keeps the password: the request has no password.
    fireEvent.click(within(card).getByRole("button", { name: "Change" }));
    let dialog = screen.getByRole("dialog");
    expect((within(dialog).getByLabelText("Password") as HTMLInputElement).placeholder).not.toBe("");
    fireEvent.change(within(dialog).getByLabelText("Junk folder at score"), { target: { value: "6.5" } });
    fireEvent.change(within(dialog).getByLabelText("Port"), { target: { value: "465" } });
    fireEvent.click(within(dialog).getByRole("button", { name: "Save settings" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    expect(api.changes.at(-1)).toEqual({ method: "PUT", path: `/servers/${id}/mail/settings`, body: { spamFilter: true, spamScore: 6.5, rejectScore: 15, relay: { host: "smtp.relay.example.net", port: 465, username: "mail-1" } } });

    // A different user needs its password. With no refusal and no relay, the request has null.
    fireEvent.click(within(card).getByRole("button", { name: "Change" }));
    dialog = screen.getByRole("dialog");
    fireEvent.change(within(dialog).getByLabelText("User name"), { target: { value: "other" } });
    fireEvent.change(within(dialog).getByLabelText("Password"), { target: { value: "new-secret" } });
    fireEvent.click(within(dialog).getByRole("button", { name: "Save settings" }));
    await waitFor(() => expect(api.changes.at(-1)).toMatchObject({ body: { relay: { username: "other", password: "new-secret" } } }));
    fireEvent.click(within(card).getByRole("button", { name: "Change" }));
    dialog = screen.getByRole("dialog");
    fireEvent.click(within(dialog).getByRole("checkbox", { name: "Refuse a message with a high score" }));
    fireEvent.click(within(dialog).getByRole("radio", { name: /^Send directly/ }));
    fireEvent.click(within(dialog).getByRole("button", { name: "Save settings" }));
    await waitFor(() => expect(api.changes.at(-1)).toMatchObject({ body: { spamFilter: true, rejectScore: null, relay: null } }));
  });
});
