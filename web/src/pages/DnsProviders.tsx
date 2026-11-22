import { useState } from "react";
import { Link } from "react-router-dom";
import { useAction, useStore } from "../api/hooks";
import { contextCustomers, inContext } from "../api/selectors";
import { deleteDnsAccount, moveDnsZone, saveDnsAccount } from "../api/store";
import type { DnsAccount, DnsZone, State } from "../api/types";
import { Card, Field, FormError, Modal, RowMenu, Status, TableWrap } from "../components/ui";
import { relativeTime } from "../lib/format";
import { CopyField } from "./website/AccessTab";

/**
 * External DNS providers, for example Cloudflare. A plugin offers each provider. A customer
 * adds an account with the credentials of the provider, and then makes a zone there. The panel
 * keeps the records and sends each change to the provider.
 */

/** The accounts that a zone of the customer can use: the accounts of the customer, and of the panel. */
export function usableAccounts(state: State, customerId: string): DnsAccount[] {
  return state.dnsAccounts.filter((a) => a.available && (a.customerId === undefined || a.customerId === customerId));
}

/** The label of the state of the name servers at the registrar, with the name of the provider of the zone. */
export function delegationText(zone: DnsZone, fallback: string): string {
  return zone.provider && zone.delegation.state === "here" ? `Uses ${zone.provider.name}` : fallback;
}

/** The names of the DNSSEC algorithms and digest types that a provider can give. */
export const DNSSEC_ALGORITHMS: Record<number, string> = { 8: "RSA with SHA-256", 13: "ECDSA P-256 with SHA-256", 14: "ECDSA P-384 with SHA-384", 15: "Ed25519" };
export const DS_DIGESTS: Record<number, string> = { 1: "SHA-1", 2: "SHA-256", 4: "SHA-384" };

