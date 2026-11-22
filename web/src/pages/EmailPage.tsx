import { useId, useMemo, useState, type ReactNode } from "react";
import { Link } from "react-router-dom";
import { ChevronDown, ChevronRight, Lock, Search } from "lucide-react";
import { useAction, useStore } from "../api/hooks";
import { contextCustomers, inContext } from "../api/selectors";
import {
  MailVerificationError, checkMailDomain, createMailAlias, createMailDomain, createMailbox, deleteMailAlias, deleteMailDomain, deleteMailbox, encryptMailbox, mailboxPrivateKey, retryMailDomain, setMailbox, setMailboxEncryption,
  type MailVerification,
} from "../api/store";
import type { MailAlias, MailDomain, MailRecord, Mailbox, State } from "../api/types";
import { Topbar } from "../components/Layout";
import { Card, Field, FormError, Meter, Modal, PageHeader, RowMenu, Status, TableWrap, Tabs } from "../components/ui";
import { bytes, GB, MB } from "../lib/format";
import { MAIL_STATUS } from "../lib/labels";
import { AutoresponderDialog } from "./Autoresponder";
import { MailboxBackupsDialog } from "./MailboxBackups";
import { CopyField } from "./website/AccessTab";

const PURPOSE: Record<string, string> = {
  mx: "Other mail servers send the mail of the domain to this server",
  spf: "SPF: this server can send mail for the domain",
  dkim: "DKIM: the key that signs the mail of the domain",
  dmarc: "DMARC: what other servers do with mail that fails the checks",
};

const tone = (ratio: number) => (ratio >= 0.9 ? "danger" : ratio >= 0.75 ? "warning" : undefined);

/** The mail domains of the selected account that can get a new mailbox or address. */
const usable = (state: State) => state.mailDomains.filter((d) => inContext(state, d.customerId) && d.status === "active").sort((a, b) => a.name.localeCompare(b.name));

/** The number of rows on one page of a list. */
export const PAGE_SIZE = 25;

/**
 * A list that the user can search and read page by page. A customer can have hundreds of
 * mailboxes: the page shows 25 rows, and the search finds an address in all of them.
 */
function usePaged<T>(rows: T[], matches: (row: T, query: string) => boolean) {
  const [query, setQueryText] = useState("");
  const [page, setPage] = useState(0);
  const q = query.trim().toLowerCase();
  const found = useMemo(() => (q ? rows.filter((r) => matches(r, q)) : rows), [rows, q]); // eslint-disable-line react-hooks/exhaustive-deps
  const pages = Math.max(1, Math.ceil(found.length / PAGE_SIZE));
  const current = Math.min(page, pages - 1);
  return {
    query, q, found, pages, page: current,
    shown: found.slice(current * PAGE_SIZE, (current + 1) * PAGE_SIZE),
    setQuery: (v: string) => { setQueryText(v); setPage(0); },
    setPage,
    total: rows.length,
  };
}

type Paged = ReturnType<typeof usePaged<unknown>>;

/** The search field of a list, with filters and the button for a new item. */
function ListTools({ list, label, children, action }: { list: Paged; label: string; children?: ReactNode; action?: ReactNode }) {
  const id = useId();
  return (
    <div className="list-tools">
      <div className="search-field">
        <Search size={16} aria-hidden="true" />
        <label htmlFor={id} className="visually-hidden">{label}</label>
        <input id={id} type="search" className="input" placeholder={label} value={list.query} onChange={(e) => list.setQuery(e.target.value)} autoCapitalize="none" spellCheck={false} />
      </div>
      {children}
      <span className="list-tools-action">{action}</span>
    </div>
  );
}

/** "1 to 25 of 184", and the buttons for the previous and the next page. */
function Pager({ list, noun }: { list: Paged; noun: [string, string] }) {
  const n = list.found.length;
  if (n === 0) return null;
  const from = list.page * PAGE_SIZE + 1;
  const to = Math.min(n, from + PAGE_SIZE - 1);
  return (
    <nav className="pager" aria-label={`Pages of ${noun[1]}`}>
      <span className="muted" role="status">
        {n <= PAGE_SIZE ? `${n} ${noun[n === 1 ? 0 : 1]}` : `${from} to ${to} of ${n} ${noun[1]}`}
        {list.q && n !== list.total ? ` found in ${list.total}` : ""}
      </span>
      {list.pages > 1 && (
        <span className="pager-buttons">
          <button type="button" className="btn btn-sm" disabled={list.page === 0} onClick={() => list.setPage(list.page - 1)}>Previous</button>
          <span className="muted">Page {list.page + 1} of {list.pages}</span>
          <button type="button" className="btn btn-sm" disabled={list.page >= list.pages - 1} onClick={() => list.setPage(list.page + 1)}>Next</button>
        </span>
      )}
    </nav>
  );
}

/** The filter for one mail domain. With one domain, the filter is not necessary. */
function DomainFilter({ domains, value, onChange }: { domains: MailDomain[]; value: string; onChange: (id: string) => void }) {
  if (domains.length < 2) return null;
  return (
    <select className="select" aria-label="Mail domain" value={value} onChange={(e) => onChange(e.target.value)}>
      <option value="">All domains</option>
      {domains.map((d) => <option key={d.id} value={d.id}>{d.name}</option>)}
    </select>
  );
}

type Tab = "mailboxes" | "forwarders" | "domains";

/**
 * Email: the mailboxes with their aliases, the forwarders, and the mail domains of the customers.
 *
 * An alias is one more address of one mailbox, so the page shows it with that mailbox. A
 * forwarder sends mail to other addresses and keeps no mail, so it has its own list.
 */
