import { useState } from "react";
import { Link } from "react-router-dom";
import { useAction, useStore } from "../api/hooks";
import { contextCustomers, inContext } from "../api/selectors";
import {
  MailVerificationError, checkMailDomain, createMailAlias, createMailDomain, createMailbox, deleteMailAlias, deleteMailDomain, deleteMailbox, retryMailDomain, setMailbox,
  type MailVerification,
} from "../api/store";
import type { MailAlias, MailDomain, MailRecord, Mailbox, State } from "../api/types";
import { Topbar } from "../components/Layout";
import { Card, Field, FormError, Meter, Modal, PageHeader, RowMenu, Status, TableWrap } from "../components/ui";
import { bytes, GB, MB } from "../lib/format";
import { MAIL_STATUS } from "../lib/labels";
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

/** Email: the mail domains, the mailboxes, and the aliases and forwarders of the customers. */
export function EmailPage() {
  const state = useStore();
  const admin = state.session.user.role === "administrator";
  const domains = state.mailDomains.filter((d) => inContext(state, d.customerId)).sort((a, b) => a.name.localeCompare(b.name));
  const hosts = state.servers.filter((s) => s.mail);
  return (
    <>
      <Topbar />
      <main id="main" className="content content-wide">
        <PageHeader title="Email" description="Mailboxes on your own domains. Add a mail domain, add its DNS records, and then make mailboxes, aliases and forwarders. A mail program reads the mail with IMAP and sends it with SMTP." />
        <div className="stack">
          {admin && hosts.length === 0 && (
            <div className="alert alert-info" role="status">
              <p><strong>No server has email turned on.</strong> Open a server and turn on email there. Then the customers can add mail domains. <Link to="/servers">Open servers</Link></p>
            </div>
          )}
          <DomainsCard domains={domains} />
          <MailboxesCard />
          <AliasesCard />
        </div>
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

function DomainsCard({ domains }: { domains: MailDomain[] }) {
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

  return (
    <Card title="Mail domains" actions={<button type="button" className="btn btn-primary" onClick={() => setCreating(true)}>Add mail domain</button>}>
      <div className="card-body" style={{ paddingBottom: 12 }}>
        <p className="muted">A mail domain is the part of an address after the @ sign. Each domain needs DNS records for MX, SPF, DKIM and DMARC. Without them, other mail servers do not deliver mail to the domain, or they put its mail in the spam folder.</p>
        <FormError error={check.error ?? retry.error ?? remove.error} />
      </div>
      <TableWrap>
        <table>
          <thead>
            <tr>
              <th scope="col">Domain</th>
              <th scope="col">Status</th>
              <th scope="col">DNS records</th>
              <th scope="col">Mailboxes</th>
              <th scope="col">Other addresses</th>
              <th scope="col"><span className="visually-hidden">Actions</span></th>
            </tr>
          </thead>
          <tbody>
            {domains.map((d) => {
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
                  <td>{d.mailboxes}</td>
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
          </tbody>
        </table>
      </TableWrap>
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
      <h3 style={{ marginTop: 18 }}>Settings for a mail program</h3>
      <table className="kv">
        <tbody>
          <tr><th scope="row">Incoming mail</th><td>IMAP, server <code>{domain.mailHost}</code>, port <code>993</code>, SSL/TLS</td></tr>
          <tr><th scope="row">Outgoing mail</th><td>SMTP, server <code>{domain.mailHost}</code>, port <code>465</code> with SSL/TLS, or port <code>587</code> with STARTTLS</td></tr>
          <tr><th scope="row">User name</th><td>The full address of the mailbox, for example <code>info@{domain.name}</code></td></tr>
        </tbody>
      </table>
    </Modal>
  );
}

/** The password of a mailbox. The panel shows it one time. */
function PasswordDialog({ address, password, onClose }: { address: string; password: string; onClose: () => void }) {
  return (
    <Modal title={`Password of ${address}`} onClose={onClose} footer={<button type="button" className="btn btn-primary" onClick={onClose}>I copied the password</button>}>
      <p>Copy the password now. The panel does not show it again. If you lose it, give the mailbox a new password.</p>
      <div style={{ marginTop: 12 }}><CopyField value={password} label="Password" /></div>
    </Modal>
  );
}

function MailboxesCard() {
  const state = useStore();
  const customers = contextCustomers(state);
  const boxes = state.mailboxes.filter((m) => inContext(state, m.customerId)).sort((a, b) => a.address.localeCompare(b.address));
  const domains = usable(state);
  const [creating, setCreating] = useState(false);
  const [editing, setEditing] = useState<Mailbox | null>(null);
  const [removing, setRemoving] = useState<Mailbox | null>(null);
  const [secret, setSecret] = useState<{ address: string; password: string } | null>(null);
  const change = useAction(setMailbox);
  const remove = useAction(deleteMailbox);
  // With one customer, the card tells how much of the plan the customer uses.
  const only = customers.length === 1 ? customers[0] : undefined;
  const plan = only && state.plans.find((p) => p.id === only.planId);
  const own = only ? boxes.filter((m) => m.customerId === only.id) : [];
  const reserved = own.reduce((n, m) => n + m.quotaBytes, 0);

  return (
    <Card title="Mailboxes" actions={<button type="button" className="btn btn-primary" disabled={domains.length === 0} onClick={() => setCreating(true)}>New mailbox</button>}>
      <div className="card-body" style={{ paddingBottom: 12 }}>
        <p className="muted">
          A mailbox keeps the mail of one address. Its user name is the full address.
          {only && plan && <> {only.name} has {own.length} of {plan.mailboxes} mailboxes, and {bytes(reserved)} of {bytes(plan.mailStorageBytes)} of mail storage.</>}
          {domains.length === 0 && <> You can make a mailbox when a mail domain is active.</>}
        </p>
        <FormError error={change.error ?? remove.error} />
      </div>
      <TableWrap>
        <table>
          <thead>
            <tr><th scope="col">Address</th><th scope="col">Storage</th><th scope="col">Status</th><th scope="col"><span className="visually-hidden">Actions</span></th></tr>
          </thead>
          <tbody>
            {boxes.map((m) => {
              const status = MAIL_STATUS[m.status];
              const ratio = m.quotaBytes > 0 ? m.usedBytes / m.quotaBytes : 0;
              return (
                <tr key={m.id}>
                  <td><strong>{m.address}</strong></td>
                  <td className="nowrap">
                    <Meter inline value={ratio} label={`Storage: ${bytes(m.usedBytes)} of ${bytes(m.quotaBytes)}`} tone={tone(ratio)} />
                    {bytes(m.usedBytes)} of {bytes(m.quotaBytes)}
                  </td>
                  <td><Status tone={status.tone}>{status.label}</Status>{m.status === "failed" && <span className="cell-sub">The server could not make the mailbox. Remove it and make it again.</span>}</td>
                  <td className="cell-actions">
                    {m.status !== "removing" && (
                      <RowMenu label={`Actions for the mailbox ${m.address}`} actions={[
                        ...(m.status === "active" ? [
                          { label: "Change size", onSelect: () => setEditing(m) },
                          { label: "New password", onSelect: () => void change.run(m.id, { newPassword: true }).then((r) => { if (r.ok && r.value) setSecret({ address: m.address, password: r.value }); }) },
                        ] : []),
                        { label: "Remove", danger: true, onSelect: () => setRemoving(m) },
                      ]} />
                    )}
                  </td>
                </tr>
              );
            })}
            {boxes.length === 0 && <tr><td colSpan={4} className="muted">No mailboxes.</td></tr>}
          </tbody>
        </table>
      </TableWrap>
      {creating && <NewMailboxDialog domains={domains} onClose={() => setCreating(false)} onCreated={(address, password) => { setCreating(false); setSecret({ address, password }); }} />}
      {editing && <SizeDialog mailbox={editing} onClose={() => setEditing(null)} />}
      {secret && <PasswordDialog {...secret} onClose={() => setSecret(null)} />}
      {removing && (
        <Modal
          title={`Remove mailbox ${removing.address}?`}
          onClose={() => setRemoving(null)}
          footer={<><button type="button" className="btn" onClick={() => setRemoving(null)}>Cancel</button><button type="button" className="btn btn-danger-solid" onClick={() => { const id = removing.id; setRemoving(null); void remove.run(id); }}>Remove mailbox</button></>}
        >
          <p>The mail server removes the mailbox and all its messages. You cannot restore them.</p>
        </Modal>
      )}
    </Card>
  );
}

/** An address field: the part before the @ sign, and the domain. */
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

function NewMailboxDialog({ domains, onClose, onCreated }: { domains: MailDomain[]; onClose: () => void; onCreated: (address: string, password: string) => void }) {
  const [domainId, setDomainId] = useState(domains[0]?.id ?? "");
  const [local, setLocal] = useState("");
  const [size, setSize] = useState("1");
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
        const r = await create.run({ domainId, localPart: local, quotaBytes: Math.round(+size * GB) });
        if (r.ok) onCreated(`${local.trim().toLowerCase()}@${domain?.name ?? ""}`, r.value);
      }}>
        <FormError error={create.error} shown={["localPart", "domainId", "quotaBytes"]} />
        <AddressFields local={local} onLocal={setLocal} domainId={domainId} onDomain={setDomainId} domains={domains} error={create.error?.fields.localPart ?? create.error?.fields.domainId} />
        <div style={{ marginTop: 14 }}>
          <Field label="Size (GB)" hint="The mail server refuses new mail when the mailbox is full." error={create.error?.fields.quotaBytes}>
            {(p) => <input {...p} className="input" type="number" min={0.1} step="any" value={size} onChange={(e) => setSize(e.target.value)} />}
          </Field>
        </div>
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

function AliasesCard() {
  const state = useStore();
  const aliases = state.mailAliases.filter((a) => inContext(state, a.customerId)).sort((a, b) => a.address.localeCompare(b.address));
  const domains = usable(state);
  const [creating, setCreating] = useState(false);
  const [removing, setRemoving] = useState<MailAlias | null>(null);
  const remove = useAction(deleteMailAlias);
  return (
    <Card title="Aliases and forwarders" actions={<button type="button" className="btn btn-primary" disabled={domains.length === 0} onClick={() => setCreating(true)}>New alias or forwarder</button>}>
      <div className="card-body" style={{ paddingBottom: 12 }}>
        <p className="muted">An alias is one more address for mailboxes of the same customer. A forwarder sends each message to addresses on other mail servers. These addresses keep no mail.</p>
        <FormError error={remove.error} />
      </div>
      <TableWrap>
        <table>
          <thead>
            <tr><th scope="col">Address</th><th scope="col">Type</th><th scope="col">Sends to</th><th scope="col">Status</th><th scope="col"><span className="visually-hidden">Actions</span></th></tr>
          </thead>
          <tbody>
            {aliases.map((a) => {
              const status = MAIL_STATUS[a.status];
              return (
                <tr key={a.id}>
                  <td><strong>{a.address}</strong></td>
                  <td>{KIND_LABEL[a.kind]}</td>
                  <td>{a.destinations.map((d) => <span key={d} style={{ display: "block" }}>{d}</span>)}</td>
                  <td><Status tone={status.tone}>{status.label}</Status></td>
                  <td className="cell-actions">
                    {a.status !== "removing" && <RowMenu label={`Actions for the address ${a.address}`} actions={[{ label: "Remove", danger: true, onSelect: () => setRemoving(a) }]} />}
                  </td>
                </tr>
              );
            })}
            {aliases.length === 0 && <tr><td colSpan={5} className="muted">No aliases and no forwarders.</td></tr>}
          </tbody>
        </table>
      </TableWrap>
      {creating && <NewAliasDialog domains={domains} onClose={() => setCreating(false)} />}
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

function NewAliasDialog({ domains, onClose }: { domains: MailDomain[]; onClose: () => void }) {
  const state = useStore();
  const [domainId, setDomainId] = useState(domains[0]?.id ?? "");
  const [local, setLocal] = useState("");
  const [kind, setKind] = useState<MailAlias["kind"]>("alias");
  const [picked, setPicked] = useState<string[]>([]);
  const [text, setText] = useState("");
  const create = useAction(createMailAlias);
  const domain = domains.find((d) => d.id === domainId);
  // An alias sends to mailboxes of the customer of the domain.
  const boxes = state.mailboxes.filter((m) => m.customerId === domain?.customerId && m.status === "active").sort((a, b) => a.address.localeCompare(b.address));
  const destinations = kind === "alias" ? picked.filter((a) => boxes.some((m) => m.address === a)) : text.split(/[\s,;]+/).map((x) => x.trim().toLowerCase()).filter(Boolean);
  const destinationError = create.error?.fields.destinations ?? Object.entries(create.error?.fields ?? {}).find(([k]) => k.startsWith("destinations."))?.[1];
  return (
    <Modal
      title="New alias or forwarder"
      onClose={onClose}
      footer={<><button type="button" className="btn" onClick={onClose}>Cancel</button><button type="submit" form="new-alias" className="btn btn-primary" disabled={create.busy || destinations.length === 0}>Create address</button></>}
    >
      <form id="new-alias" noValidate onSubmit={async (e) => {
        e.preventDefault();
        const r = await create.run({ domainId, localPart: local, kind, destinations });
        if (r.ok) onClose();
      }}>
        <FormError error={create.error} shown={["localPart", "domainId", "kind", "destinations"]} />
        <AddressFields local={local} onLocal={setLocal} domainId={domainId} onDomain={(id) => { setDomainId(id); setPicked([]); }} domains={domains} error={create.error?.fields.localPart ?? create.error?.fields.domainId} />
        <fieldset className="radio-cards" style={{ marginTop: 14 }}>
          <legend>Type</legend>
          <label className="radio-card"><input type="radio" name="alias-kind" checked={kind === "alias"} onChange={() => setKind("alias")} /><span><strong>Alias</strong><span>Sends to mailboxes of this customer</span></span></label>
          <label className="radio-card"><input type="radio" name="alias-kind" checked={kind === "forwarder"} onChange={() => setKind("forwarder")} /><span><strong>Forwarder</strong><span>Sends to addresses on other mail servers</span></span></label>
        </fieldset>
        {kind === "alias" ? (
          <fieldset className="checkbox-list" style={{ marginTop: 14 }}>
            <legend>Mailboxes that get the mail</legend>
            {boxes.map((m) => (
              <label key={m.id} className="check"><input type="checkbox" checked={picked.includes(m.address)} onChange={(e) => setPicked(e.target.checked ? [...picked, m.address] : picked.filter((x) => x !== m.address))} />{m.address}</label>
            ))}
            {boxes.length === 0 && <p className="muted">This customer has no mailboxes. Make a mailbox first.</p>}
            {destinationError && <p className="text-danger small" role="alert">{destinationError}</p>}
          </fieldset>
        ) : (
          <div style={{ marginTop: 14 }}>
            <Field label="Addresses that get the mail" hint="One address on each line. You can give 50 addresses." error={destinationError}>
              {(p) => <textarea {...p} className="input" rows={3} value={text} onChange={(e) => setText(e.target.value)} autoCapitalize="none" spellCheck={false} placeholder="name@example.org" />}
            </Field>
          </div>
        )}
      </form>
    </Modal>
  );
}