/** The card with the DNS accounts on the list of zones. */
export function AccountsCard() {
  const state = useStore();
  const admin = state.session.user.role === "administrator";
  const accounts = state.dnsAccounts.filter((a) => a.customerId === undefined || inContext(state, a.customerId));
  const [editing, setEditing] = useState<DnsAccount | "new" | null>(null);
  const [removing, setRemoving] = useState<DnsAccount | null>(null);
  const remove = useAction(deleteDnsAccount);
  const customer = (id?: string) => (id ? state.customers.find((c) => c.id === id)?.name ?? "Removed customer" : "Panel: each customer");
  if (state.dnsProviders.length === 0 && accounts.length === 0 && !admin) return null;

  return (
    <Card title="External DNS providers" actions={state.dnsProviders.length > 0 && <button type="button" className="btn btn-sm" onClick={() => setEditing("new")}>Add account</button>}>
      <div className="card-body" style={{ paddingBottom: 12 }}>
        <p className="muted">
          A zone can use an external DNS provider, for example Cloudflare, and not the name servers of this panel. The panel keeps the records and sends each change to the provider.
          {state.dnsProviders.length === 0 && (admin
            ? <> To offer a provider, install a plugin that has one, for example the Cloudflare DNS plugin, on the <Link to="/plugins">Plugins</Link> page.</>
            : " Your provider has no external DNS provider now.")}
        </p>
        <FormError error={remove.error} />
      </div>
      {accounts.length > 0 && (
        <TableWrap>
          <table>
            <thead><tr><th scope="col">Account</th><th scope="col">Provider</th><th scope="col">Owner</th><th scope="col">Zones</th><th scope="col">Checked</th><th scope="col"><span className="visually-hidden">Actions</span></th></tr></thead>
            <tbody>
              {accounts.map((a) => {
                const own = admin || a.customerId !== undefined;
                return (
                  <tr key={a.id}>
                    <td><span className="cell-title">{a.name}</span>{a.account && <span className="cell-sub">{a.account}</span>}</td>
                    <td>{a.providerName}{!a.available && <span className="cell-sub"><Status tone="warning">Plugin off</Status></span>}</td>
                    <td>{customer(a.customerId)}</td>
                    <td>{a.zones}</td>
                    <td className="muted nowrap">{a.checkedAt ? relativeTime(a.checkedAt) : "Never"}</td>
                    <td className="cell-actions">
                      {own && (
                        <RowMenu label={`Actions for the DNS account ${a.name}`} actions={[
                          ...(a.available ? [{ label: "Change", onSelect: () => setEditing(a) }] : []),
                          { label: "Remove", danger: true, onSelect: () => setRemoving(a) },
                        ]} />
                      )}
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </TableWrap>
      )}
      {editing && <AccountDialog account={editing === "new" ? undefined : editing} onClose={() => setEditing(null)} />}
      {removing && (
        <Modal
          title={`Remove the DNS account ${removing.name}?`}
          onClose={() => setRemoving(null)}
          footer={<><button type="button" className="btn" onClick={() => setRemoving(null)}>Cancel</button><button type="button" className="btn btn-danger-solid" disabled={removing.zones > 0} onClick={() => { const a = removing; setRemoving(null); void remove.run(a.id); }}>Remove account</button></>}
        >
          {removing.zones > 0
            ? <p>{removing.zones} {removing.zones === 1 ? "zone uses" : "zones use"} this account. Move the zones to a different DNS provider, or remove them, first.</p>
            : <p>The panel forgets the credentials of the account. The zones at {removing.providerName} do not change.</p>}
        </Modal>
      )}
    </Card>
  );
}

/** Adds an account at an external DNS provider, or changes its name and its credentials. */
function AccountDialog({ account, onClose }: { account?: DnsAccount; onClose: () => void }) {
  const state = useStore();
  const admin = state.session.user.role === "administrator";
  const customers = contextCustomers(state);
  const [providerKey, setProviderKey] = useState(() => account ? `${account.pluginId}/${account.provider}` : state.dnsProviders[0] ? `${state.dnsProviders[0].pluginId}/${state.dnsProviders[0].id}` : "");
  const provider = state.dnsProviders.find((p) => `${p.pluginId}/${p.id}` === providerKey);
  const [name, setName] = useState(account?.name ?? "");
  const [owner, setOwner] = useState(() => admin ? "" : customers.find((c) => c.id === state.session.contextId)?.id ?? customers[0]?.id ?? "");
  const [values, setValues] = useState<Record<string, string>>(() => Object.fromEntries(Object.entries(account?.credentials ?? {}).map(([k, v]) => [k, v ?? ""])));
  const save = useAction(saveDnsAccount);
  const fields = (provider?.credentials ?? []).map((f) => f.id);

  return (
    <Modal
      title={account ? `Change the DNS account ${account.name}` : "Add a DNS account"}
      onClose={onClose}
      footer={<><button type="button" className="btn" onClick={onClose}>Cancel</button><button type="submit" form="dns-account" className="btn btn-primary" disabled={save.busy || !provider}>{save.busy ? "Checking…" : account ? "Save" : "Add account"}</button></>}
    >
      <form id="dns-account" noValidate onSubmit={async (e) => {
        e.preventDefault();
        if (!provider) return;
        const credentials = Object.fromEntries(fields.map((id) => [id, values[id] ?? ""]));
        const r = await save.run({ name, pluginId: provider.pluginId, provider: provider.id, credentials, customerId: owner || null }, account?.id);
        if (r.ok) onClose();
      }}>
        <FormError error={save.error} shown={["name", "provider", "customerId", ...fields.map((f) => `credentials.${f}`)]} />
        {!account && (
          <Field label="Provider" error={save.error?.fields.provider}>
            {(p) => (
              <select {...p} className="select" value={providerKey} onChange={(e) => setProviderKey(e.target.value)}>
                {state.dnsProviders.map((d) => <option key={`${d.pluginId}/${d.id}`} value={`${d.pluginId}/${d.id}`}>{d.name} ({d.pluginName})</option>)}
              </select>
            )}
          </Field>
        )}
        <Field label="Name" hint="A name for you, for example Main Cloudflare account." error={save.error?.fields.name}>
          {(p) => <input {...p} className="input" value={name} maxLength={60} onChange={(e) => setName(e.target.value)} />}
        </Field>
        {!account && (admin || customers.length > 1) && (
          <Field label="Owner" hint={admin ? "Each customer can make zones with an account of the panel. Only an administrator sees its credentials." : undefined} error={save.error?.fields.customerId}>
            {(p) => (
              <select {...p} className="select" value={owner} onChange={(e) => setOwner(e.target.value)}>
                {admin && <option value="">The panel: each customer can use it</option>}
                {customers.map((c) => <option key={c.id} value={c.id}>{c.name}</option>)}
              </select>
            )}
          </Field>
        )}
        {(provider?.credentials ?? []).map((f) => (
          <Field key={f.id} label={f.optional ? `${f.label} (optional)` : f.label} hint={[f.hint, account && f.secret ? "Leave it empty to keep the current value." : ""].filter(Boolean).join(" ") || undefined} error={save.error?.fields[`credentials.${f.id}`] ?? (f.id === fields[fields.length - 1] ? save.error?.fields.credentials : undefined)}>
            {(p) => <input {...p} className="input" type={f.secret ? "password" : "text"} autoComplete="off" spellCheck={false} value={values[f.id] ?? ""} onChange={(e) => setValues({ ...values, [f.id]: e.target.value })} />}
          </Field>
        ))}
        <p className="muted small" style={{ marginTop: 14 }}>The plugin of {provider?.name ?? "the provider"} checks the credentials before the panel keeps them. The panel keeps them encrypted, and never shows a secret again.</p>
      </form>
    </Modal>
  );
}

/** The card of a zone at an external DNS provider: the state of the last change, and the name servers for the registrar. */
export function ProviderCard({ zone, onSync, syncing }: { zone: DnsZone; onSync: () => void; syncing: boolean }) {
  const p = zone.provider;
  if (!p) return null;
  const state = p.state === "ok" ? { tone: "success" as const, text: "Has the last change" } : p.state === "pending" ? { tone: "info" as const, text: "Waits for the last change" } : { tone: "danger" as const, text: "Could not take the last change" };
  return (
    <table className="kv kv-fixed">
      <tbody>
        <tr><th scope="row">Provider</th><td>{p.name}<span className="cell-sub">Account: {p.accountName}</span></td></tr>
        <tr>
          <th scope="row">Last change</th>
          <td>
            <Status tone={state.tone}>{state.text}</Status>
            {p.syncedAt && <span className="cell-sub">Sent {relativeTime(p.syncedAt)}{p.changes ? `: ${p.changes.added} added, ${p.changes.changed} changed, ${p.changes.removed} removed` : ""}</span>}
            {p.state === "failed" && p.error && <span className="cell-sub text-danger">{p.error}</span>}
            {p.state === "failed" && <span className="cell-sub"><button type="button" className="btn-link" disabled={syncing} onClick={onSync}>{syncing ? "Sending…" : "Send again now"}</button></span>}
          </td>
        </tr>
        <tr>
          <th scope="row">At {p.name}</th>
          <td>{p.active ? <Status tone="success">Active</Status> : <Status tone="warning">Waits for the registrar</Status>}</td>
        </tr>
        {zone.nameServers.length > 0
          ? zone.nameServers.map((n, i) => <tr key={n}><th scope="row">Name server {i + 1}</th><td><CopyField value={n} label={`Name server ${i + 1} of ${p.name}`} /></td></tr>)
          : <tr><th scope="row">Name servers</th><td className="muted">{p.name} gives the name servers after the first change.</td></tr>}
        {p.handover && (
          <tr>
            <th scope="row">Handover</th>
            <td className="muted small">
              The name servers of this panel still answer for the zone, with the name servers of {p.name}, so that the domain continues to work while the change goes through the DNS.
              {p.handover.endsAt ? ` They stop ${relativeTime(p.handover.endsAt)}.` : " They stop two days after the registrar has the new name servers."}
            </td>
          </tr>
        )}
        {p.imported && (
          <tr>
            <th scope="row">Copied</th>
            <td>
              <span className="small">{p.imported.added} {p.imported.added === 1 ? "record" : "records"} from {p.name}</span>
              {p.imported.skipped.length > 0 && (
                <details className="small" style={{ marginTop: 4 }}>
                  <summary>{p.imported.skipped.length} not copied</summary>
                  <ul style={{ margin: "6px 0 0", paddingLeft: 18 }}>
                    {p.imported.skipped.map((s) => <li key={`${s.line}-${s.text}`}><code>{s.text}</code> <span className="muted">{s.reason}</span></li>)}
                  </ul>
                </details>
              )}
            </td>
          </tr>
        )}
      </tbody>
    </table>
  );
}

/** Moves a zone to a different DNS provider, or back to the name servers of this panel. */
export function MoveDialog({ zone, onClose }: { zone: DnsZone; onClose: () => void }) {
  const state = useStore();
  const panel = state.nameServers.length > 0;
  const options = [
    ...(panel && zone.provider ? [{ id: "", label: `This panel (${state.nameServers.join(", ")})` }] : []),
    ...usableAccounts(state, zone.customerId).filter((a) => a.id !== zone.provider?.accountId).map((a) => ({ id: a.id, label: `${a.providerName}: ${a.name}` })),
  ];
  const [target, setTarget] = useState(options[0]?.id ?? "");
  const [importRecords, setImportRecords] = useState(true);
  const move = useAction(moveDnsZone);
  const account = state.dnsAccounts.find((a) => a.id === target);
  const to = account?.providerName ?? "the name servers of this panel";

  return (
    <Modal
      title={`Move ${zone.name} to a different DNS provider`}
      onClose={onClose}
      footer={<><button type="button" className="btn" onClick={onClose}>Cancel</button><button type="submit" form="move-zone" className="btn btn-primary" disabled={move.busy || options.length === 0 || zone.dnssec.enabled}>{move.busy ? "Moving…" : "Move zone"}</button></>}
    >
      <form id="move-zone" noValidate onSubmit={async (e) => {
        e.preventDefault();
        const r = await move.run(zone.id, target || null, importRecords);
        if (r.ok) onClose();
      }}>
        <FormError error={move.error} shown={["accountId"]} />
        {zone.dnssec.enabled && (
          <div className="alert alert-warning" role="status" style={{ marginBottom: 14 }}>
            <p><strong>Turn off DNSSEC first.</strong> The new provider signs the zone with a different key. First remove the DS record at the registrar, wait one day, and then turn off DNSSEC.</p>
          </div>
        )}
        {options.length === 0 ? (
          <p className="muted">There is no other DNS provider for this zone. Add an account at an external DNS provider on the DNS page first.</p>
        ) : (
          <>
            <Field label="Move to" error={move.error?.fields.accountId}>
              {(p) => (
                <select {...p} className="select" value={target} onChange={(e) => setTarget(e.target.value)}>
                  {options.map((o) => <option key={o.id} value={o.id}>{o.label}</option>)}
                </select>
              )}
            </Field>
            {account && (
              <label className="check" style={{ marginTop: 10 }}><input type="checkbox" checked={importRecords} onChange={(e) => setImportRecords(e.target.checked)} />Also keep the records that {account.providerName} has for {zone.name} now</label>
            )}
            <p className="muted small" style={{ marginTop: 14 }}>
              The records stay. After the move, give the name servers of {to} to the registrar of {zone.name}.{" "}
              {!zone.provider && account
                ? "The name servers of this panel continue to answer for the zone until two days after the registrar has the new name servers."
                : zone.provider ? `The zone stays at ${zone.provider.name} with its current records. Remove it there after the registrar has the new name servers.` : ""}
            </p>
          </>
        )}
      </form>
    </Modal>
  );
}