export function EmailPage() {
  const state = useStore();
  const admin = state.session.user.role === "administrator";
  const domains = state.mailDomains.filter((d) => inContext(state, d.customerId)).sort((a, b) => a.name.localeCompare(b.name));
  const boxes = useMemo(() => state.mailboxes.filter((m) => inContext(state, m.customerId)).sort((a, b) => a.address.localeCompare(b.address)), [state]);
  const addresses = useMemo(() => state.mailAliases.filter((a) => inContext(state, a.customerId)).sort((a, b) => a.address.localeCompare(b.address)), [state]);
  // An alias goes with its mailbox. All other addresses are in the list of forwarders.
  const { aliases, forwarders } = useMemo(() => {
    const known = new Set(boxes.map((m) => m.address));
    const aliases = new Map<string, MailAlias[]>();
    const forwarders: MailAlias[] = [];
    for (const a of addresses) {
      const to = a.destinations[0];
      if (a.kind === "alias" && a.destinations.length === 1 && known.has(to)) aliases.set(to, [...(aliases.get(to) ?? []), a]);
      else forwarders.push(a);
    }
    return { aliases, forwarders };
  }, [boxes, addresses]);
  const hosts = state.servers.filter((s) => s.mail);
  const [tab, setTab] = useState<Tab>(domains.length === 0 ? "domains" : "mailboxes");
  const [domainId, setDomainId] = useState("");
  const filter = domains.some((d) => d.id === domainId) ? domainId : "";
  return (
    <>
      <Topbar />
      <main id="main" className="content content-wide">
        <PageHeader title="Email" description="Mailboxes on your own domains. Add a mail domain, add its DNS records, and then make mailboxes. A mail program reads the mail with IMAP and sends it with SMTP." />
        {admin && hosts.length === 0 && (
          <div className="alert alert-info" role="status">
            <p><strong>No server has email turned on.</strong> Open a server and turn on email there. Then the customers can add mail domains. <Link to="/servers">Open servers</Link></p>
          </div>
        )}
        <Tabs
          label="Email"
          current={tab}
          onSelect={(id) => setTab(id as Tab)}
          items={[
            { id: "mailboxes", label: `Mailboxes (${boxes.length})` },
            { id: "forwarders", label: `Forwarders (${forwarders.length})` },
            { id: "domains", label: `Domains (${domains.length})` },
          ]}
        />
        {tab === "mailboxes" && <MailboxesCard boxes={boxes} aliases={aliases} domains={domains} domainId={filter} onDomain={setDomainId} />}
        {tab === "forwarders" && <ForwardersCard forwarders={forwarders} domains={domains} domainId={filter} onDomain={setDomainId} />}
        {tab === "domains" && <DomainsCard domains={domains} onShowMailboxes={(id) => { setDomainId(id); setTab("mailboxes"); }} />}
      </main>
    </>
  );
}

function dnsState(d: MailDomain): { label: string; tone: "success" | "warning" | "neutral" } {
  if (d.records.length === 0) return { label: "No records yet", tone: "neutral" };
  if (d.dnsReady) return { label: "Ready", tone: "success" };
  if (!d.dnsCheckedAt) return { label: "Not checked", tone: "neutral" };
  const found = d.records.filter((r) => r.found).length;
  return { label: `${found} of ${d.records.length} records found`, tone: "warning" };
}

function DomainsCard({ domains, onShowMailboxes }: { domains: MailDomain[]; onShowMailboxes: (domainId: string) => void }) {
  const state = useStore();
  const customers = contextCustomers(state);
  const [creating, setCreating] = useState(false);
  const [showing, setShowing] = useState<string | null>(null);
  const [removing, setRemoving] = useState<MailDomain | null>(null);
  const check = useAction(checkMailDomain);
  const retry = useAction(retryMailDomain);
  const remove = useAction(deleteMailDomain);
  const customer = (id: string) => state.customers.find((c) => c.id === id);
  const showCustomer = customers.length > 1 || domains.some((d) => d.customerId !== customers[0]?.id);
  const shown = domains.find((d) => d.id === showing);
  const list = usePaged(domains, (d, q) => d.name.includes(q) || (customer(d.customerId)?.name ?? "").toLowerCase().includes(q));

  return (
    <Card title="Mail domains">
      <div className="card-body" style={{ paddingBottom: 12 }}>
        <p className="muted">A mail domain is the part of an address after the @ sign. Each domain needs DNS records for MX, SPF, DKIM and DMARC. Without them, other mail servers do not deliver mail to the domain, or they put its mail in the spam folder.</p>
        <FormError error={check.error ?? retry.error ?? remove.error} />
        <ListTools list={list} label="Search domains" action={<button type="button" className="btn btn-primary" onClick={() => setCreating(true)}>Add mail domain</button>} />
      </div>
      <TableWrap>
        <table>
          <thead>
            <tr>
              <th scope="col">Domain</th>
              <th scope="col">Status</th>
              <th scope="col">DNS records</th>
              <th scope="col">Mailboxes</th>
              <th scope="col">Aliases and forwarders</th>
              <th scope="col"><span className="visually-hidden">Actions</span></th>
            </tr>
          </thead>
          <tbody>
            {list.shown.map((d) => {
              const status = MAIL_STATUS[d.status];
              const dns = dnsState(d);
              return (
                <tr key={d.id}>
                  <td>
                    <strong>{d.name}</strong>
                    {showCustomer && <span className="cell-sub">{customer(d.customerId)?.name ?? "Removed customer"}</span>}
                  </td>
                  <td><Status tone={status.tone}>{status.label}</Status>{d.status === "failed" && <span className="cell-sub">The server could not add the domain. Try again.</span>}</td>
                  <td>
                    {d.status === "active" || d.records.length > 0 ? <Status tone={dns.tone}>{dns.label}</Status> : <span className="muted">After the domain is active</span>}
                    {d.records.length > 0 && <span className="cell-sub"><button type="button" className="btn btn-sm" onClick={() => setShowing(d.id)}>Show records</button></span>}
                  </td>
                  <td>{d.mailboxes > 0 ? <button type="button" className="btn-link" onClick={() => onShowMailboxes(d.id)} aria-label={`Show the ${d.mailboxes} mailboxes of ${d.name}`}>{d.mailboxes}</button> : 0}</td>
                  <td>{d.aliases}</td>
                  <td className="cell-actions">
                    {d.status !== "removing" && (
                      <RowMenu label={`Actions for the mail domain ${d.name}`} actions={[
                        ...(d.status === "failed" ? [{ label: "Try again", onSelect: () => void retry.run(d.id) }] : []),
                        ...(d.records.length > 0 ? [{ label: "Show DNS records", onSelect: () => setShowing(d.id) }, { label: "Check DNS now", onSelect: () => void check.run(d.id) }] : []),
                        { label: "Remove", danger: true, onSelect: () => setRemoving(d) },
                      ]} />
                    )}
                  </td>
                </tr>
              );
            })}
            {domains.length === 0 && <tr><td colSpan={6} className="muted">No mail domains. Add a domain to make the first mailbox.</td></tr>}
            {domains.length > 0 && list.found.length === 0 && <tr><td colSpan={6} className="muted">No domain has “{list.query.trim()}” in its name.</td></tr>}
          </tbody>
        </table>
      </TableWrap>
      <Pager list={list} noun={["domain", "domains"]} />
      {creating && <AddDomainDialog onClose={() => setCreating(false)} />}
      {shown && <RecordsDialog domain={shown} checking={check.busy} onCheck={() => void check.run(shown.id)} onClose={() => setShowing(null)} />}
      {removing && (
        removing.mailboxes + removing.aliases > 0 ? (
          <Modal title={`Remove mail domain ${removing.name}?`} onClose={() => setRemoving(null)} footer={<button type="button" className="btn btn-primary" onClick={() => setRemoving(null)}>Close</button>}>
            <p>This domain has {removing.mailboxes} {removing.mailboxes === 1 ? "mailbox" : "mailboxes"} and {removing.aliases} {removing.aliases === 1 ? "alias or forwarder" : "aliases and forwarders"}. Remove them first. Then you can remove the domain.</p>
          </Modal>
        ) : (
          <Modal
            title={`Remove mail domain ${removing.name}?`}
            onClose={() => setRemoving(null)}
            footer={<><button type="button" className="btn" onClick={() => setRemoving(null)}>Cancel</button><button type="button" className="btn btn-danger-solid" onClick={() => { const id = removing.id; setRemoving(null); void remove.run(id); }}>Remove mail domain</button></>}
          >
            <p>The mail server stops receiving mail for {removing.name}, and removes the DKIM keys of the domain. Remove the DNS records of the domain after that.</p>
          </Modal>
        )
      )}
    </Card>
  );
}

function AddDomainDialog({ onClose }: { onClose: () => void }) {
  const state = useStore();
  const customers = contextCustomers(state);
  const [customerId, setCustomerId] = useState(() => customers.find((c) => c.id === state.session.contextId)?.id ?? customers[0]?.id ?? "");
  const [name, setName] = useState("");
  const [serverId, setServerId] = useState("");
  const [verification, setVerification] = useState<MailVerification | null>(null);
  const create = useAction(createMailDomain);
  const hosts = state.servers.filter((s) => s.mail?.status === "running");
  return (
    <Modal
      title="Add mail domain"
      onClose={onClose}
      footer={<><button type="button" className="btn" onClick={onClose}>Cancel</button><button type="submit" form="new-mail-domain" className="btn btn-primary" disabled={create.busy}>{verification ? "Add domain again" : "Add mail domain"}</button></>}
    >
      <form id="new-mail-domain" noValidate onSubmit={async (e) => {
        e.preventDefault();
        const r = await create.run({ name, customerId, serverId });
        if (r.ok) return onClose();
        setVerification(r.error instanceof MailVerificationError ? r.error.verification : null);
      }}>
        {!verification && <FormError error={create.error} shown={["name", "customerId", "serverId"]} />}
        {customers.length > 1 && (
          <Field label="Customer" error={create.error?.fields.customerId}>
            {(p) => <select {...p} className="select" value={customerId} onChange={(e) => setCustomerId(e.target.value)}>{customers.map((c) => <option key={c.id} value={c.id}>{c.name}</option>)}</select>}
          </Field>
        )}
        <div style={{ marginTop: customers.length > 1 ? 14 : 0 }}>
          <Field label="Domain" hint={<>The part of the addresses after the @ sign, for example <code>example.com</code>. The domain can also have a website.</>} error={verification ? undefined : create.error?.fields.name}>
            {(p) => <input {...p} className="input" value={name} onChange={(e) => { setName(e.target.value); setVerification(null); }} autoCapitalize="none" spellCheck={false} placeholder="example.com" />}
          </Field>
        </div>
        {hosts.length > 1 && (
          <div style={{ marginTop: 14 }}>
            <Field label="Server" error={create.error?.fields.serverId}>
              {(p) => (
                <select {...p} className="select" value={serverId} onChange={(e) => setServerId(e.target.value)}>
                  <option value="">Automatic: the panel selects a server with email</option>
                  {hosts.map((s) => <option key={s.id} value={s.id}>{s.name} · {s.mail?.hostname}</option>)}
                </select>
              )}
            </Field>
          </div>
        )}
        {verification && (
          <div className="alert alert-warning alert-stack" role="alert" style={{ marginTop: 14 }}>
            <p><strong>Show that you control this domain.</strong> Add this DNS record, wait some minutes, and add the domain again.</p>
            <dl className="verify-record">
              <dt>Type</dt><dd><code>{verification.type}</code></dd>
              <dt>Name</dt><dd><code>{verification.name}</code></dd>
              <dt>Value</dt><dd><code>{verification.value}</code></dd>
            </dl>
          </div>
        )}
      </form>
    </Modal>
  );
}

function RecordState({ record }: { record: MailRecord }) {
  if (record.found === null) return <Status tone="neutral">Not checked</Status>;
  return record.found ? <Status tone="success">Found</Status> : <Status tone="warning">Missing</Status>;
}

/** The DNS records of a mail domain, and the settings for a mail program. */
function RecordsDialog({ domain, checking, onCheck, onClose }: { domain: MailDomain; checking: boolean; onCheck: () => void; onClose: () => void }) {
  return (
    <Modal
      wide
      title={`DNS records of ${domain.name}`}
      onClose={onClose}
      footer={<><button type="button" className="btn" onClick={onCheck} disabled={checking}>{checking ? "Checking…" : "Check DNS now"}</button><button type="button" className="btn btn-primary" onClick={onClose}>Close</button></>}
    >
      <p>Add these records at the DNS provider of {domain.name}. A change of the DNS can take some minutes before all servers see it.</p>
      <div className="mail-records">
        {domain.records.map((r) => (
          <section key={`${r.type} ${r.name} ${r.value}`} className="mail-record" aria-label={`${r.type} record for ${r.name}`}>
            <header>
              <span><strong>{r.type}</strong> <span className="muted">{PURPOSE[r.purpose] ?? r.purpose}</span></span>
              <RecordState record={r} />
            </header>
            <dl className="verify-record">
              <dt>Name</dt><dd><code>{r.name}</code></dd>
              <dt>Value</dt><dd><CopyField value={r.value} label={`Value of the ${r.type} record for ${r.name}`} /></dd>
            </dl>
          </section>
        ))}
      </div>
      <h3 style={{ marginTop: 18 }}>Webmail and settings for a mail program</h3>
      <table className="kv">
        <tbody>
          <tr><th scope="row">Webmail</th><td><a href={`https://${domain.mailHost}/`} target="_blank" rel="noreferrer">https://{domain.mailHost}</a>. Sign in with the full address and the password of the mailbox.</td></tr>
          <tr><th scope="row">Incoming mail</th><td>IMAP, server <code>{domain.mailHost}</code>, port <code>993</code>, SSL/TLS</td></tr>
          <tr><th scope="row">Outgoing mail</th><td>SMTP, server <code>{domain.mailHost}</code>, port <code>465</code> with SSL/TLS, or port <code>587</code> with STARTTLS</td></tr>
          <tr><th scope="row">Calendars and contacts</th><td>CalDAV and CardDAV, server <code>https://{domain.mailHost}/dav</code></td></tr>
          <tr><th scope="row">User name</th><td>The full address of the mailbox, for example <code>info@{domain.name}</code></td></tr>
        </tbody>
      </table>
    </Modal>
  );
}

/** The password of a mailbox. The panel shows it one time. */
function PasswordDialog({ address, password, mailboxId, onClose }: { address: string; password: string; mailboxId?: string; onClose: () => void }) {
  return (
    <Modal title={`Password of ${address}`} onClose={onClose} footer={<button type="button" className="btn btn-primary" onClick={onClose}>I copied the password</button>}>
      <p>Copy the password now. The panel does not show it again. If you lose it, give the mailbox a new password.</p>
      <div style={{ marginTop: 12 }}><CopyField value={password} label="Password" /></div>
      {mailboxId && (
        <div className="alert alert-info alert-stack" role="status" style={{ marginTop: 14 }}>
          <p><strong>This password also opens the private key of the mailbox.</strong> To read the encrypted mail, put the private key in a mail program and give this password.</p>
          <KeyDownload mailboxId={mailboxId} address={address} />
        </div>
      )}
    </Modal>
  );
}

/** Downloads the private keys that the panel made. The file is not usable without the password of the mailbox. */
function KeyDownload({ mailboxId, address }: { mailboxId: string; address: string }) {
  const get = useAction(mailboxPrivateKey);
  return (
    <>
      <FormError error={get.error} />
      <button type="button" className="btn btn-sm" disabled={get.busy} onClick={async () => {
        const r = await get.run(mailboxId);
        if (!r.ok) return;
        const url = URL.createObjectURL(new Blob([r.value], { type: "application/pgp-keys" }));
        const link = Object.assign(document.createElement("a"), { href: url, download: `${address}-private-key.asc` });
        link.click();
        URL.revokeObjectURL(url);
      }}>Download private key</button>
    </>
  );
}

/** Asks for the old password of an encrypted mailbox, so that its private key gets the new password too. */
function NewPasswordDialog({ mailbox, onClose, onDone }: { mailbox: Mailbox; onClose: () => void; onDone: (password: string) => void }) {
  const [current, setCurrent] = useState("");
  const change = useAction(setMailbox);
  return (
    <Modal
      title={`New password for ${mailbox.address}`}
      onClose={onClose}
      footer={<><button type="button" className="btn" onClick={onClose}>Cancel</button><button type="submit" form="mailbox-password" className="btn btn-primary" disabled={change.busy}>Make new password</button></>}
    >
      <form id="mailbox-password" noValidate onSubmit={async (e) => {
        e.preventDefault();
        const r = await change.run(mailbox.id, { newPassword: true, ...(current !== "" ? { currentPassword: current } : {}) });
        if (r.ok) onDone(r.value);
      }}>
        <p>The private key of this mailbox is protected with the password of the mailbox. Give the current password, and the private key gets the new password too.</p>
        <FormError error={change.error} shown={["currentPassword"]} />
        <div style={{ marginTop: 12 }}>
          <Field label="Current password (optional)" hint="Without it, the mailbox gets a new password and the private key keeps the old password. Keep the old password to open the private key." error={change.error?.fields.currentPassword}>
            {(p) => <input {...p} className="input" type="password" autoComplete="off" value={current} onChange={(e) => setCurrent(e.target.value)} />}
          </Field>
        </div>
      </form>
    </Modal>
  );
}

const fingerprint = (f: string) => f.toUpperCase().replace(/(.{4})(?=.)/g, "$1 ");

const KEY_WARNING: Record<string, string> = {
  expiresSoon: "The key expires in fewer than 14 days. Give a new key before that date.",
  weakRsa: "The RSA key is shorter than 3072 bits.",
  addressMismatch: "The key does not name the address of this mailbox.",
  postQuantumOnly: "Only some mail programs can decrypt messages for this key.",
  noKnownDecryptor: "Only some mail programs can decrypt messages for this key.",
  unusable: "The key cannot be used now. New mail is not encrypted. Give a new key.",
};

function MailboxesCard({ boxes, aliases, domains, domainId, onDomain }: { boxes: Mailbox[]; aliases: Map<string, MailAlias[]>; domains: MailDomain[]; domainId: string; onDomain: (id: string) => void }) {
  const state = useStore();
  const customers = contextCustomers(state);
  const active = usable(state);
  const [creating, setCreating] = useState(false);
  const [editing, setEditing] = useState<Mailbox | null>(null);
  const [encrypting, setEncrypting] = useState<string | null>(null);
  const [answering, setAnswering] = useState<Mailbox | null>(null);
  const [backingUp, setBackingUp] = useState<Mailbox | null>(null);
  const [removing, setRemoving] = useState<Mailbox | null>(null);
  // "withKey": the password also opens the private key that the panel made.
  const [secret, setSecret] = useState<{ address: string; password: string; withKey?: boolean } | null>(null);
  const [resetting, setResetting] = useState<string | null>(null);
  const [open, setOpen] = useState<Set<string>>(new Set());
  const change = useAction(setMailbox);
  const remove = useAction(deleteMailbox);
  // With one customer, the card tells how much of the plan the customer uses.
  const only = customers.length === 1 ? customers[0] : undefined;
  const plan = only && state.plans.find((p) => p.id === only.planId);
  const own = only ? boxes.filter((m) => m.customerId === only.id) : [];
  const reserved = own.reduce((n, m) => n + m.quotaBytes, 0);
  const showCustomer = customers.length > 1;
  const customer = (id: string) => state.customers.find((c) => c.id === id)?.name ?? "Removed customer";
  const inDomain = useMemo(() => (domainId ? boxes.filter((m) => m.domainId === domainId) : boxes), [boxes, domainId]);
  // The search finds a mailbox by its address, and also by the address of one of its aliases.
  const list = usePaged(inDomain, (m, q) => m.address.includes(q) || (aliases.get(m.address) ?? []).some((a) => a.address.includes(q)) || (showCustomer && customer(m.customerId).toLowerCase().includes(q)));
  const toggle = (id: string) => setOpen((s) => { const next = new Set(s); if (!next.delete(id)) next.add(id); return next; });
  const encrypted = boxes.find((m) => m.id === encrypting);
  const reset = boxes.find((m) => m.id === resetting);

  return (
    <Card title="Mailboxes">
      <div className="card-body" style={{ paddingBottom: 12 }}>
        <p className="muted">
          A mailbox keeps the mail of one address. Its user name is the full address. An alias is one more address of a mailbox.
          {only && plan && <> {only.name} has {own.length} of {plan.mailboxes} mailboxes, and {bytes(reserved)} of {bytes(plan.mailStorageBytes)} of mail storage.</>}
          {active.length === 0 && <> You can make a mailbox when a mail domain is active.</>}
        </p>
        <FormError error={change.error ?? remove.error} />
        <ListTools list={list} label="Search mailboxes and aliases" action={<button type="button" className="btn btn-primary" disabled={active.length === 0} onClick={() => setCreating(true)}>New mailbox</button>}>
          <DomainFilter domains={domains} value={domainId} onChange={(id) => { onDomain(id); list.setPage(0); }} />
        </ListTools>
      </div>
      <TableWrap>
        <table className="mailbox-table">
          <thead>
            <tr><th scope="col">Address</th><th scope="col">Storage</th><th scope="col">Status</th><th scope="col"><span className="visually-hidden">Actions</span></th></tr>
          </thead>
          <tbody>
            {list.shown.map((m) => {
              const status = MAIL_STATUS[m.status];
              const ratio = m.quotaBytes > 0 ? m.usedBytes / m.quotaBytes : 0;
              const own = aliases.get(m.address) ?? [];
              // A search that found an alias shows the aliases of that mailbox.
              const expanded = open.has(m.id) || (list.q !== "" && own.some((a) => a.address.includes(list.q)));
              return (
                <MailboxRows key={m.id} mailbox={m} aliases={own} expanded={expanded} onToggle={() => toggle(m.id)} domains={active}>
                  <td>
                    <span className="mailbox-name">
                      <strong>{m.address}</strong>
                      {m.encryption && <span className="badge badge-success" title="The mail server encrypts new mail with the OpenPGP key of the owner"><Lock size={12} aria-hidden="true" /> Encrypted</span>}
                    </span>
                    {showCustomer && <span className="cell-sub">{customer(m.customerId)}</span>}
                    <button type="button" className="alias-toggle" aria-expanded={expanded} onClick={() => toggle(m.id)}>
                      {expanded ? <ChevronDown size={14} aria-hidden="true" /> : <ChevronRight size={14} aria-hidden="true" />}
                      {own.length === 0 ? "No aliases" : own.length === 1 ? "1 alias" : `${own.length} aliases`}
                      <span className="visually-hidden"> of {m.address}</span>
                    </button>
                  </td>
                  <td className="nowrap">
                    <Meter inline value={ratio} label={`Storage: ${bytes(m.usedBytes)} of ${bytes(m.quotaBytes)}`} tone={tone(ratio)} />
                    {bytes(m.usedBytes)} of {bytes(m.quotaBytes)}
                  </td>
                  <td><Status tone={status.tone}>{status.label}</Status>{m.status === "failed" && <span className="cell-sub">The server could not make the mailbox. Remove it and make it again.</span>}</td>
                  <td className="cell-actions">
                    {m.status !== "removing" && (
                      <RowMenu label={`Actions for the mailbox ${m.address}`} actions={[
                        ...(m.status === "active" ? [
                          { label: "Add alias", onSelect: () => setOpen((s) => new Set(s).add(m.id)) },
                          { label: "Autoresponder", onSelect: () => setAnswering(m) },
                          { label: "Backups", onSelect: () => setBackingUp(m) },
                          { label: "Encryption", onSelect: () => setEncrypting(m.id) },
                          { label: "Change size", onSelect: () => setEditing(m) },
                          { label: "New password", onSelect: () => (m.privateKey ? setResetting(m.id) : void change.run(m.id, { newPassword: true }).then((r) => { if (r.ok && r.value) setSecret({ address: m.address, password: r.value }); })) },
                        ] : []),
                        { label: "Remove", danger: true, onSelect: () => setRemoving(m) },
                      ]} />
                    )}
                  </td>
                </MailboxRows>
              );
            })}
            {boxes.length === 0 && <tr><td colSpan={4} className="muted">No mailboxes.</td></tr>}
            {boxes.length > 0 && list.found.length === 0 && <tr><td colSpan={4} className="muted">{list.q ? <>No mailbox and no alias has “{list.query.trim()}” in its address.</> : "This domain has no mailboxes."}</td></tr>}
          </tbody>
        </table>
      </TableWrap>
      <Pager list={list} noun={["mailbox", "mailboxes"]} />
      {creating && <NewMailboxDialog domains={active} domainId={domainId} onClose={() => setCreating(false)} onCreated={(address, password, withKey) => { setCreating(false); setSecret({ address, password, withKey }); }} />}
      {editing && <SizeDialog mailbox={editing} onClose={() => setEditing(null)} />}
      {encrypted && <EncryptionDialog mailbox={encrypted} onClose={() => setEncrypting(null)} />}
      {answering && <AutoresponderDialog mailbox={answering} onClose={() => setAnswering(null)} />}
      {backingUp && <MailboxBackupsDialog mailbox={backingUp} onClose={() => setBackingUp(null)} />}
      {secret && <PasswordDialog address={secret.address} password={secret.password} mailboxId={secret.withKey ? boxes.find((m) => m.address === secret.address)?.id : undefined} onClose={() => setSecret(null)} />}
      {reset && <NewPasswordDialog mailbox={reset} onClose={() => setResetting(null)} onDone={(password) => { setResetting(null); if (password) setSecret({ address: reset.address, password, withKey: true }); }} />}
      {removing && (
        <Modal
          title={`Remove mailbox ${removing.address}?`}
          onClose={() => setRemoving(null)}
          footer={<><button type="button" className="btn" onClick={() => setRemoving(null)}>Cancel</button><button type="button" className="btn btn-danger-solid" onClick={() => { const id = removing.id; setRemoving(null); void remove.run(id); }}>Remove mailbox</button></>}
        >
          <p>The mail server removes the mailbox and all its messages. You cannot restore them.</p>
          {(aliases.get(removing.address) ?? []).length > 0 && <p style={{ marginTop: 8 }}>The mail server also removes the aliases of the mailbox: {(aliases.get(removing.address) ?? []).map((a) => a.address).join(", ")}.</p>}
        </Modal>
      )}
    </Card>
  );
}

/** The row of a mailbox and, when it is open, the row with its aliases. */
function MailboxRows({ mailbox, aliases, expanded, domains, children }: { mailbox: Mailbox; aliases: MailAlias[]; expanded: boolean; onToggle: () => void; domains: MailDomain[]; children: ReactNode }) {
  const remove = useAction(deleteMailAlias);
  const create = useAction(createMailAlias);
  const [local, setLocal] = useState("");
  const [domainId, setDomainId] = useState(mailbox.domainId);
  // An alias can be in each active mail domain of the same customer.
  const choices = domains.filter((d) => d.customerId === mailbox.customerId);
  const id = useId();
  return (
    <>
      <tr className={expanded ? "is-open" : undefined}>{children}</tr>
      {expanded && (
        <tr className="alias-row">
          <td colSpan={4}>
            <div className="alias-panel" role="group" aria-label={`Aliases of ${mailbox.address}`}>
              {aliases.length === 0 && <p className="muted">Mail for an alias goes to this mailbox. For example, give {mailbox.address} the alias sales@{mailbox.address.split("@")[1]}.</p>}
              {aliases.length > 0 && (
                <ul className="alias-list">
                  {aliases.map((a) => (
                    <li key={a.id}>
                      <span>{a.address}</span>
                      {a.status !== "active" && <Status tone={MAIL_STATUS[a.status].tone}>{MAIL_STATUS[a.status].label}</Status>}
                      {a.status !== "removing" && <button type="button" className="btn btn-sm btn-ghost" disabled={remove.busy} onClick={() => void remove.run(a.id)} aria-label={`Remove the alias ${a.address}`}>Remove</button>}
                    </li>
                  ))}
                </ul>
              )}
              <FormError error={remove.error} />
              {mailbox.status === "active" && choices.length > 0 && (
                <form className="alias-form" noValidate onSubmit={async (e) => {
                  e.preventDefault();
                  const r = await create.run({ domainId: choices.some((d) => d.id === domainId) ? domainId : choices[0].id, localPart: local, kind: "alias", destinations: [mailbox.address] });
                  if (r.ok) setLocal("");
                }}>
                  <label htmlFor={id}>New alias</label>
                  <div className="address-input">
                    <input id={id} className="input" value={local} onChange={(e) => setLocal(e.target.value)} autoCapitalize="none" spellCheck={false} placeholder="sales" aria-invalid={create.error ? true : undefined} />
                    <span aria-hidden="true">@</span>
                    {choices.length > 1 ? (
                      <select className="select" aria-label="Mail domain of the alias" value={domainId} onChange={(e) => setDomainId(e.target.value)}>
                        {choices.map((d) => <option key={d.id} value={d.id}>{d.name}</option>)}
                      </select>
                    ) : <span>{choices[0].name}</span>}
                    <button type="submit" className="btn" disabled={create.busy || local.trim() === ""}>Add alias</button>
                  </div>
                  {create.error && <p className="text-danger small" role="alert">{create.error.fields.localPart ?? create.error.fields.destinations ?? create.error.message}</p>}
                </form>
              )}
            </div>
          </td>
        </tr>
      )}
    </>
  );
}

/** The encryption of a mailbox: the OpenPGP public key of its owner. */
function EncryptionDialog({ mailbox, onClose }: { mailbox: Mailbox; onClose: () => void }) {
  const [key, setKey] = useState("");
  // "own" shows the form for an owner who has a key pair and keeps the private key.
  const [own, setOwn] = useState(false);
  const [password, setPassword] = useState("");
  const set = useAction(setMailboxEncryption);
  const make = useAction(encryptMailbox);
  const e = mailbox.encryption;
  const busy = set.busy || make.busy;
  const footer = own
    ? <><button type="button" className="btn" onClick={() => setOwn(false)}>Back</button><button type="submit" form="mailbox-key" className="btn btn-primary" disabled={busy || key.trim() === ""}>{set.busy ? "The mail server reads the key…" : "Use this key"}</button></>
    : e
      ? <><button type="button" className="btn btn-danger" disabled={busy} onClick={() => void set.run(mailbox.id, null).then((r) => { if (r.ok) onClose(); })}>Turn off encryption</button><button type="button" className="btn" onClick={() => setOwn(true)}>Use my own key</button><button type="button" className="btn btn-primary" onClick={onClose}>Close</button></>
      : <><button type="button" className="btn" onClick={onClose}>Cancel</button><button type="button" className="btn btn-primary" disabled={busy} onClick={() => void make.run(mailbox.id).then((r) => { if (r.ok) setPassword(r.value); })}>{make.busy ? "The panel makes the keys…" : "Turn on encryption"}</button></>;
  return (
    <Modal wide title={`Encryption of ${mailbox.address}`} onClose={onClose} footer={footer}>
      {e && (
        <div className="alert alert-info alert-stack" role="status">
          <p><strong>Encryption is on.</strong> The mail server encrypts each new message with this key before it stores the message.</p>
          <dl className="verify-record">
            <dt>Fingerprint</dt><dd>{e.fingerprints.map((f) => <code key={f} style={{ display: "block" }}>{fingerprint(f)}</code>)}</dd>
            <dt>Key</dt><dd>{e.keyType}</dd>
            <dt>Expires</dt><dd>{e.expiresAt ? new Date(e.expiresAt).toLocaleDateString(undefined, { dateStyle: "long" }) : "Never"}</dd>
          </dl>
          {e.warnings.map((w) => KEY_WARNING[w] && <p key={w} className="text-warning"><strong>Warning:</strong> {KEY_WARNING[w]}</p>)}
        </div>
      )}
      {password !== "" && (
        <div style={{ marginTop: 14 }}>
          <p><strong>The mailbox has a new password.</strong> Copy it now. The panel does not show it again. The password also opens the private key.</p>
          <div style={{ marginTop: 10 }}><CopyField value={password} label="Password" /></div>
        </div>
      )}
      {!own && mailbox.privateKey && (
        <div style={{ marginTop: 14 }}>
          <p>{e ? "The panel made the keys of this mailbox." : "The panel has the private key from the time when the encryption was on. It opens the messages of that time."} The webmail gets the private key by itself: it asks one time for the password of the mailbox. To read the encrypted mail in a mail program, for example Thunderbird, download the private key and give the password of the mailbox.</p>
          {!mailbox.privateKey.passwordCurrent && <p className="text-warning" style={{ marginTop: 6 }}><strong>Warning:</strong> The mailbox got a new password after the panel made the key. The private key has an older password of the mailbox.</p>}
          <div style={{ marginTop: 10 }}><KeyDownload mailboxId={mailbox.id} address={mailbox.address} /></div>
        </div>
      )}
      {!own && <FormError error={set.error ?? make.error} />}
      {!own && !e && (
        <>
          <p style={{ marginTop: mailbox.privateKey ? 14 : 0 }}>The panel makes a pair of OpenPGP keys for the mailbox. The mail server encrypts each new message with the public key before it stores the message. A person who gets the disks or the backups of the server cannot read the mail.</p>
          <ul className="plain-list" style={{ margin: "10px 0 14px" }}>
            <li>The mailbox gets a new password. The panel shows it one time. The private key is protected with the same password, and the panel keeps no password.</li>
            <li>To read the mail, a mail program needs the private key and OpenPGP, for example Thunderbird. You download the private key here.</li>
            <li>The messages that the mailbox has now do not change. The subject and the addresses of a message stay readable.</li>
            <li>The server cannot search the text of encrypted messages.</li>
          </ul>
          <button type="button" className="btn btn-sm" onClick={() => setOwn(true)}>I have my own key</button>
        </>
      )}
      {own && (
        <form id="mailbox-key" noValidate onSubmit={async (ev) => {
          ev.preventDefault();
          const r = await set.run(mailbox.id, key);
          if (r.ok) { setKey(""); setOwn(false); }
        }}>
          <p>Give the public key of a key pair that you made in your mail program. The private key stays with you: the panel never has it. If you lose the private key, nobody can read the encrypted messages.</p>
          <FormError error={set.error} shown={["publicKey"]} />
          <div style={{ marginTop: 12 }}>
            <Field label="OpenPGP public key" hint={<>Paste the key that the mail program exports. It starts with <code>-----BEGIN PGP PUBLIC KEY BLOCK-----</code>. Never give the private key.</>} error={set.error?.fields.publicKey}>
              {(p) => <textarea {...p} className="input mono" rows={8} value={key} onChange={(ev) => setKey(ev.target.value)} autoCapitalize="none" spellCheck={false} placeholder="-----BEGIN PGP PUBLIC KEY BLOCK-----" />}
            </Field>
          </div>
          <label className="btn btn-sm file-button" style={{ marginTop: 10 }}>
            Select a key file
            <input type="file" accept=".asc,.gpg,.pgp,.key,.txt,text/plain,application/pgp-keys" className="visually-hidden" onChange={(ev) => { const f = ev.target.files?.[0]; if (f && f.size <= 65536) void f.text().then(setKey); }} />
          </label>
        </form>
      )}
    </Modal>
  );
}

function AddressFields({ local, onLocal, domainId, onDomain, domains, error }: { local: string; onLocal: (v: string) => void; domainId: string; onDomain: (v: string) => void; domains: MailDomain[]; error?: string }) {
  return (
    <Field label="Address" hint="Use lower-case letters, digits, dots, hyphens and underscores." error={error}>
      {(p) => (
        <div className="address-input">
          <input {...p} className="input" value={local} onChange={(e) => onLocal(e.target.value)} autoCapitalize="none" spellCheck={false} placeholder="info" />
          <span aria-hidden="true">@</span>
          <select className="select" aria-label="Mail domain" value={domainId} onChange={(e) => onDomain(e.target.value)}>
            {domains.map((d) => <option key={d.id} value={d.id}>{d.name}</option>)}
          </select>
        </div>
      )}
    </Field>
  );
}

function NewMailboxDialog({ domains, domainId: first, onClose, onCreated }: { domains: MailDomain[]; domainId?: string; onClose: () => void; onCreated: (address: string, password: string, encrypted: boolean) => void }) {
  const [domainId, setDomainId] = useState(domains.find((d) => d.id === first)?.id ?? domains[0]?.id ?? "");
  const [local, setLocal] = useState("");
  const [size, setSize] = useState("1");
  const [encrypt, setEncrypt] = useState(false);
  const create = useAction(createMailbox);
  const domain = domains.find((d) => d.id === domainId);
  return (
    <Modal
      title="New mailbox"
      onClose={onClose}
      footer={<><button type="button" className="btn" onClick={onClose}>Cancel</button><button type="submit" form="new-mailbox" className="btn btn-primary" disabled={create.busy}>Create mailbox</button></>}
    >
      <form id="new-mailbox" noValidate onSubmit={async (e) => {
        e.preventDefault();
        const r = await create.run({ domainId, localPart: local, quotaBytes: Math.round(+size * GB), ...(encrypt ? { encrypt: true } : {}) });
        if (r.ok) onCreated(`${local.trim().toLowerCase()}@${domain?.name ?? ""}`, r.value, encrypt);
      }}>
        <FormError error={create.error} shown={["localPart", "domainId", "quotaBytes"]} />
        <AddressFields local={local} onLocal={setLocal} domainId={domainId} onDomain={setDomainId} domains={domains} error={create.error?.fields.localPart ?? create.error?.fields.domainId} />
        <div style={{ marginTop: 14 }}>
          <Field label="Size (GB)" hint="The mail server refuses new mail when the mailbox is full." error={create.error?.fields.quotaBytes}>
            {(p) => <input {...p} className="input" type="number" min={0.1} step="any" value={size} onChange={(e) => setSize(e.target.value)} />}
          </Field>
        </div>
        <label className="check" style={{ marginTop: 14, display: "flex", gap: 8, alignItems: "flex-start" }}>
          <input type="checkbox" checked={encrypt} onChange={(e) => setEncrypt(e.target.checked)} style={{ marginTop: 3 }} />
          <span>
            <strong>Encrypt the stored mail</strong>
            <span className="muted small" style={{ display: "block" }}>The panel makes OpenPGP keys for the mailbox, and the mail server encrypts each new message. To read the mail, a mail program needs the private key and the password of the mailbox.</span>
          </span>
        </label>
        <p className="muted small" style={{ marginTop: 12 }}>The panel makes a password and shows it one time.</p>
      </form>
    </Modal>
  );
}

function SizeDialog({ mailbox, onClose }: { mailbox: Mailbox; onClose: () => void }) {
  const [size, setSize] = useState(String(+(mailbox.quotaBytes / GB).toFixed(2)));
  const change = useAction(setMailbox);
  return (
    <Modal
      title={`Change the size of ${mailbox.address}`}
      onClose={onClose}
      footer={<><button type="button" className="btn" onClick={onClose}>Cancel</button><button type="submit" form="mailbox-size" className="btn btn-primary" disabled={change.busy}>Save changes</button></>}
    >
      <form id="mailbox-size" noValidate onSubmit={async (e) => {
        e.preventDefault();
        const r = await change.run(mailbox.id, { quotaBytes: Math.round(+size * GB) });
        if (r.ok) onClose();
      }}>
        <FormError error={change.error} shown={["quotaBytes"]} />
        <Field label="Size (GB)" hint={`The mailbox uses ${bytes(mailbox.usedBytes)} now. The smallest size is ${bytes(100 * MB)}.`} error={change.error?.fields.quotaBytes}>
          {(p) => <input {...p} className="input" type="number" min={0.1} step="any" value={size} onChange={(e) => setSize(e.target.value)} />}
        </Field>
      </form>
    </Modal>
  );
}

const KIND_LABEL: Record<MailAlias["kind"], string> = { alias: "Alias", forwarder: "Forwarder" };

function ForwardersCard({ forwarders, domains, domainId, onDomain }: { forwarders: MailAlias[]; domains: MailDomain[]; domainId: string; onDomain: (id: string) => void }) {
  const state = useStore();
  const active = usable(state);
  const [creating, setCreating] = useState(false);
  const [removing, setRemoving] = useState<MailAlias | null>(null);
  const remove = useAction(deleteMailAlias);
  const inDomain = useMemo(() => (domainId ? forwarders.filter((a) => a.domainId === domainId) : forwarders), [forwarders, domainId]);
  const list = usePaged(inDomain, (a, q) => a.address.includes(q) || a.destinations.some((d) => d.includes(q)));
  return (
    <Card title="Forwarders">
      <div className="card-body" style={{ paddingBottom: 12 }}>
        <p className="muted">A forwarder sends each message to one or more other addresses, for example to addresses on other mail servers. A forwarder keeps no mail. For one more address of a mailbox, add an alias to the mailbox.</p>
        <FormError error={remove.error} />
        <ListTools list={list} label="Search forwarders" action={<button type="button" className="btn btn-primary" disabled={active.length === 0} onClick={() => setCreating(true)}>New forwarder</button>}>
          <DomainFilter domains={domains} value={domainId} onChange={(id) => { onDomain(id); list.setPage(0); }} />
        </ListTools>
      </div>
      <TableWrap>
        <table>
          <thead>
            <tr><th scope="col">Address</th><th scope="col">Sends to</th><th scope="col">Status</th><th scope="col"><span className="visually-hidden">Actions</span></th></tr>
          </thead>
          <tbody>
            {list.shown.map((a) => {
              const status = MAIL_STATUS[a.status];
              return (
                <tr key={a.id}>
                  <td><strong>{a.address}</strong>{a.kind === "alias" && <span className="cell-sub">{KIND_LABEL[a.kind]}</span>}</td>
                  <td>{a.destinations.map((d) => <span key={d} style={{ display: "block" }}>{d}</span>)}</td>
                  <td><Status tone={status.tone}>{status.label}</Status></td>
                  <td className="cell-actions">
                    {a.status !== "removing" && <RowMenu label={`Actions for the address ${a.address}`} actions={[{ label: "Remove", danger: true, onSelect: () => setRemoving(a) }]} />}
                  </td>
                </tr>
              );
            })}
            {forwarders.length === 0 && <tr><td colSpan={4} className="muted">No forwarders.</td></tr>}
            {forwarders.length > 0 && list.found.length === 0 && <tr><td colSpan={4} className="muted">{list.q ? <>No forwarder has “{list.query.trim()}” in its addresses.</> : "This domain has no forwarders."}</td></tr>}
          </tbody>
        </table>
      </TableWrap>
      <Pager list={list} noun={["forwarder", "forwarders"]} />
      {creating && <NewForwarderDialog domains={active} domainId={domainId} onClose={() => setCreating(false)} />}
      {removing && (
        <Modal
          title={`Remove ${removing.address}?`}
          onClose={() => setRemoving(null)}
          footer={<><button type="button" className="btn" onClick={() => setRemoving(null)}>Cancel</button><button type="button" className="btn btn-danger-solid" onClick={() => { const id = removing.id; setRemoving(null); void remove.run(id); }}>Remove address</button></>}
        >
          <p>The mail server stops receiving mail for {removing.address}. The destinations keep the mail that they have.</p>
        </Modal>
      )}
    </Card>
  );
}

function NewForwarderDialog({ domains, domainId: first, onClose }: { domains: MailDomain[]; domainId?: string; onClose: () => void }) {
  const [domainId, setDomainId] = useState(domains.find((d) => d.id === first)?.id ?? domains[0]?.id ?? "");
  const [local, setLocal] = useState("");
  const [text, setText] = useState("");
  const create = useAction(createMailAlias);
  const destinations = text.split(/[\s,;]+/).map((x) => x.trim().toLowerCase()).filter(Boolean);
  const destinationError = create.error?.fields.destinations ?? Object.entries(create.error?.fields ?? {}).find(([k]) => k.startsWith("destinations."))?.[1];
  return (
    <Modal
      title="New forwarder"
      onClose={onClose}
      footer={<><button type="button" className="btn" onClick={onClose}>Cancel</button><button type="submit" form="new-forwarder" className="btn btn-primary" disabled={create.busy || destinations.length === 0}>Create forwarder</button></>}
    >
      <form id="new-forwarder" noValidate onSubmit={async (e) => {
        e.preventDefault();
        const r = await create.run({ domainId, localPart: local, kind: "forwarder", destinations });
        if (r.ok) onClose();
      }}>
        <FormError error={create.error} shown={["localPart", "domainId", "kind", "destinations"]} />
        <AddressFields local={local} onLocal={setLocal} domainId={domainId} onDomain={setDomainId} domains={domains} error={create.error?.fields.localPart ?? create.error?.fields.domainId} />
        <div style={{ marginTop: 14 }}>
          <Field label="Addresses that get the mail" hint="One address on each line. You can give 50 addresses." error={destinationError}>
            {(p) => <textarea {...p} className="input" rows={3} value={text} onChange={(e) => setText(e.target.value)} autoCapitalize="none" spellCheck={false} placeholder="name@example.org" />}
          </Field>
        </div>
      </form>
    </Modal>
  );
}
