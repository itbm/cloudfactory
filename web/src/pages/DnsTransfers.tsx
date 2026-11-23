import { useState } from "react";
import { useAction, useStore } from "../api/hooks";
import { deleteDnsSecondary, dnsSecondarySecret, dnsTransfersSecret, saveDnsSecondary, setDnsTransfers, stopDnsTransfers } from "../api/store";
import type { DnsSecondary, DnsZone, TsigAlgorithm, TsigKey } from "../api/types";
import { Card, Field, FormError, Modal, RowMenu, Status, TableWrap } from "../components/ui";
import { CopyField } from "./website/AccessTab";

/**
 * Secondary DNS: DNS servers outside the panel copy the zones from the DNS servers of the panel
 * with zone transfers. The panel never copies a zone from a different server.
 *
 * - The secondaries of the panel copy all zones. Only an administrator sees and changes them.
 * - The owner of a zone can send the zone to its own secondaries too.
 */

const ALGORITHMS: { value: TsigAlgorithm; label: string }[] = [
  { value: "hmac-sha256", label: "HMAC-SHA256" },
  { value: "hmac-sha512", label: "HMAC-SHA512" },
];

/** The addresses in a text box: one on each line, or with commas or spaces between them. */
const addressList = (text: string) => text.split(/[\s,]+/).map((a) => a.trim()).filter(Boolean);

const LIST_STATE = {
  ok: { tone: "success" as const, label: "Has the list" },
  pending: { tone: "info" as const, label: "Waits for the list" },
  failed: { tone: "danger" as const, label: "Could not take the list" },
};

/** The card with the secondary DNS servers of the panel, on the list of zones. Only for an administrator. */
export function SecondariesCard() {
  const state = useStore();
  const list = state.dnsSecondaries;
  const [editing, setEditing] = useState<DnsSecondary | "new" | null>(null);
  const [removing, setRemoving] = useState<DnsSecondary | null>(null);
  const [showing, setShowing] = useState<DnsSecondary | null>(null);
  const remove = useAction(deleteDnsSecondary);
  if (state.session.user.role !== "administrator" || !list) return null;
  const catalog = list.data.some((s) => s.catalog);

  return (
    <Card title="Secondary DNS servers" actions={<button type="button" className="btn btn-sm" onClick={() => setEditing("new")}>Add secondary</button>}>
      <div className="card-body" style={{ paddingBottom: 12 }}>
        <p className="muted">
          A secondary DNS server outside this panel, for example the secondary DNS service of a different company, copies all zones from the DNS servers of the panel with zone transfers (AXFR and IXFR). A TSIG key signs the transfers. The panel never copies a zone from a different server.
        </p>
        {list.primaries.length > 0 && (
          <p className="muted" style={{ marginTop: 10 }}>
            Configure each secondary to copy the zones from {list.primaries.length === 1 ? "this server" : "these servers"}:{" "}
            {list.primaries.map((p, i) => <span key={p.nameServer}>{i > 0 && "; "}<code>{p.nameServer}</code> ({p.addresses.join(", ") || "no address yet"})</span>)}.
          </p>
        )}
        {catalog && <p className="muted" style={{ marginTop: 10 }}>The catalog zone <code>{list.catalogZone}</code> lists all zones (RFC 9432). A secondary that copies it finds each new zone itself.</p>}
        <FormError error={remove.error} />
      </div>
      {list.data.length > 0 && (
        <TableWrap>
          <table>
            <thead><tr><th scope="col">Secondary</th><th scope="col">Addresses</th><th scope="col">Name server</th><th scope="col">TSIG key</th><th scope="col">Copies</th><th scope="col"><span className="visually-hidden">Actions</span></th></tr></thead>
            <tbody>
              {list.data.map((s) => (
                <tr key={s.id}>
                  <td><span className="cell-title">{s.name}</span>{s.notify && <span className="cell-sub">Gets NOTIFY</span>}</td>
                  <td>{s.addresses.map((a) => <code key={a} style={{ display: "block" }}>{a}</code>)}</td>
                  <td>{s.nameServer ? <code>{s.nameServer}</code> : <span className="muted">Not a name server</span>}</td>
                  <td>{s.tsig ? <><code>{s.tsig.name}</code><span className="cell-sub">{s.tsig.algorithm}</span></> : <Status tone="warning">No key</Status>}</td>
                  <td>{s.catalog ? "All zones and the catalog zone" : "All zones"}</td>
                  <td className="cell-actions">
                    <RowMenu label={`Actions for the secondary DNS server ${s.name}`} actions={[
                      { label: "Change", onSelect: () => setEditing(s) },
                      ...(s.tsig ? [{ label: "Show the TSIG key", onSelect: () => setShowing(s) }] : []),
                      { label: "Remove", danger: true, onSelect: () => setRemoving(s) },
                    ]} />
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </TableWrap>
      )}
      {list.data.length > 0 && list.servers.length > 0 && (
        <p className="card-body muted small" style={{ paddingTop: 12 }}>
          {list.servers.map((s, i) => <span key={s.server}>{i > 0 && " · "}<code>{s.server}</code> <Status tone={LIST_STATE[s.state].tone}>{LIST_STATE[s.state].label}</Status></span>)}
        </p>
      )}
      {editing && <SecondaryDialog secondary={editing === "new" ? undefined : editing} onClose={() => setEditing(null)} />}
      {showing && <KeyDialog title={`The TSIG key of ${showing.name}`} load={() => dnsSecondarySecret(showing.id)} onClose={() => setShowing(null)} />}
      {removing && (
        <Modal
          title={`Remove the secondary DNS server ${removing.name}?`}
          onClose={() => setRemoving(null)}
          footer={<><button type="button" className="btn" onClick={() => setRemoving(null)}>Cancel</button><button type="button" className="btn btn-danger-solid" onClick={() => { const s = removing; setRemoving(null); void remove.run(s.id); }}>Remove secondary</button></>}
        >
          <p>The DNS servers of the panel stop the transfers to {removing.name}.</p>
          {removing.nameServer && <p style={{ marginTop: 10 }}><strong>First remove <code>{removing.nameServer}</code> at the registrar of each domain.</strong> The zones lose the NS record of the secondary, and a resolver that asks {removing.nameServer} gets old records or no answer.</p>}
        </Modal>
      )}
    </Card>
  );
}

/** Adds a secondary DNS server of the panel, or changes one. */
function SecondaryDialog({ secondary, onClose }: { secondary?: DnsSecondary; onClose: () => void }) {
  const [name, setName] = useState(secondary?.name ?? "");
  const [addresses, setAddresses] = useState(secondary?.addresses.join("\n") ?? "");
  const [nameServer, setNameServer] = useState(secondary?.nameServer ?? "");
  const [tsig, setTsig] = useState(secondary ? !!secondary.tsig : true);
  const [algorithm, setAlgorithm] = useState<TsigAlgorithm>(secondary?.tsig?.algorithm ?? "hmac-sha256");
  const [keyName, setKeyName] = useState(secondary?.tsig?.name ?? "");
  const [secret, setSecret] = useState("");
  const [newSecret, setNewSecret] = useState(false);
  const [notify, setNotify] = useState(secondary?.notify ?? true);
  const [catalog, setCatalog] = useState(secondary?.catalog ?? false);
  const save = useAction(saveDnsSecondary);
  const e = save.error?.fields ?? {};

  return (
    <Modal
      title={secondary ? `Change the secondary DNS server ${secondary.name}` : "Add a secondary DNS server"}
      onClose={onClose}
      footer={<><button type="button" className="btn" onClick={onClose}>Cancel</button><button type="submit" form="dns-secondary" className="btn btn-primary" disabled={save.busy}>{save.busy ? "Saving…" : secondary ? "Save" : "Add secondary"}</button></>}
    >
      <form id="dns-secondary" noValidate onSubmit={async (ev) => {
        ev.preventDefault();
        const r = await save.run({ name, addresses: addressList(addresses), nameServer, tsig, algorithm, keyName, secret, newSecret, notify, catalog }, secondary?.id);
        if (r.ok) onClose();
      }}>
        <FormError error={save.error} shown={["name", "addresses", "nameServer", "keyName", "algorithm", "secret"]} />
        <Field label="Name" hint="A name for you, for example Backup DNS service." error={e.name}>
          {(p) => <input {...p} className="input" value={name} maxLength={60} onChange={(ev) => setName(ev.target.value)} />}
        </Field>
        <Field label="Addresses" hint="The IP addresses of the secondary, one on each line. Only these addresses can copy the zones." error={e.addresses}>
          {(p) => <textarea {...p} className="textarea" rows={3} value={addresses} spellCheck={false} onChange={(ev) => setAddresses(ev.target.value)} />}
        </Field>
        <Field label="Name server (optional)" hint="The host name of the secondary, for example ns3.example.net. Each zone gets an NS record for it. Leave it empty for a secondary that does not answer the public." error={e.nameServer}>
          {(p) => <input {...p} className="input" value={nameServer} spellCheck={false} onChange={(ev) => setNameServer(ev.target.value)} />}
        </Field>
        <label className="check" style={{ marginTop: 10 }}><input type="checkbox" checked={notify} onChange={(ev) => setNotify(ev.target.checked)} />Send NOTIFY after each change, so that the secondary copies it at once</label>
        <label className="check"><input type="checkbox" checked={catalog} onChange={(ev) => setCatalog(ev.target.checked)} />Also copy the catalog zone, so that the secondary finds each new zone itself</label>
        {catalog !== (secondary?.catalog ?? false) && <p className="muted small">When the catalog zone starts or stops, each DNS server of the panel restarts. It does not answer for some seconds.</p>}
        <label className="check"><input type="checkbox" checked={tsig} onChange={(ev) => setTsig(ev.target.checked)} />Sign the transfers with a TSIG key</label>
        {tsig ? (
          <>
            <Field label="Algorithm" error={e.algorithm}>
              {(p) => <select {...p} className="select" value={algorithm} onChange={(ev) => setAlgorithm(ev.target.value as TsigAlgorithm)}>{ALGORITHMS.map((a) => <option key={a.value} value={a.value}>{a.label}</option>)}</select>}
            </Field>
            <Field label="Key name (optional)" hint="Leave it empty and the panel makes a name. The secondary must use the same name." error={e.keyName}>
              {(p) => <input {...p} className="input" value={keyName} spellCheck={false} onChange={(ev) => setKeyName(ev.target.value)} />}
            </Field>
            <Field label="Secret (optional)" hint={secondary?.tsig ? "Leave it empty to keep the current secret. Enter a secret in base64 if the secondary DNS service gives you one." : "Leave it empty and the panel makes a secret. Enter a secret in base64 if the secondary DNS service gives you one."} error={e.secret}>
              {(p) => <input {...p} className="input" type="password" autoComplete="off" spellCheck={false} value={secret} onChange={(ev) => setSecret(ev.target.value)} />}
            </Field>
            {secondary?.tsig && <label className="check"><input type="checkbox" checked={newSecret} onChange={(ev) => setNewSecret(ev.target.checked)} />Make a new secret</label>}
          </>
        ) : (
          <div className="alert alert-warning" role="status" style={{ marginTop: 10 }}><p>With no TSIG key, the DNS servers check only the address of the secondary. Use this only in a private network.</p></div>
        )}
      </form>
    </Modal>
  );
}

/** Shows a TSIG key with its secret, and the same key in the format of BIND and Knot DNS. */
function KeyDialog({ title, load, onClose }: { title: string; load: () => Promise<Required<TsigKey>>; onClose: () => void }) {
  const [key, setKey] = useState<Required<TsigKey> | null>(null);
  const read = useAction(async () => setKey(await load()));
  return (
    <Modal title={title} onClose={onClose} footer={<button type="button" className="btn" onClick={onClose}>Close</button>}>
      <FormError error={read.error} />
      {key ? (
        <>
          <div className="copy-fields copy-wrap">
            {([["Key name", key.name], ["Algorithm", key.algorithm], ["Secret", key.secret], ["Key for BIND", `key "${key.name}" { algorithm ${key.algorithm}; secret "${key.secret}"; };`]] as const).map(([label, value]) => (
              <div key={label}>
                <div className="label" aria-hidden="true">{label}</div>
                <CopyField value={value} label={label} />
              </div>
            ))}
          </div>
        </>
      ) : (
        <>
          <p>The secret lets a server copy the zones. Keep it secret. The audit log records that you looked at it.</p>
          <p style={{ marginTop: 14 }}><button type="button" className="btn btn-primary" disabled={read.busy} onClick={() => void read.run()}>{read.busy ? "Loading…" : "Show the secret"}</button></p>
        </>
      )}
    </Modal>
  );
}

/** The card of the zone transfers of one zone to the secondary DNS servers of its owner. */
export function TransfersCard({ zone, locked }: { zone: DnsZone; locked: boolean }) {
  const t = zone.transfers;
  const [editing, setEditing] = useState(false);
  const [stopping, setStopping] = useState(false);
  const [showing, setShowing] = useState(false);
  const stop = useAction(stopDnsTransfers);
  if (!t) return null;
  const on = t.addresses.length > 0;

  return (
    <Card
      className="mt"
      title="Zone transfers"
      actions={!locked && <button type="button" className="btn btn-sm" onClick={() => setEditing(true)}>{on ? "Change" : "Set up"}</button>}
    >
      <div className="card-body">
        <FormError error={stop.error} />
        {on ? (
          <table className="kv">
            <tbody>
              <tr><th scope="row">Secondaries</th><td>{t.addresses.map((a) => <code key={a} style={{ display: "block" }}>{a}</code>)}<span className="cell-sub">{t.notify ? "Get NOTIFY after each change" : "Get no NOTIFY. They check the zone at its refresh time."}</span></td></tr>
              <tr><th scope="row">TSIG key</th><td>{t.tsig ? <><code>{t.tsig.name}</code> <span className="muted">{t.tsig.algorithm}</span> <button type="button" className="btn-link" onClick={() => setShowing(true)}>Show the secret</button></> : <Status tone="warning">No key</Status>}</td></tr>
              {t.nameServers.length > 0 && <tr><th scope="row">Name servers</th><td>{t.nameServers.map((n) => <code key={n} style={{ display: "block" }}>{n}</code>)}<span className="cell-sub">Give them to the registrar too.</span></td></tr>}
              <tr><th scope="row">Copy from</th><td>{t.primaries.length > 0 ? t.primaries.map((a) => <code key={a} style={{ display: "block" }}>{a}</code>) : <span className="muted">The DNS servers of the panel have no address yet.</span>}</td></tr>
              {!locked && <tr><th scope="row">Stop</th><td><button type="button" className="btn btn-sm btn-danger" disabled={stop.busy} onClick={() => setStopping(true)}>Stop the transfers</button></td></tr>}
            </tbody>
          </table>
        ) : (
          <p className="muted">Your own secondary DNS servers can copy this zone from the DNS servers of the panel with zone transfers (AXFR and IXFR). Then they also answer for the domain.</p>
        )}
      </div>
      {editing && <TransfersDialog zone={zone} onClose={() => setEditing(false)} />}
      {showing && <KeyDialog title={`The TSIG key of ${zone.name}`} load={() => dnsTransfersSecret(zone.id)} onClose={() => setShowing(false)} />}
      {stopping && (
        <Modal
          title={`Stop the zone transfers of ${zone.name}?`}
          onClose={() => setStopping(false)}
          footer={<><button type="button" className="btn" onClick={() => setStopping(false)}>Cancel</button><button type="button" className="btn btn-danger-solid" onClick={() => { setStopping(false); void stop.run(zone.id); }}>Stop the transfers</button></>}
        >
          <p>Your secondaries cannot copy the zone again. They keep their copy until it expires.</p>
          {t.nameServers.length > 0 && <p style={{ marginTop: 10 }}><strong>First remove {t.nameServers.join(", ")} at the registrar.</strong> The zone loses the NS records of your secondaries.</p>}
        </Modal>
      )}
    </Card>
  );
}

/** Sets the secondary DNS servers of the owner of a zone. */
function TransfersDialog({ zone, onClose }: { zone: DnsZone; onClose: () => void }) {
  const t = zone.transfers;
  const on = !!t && t.addresses.length > 0;
  const [addresses, setAddresses] = useState(t?.addresses.join("\n") ?? "");
  const [nameServers, setNameServers] = useState(t?.nameServers.join("\n") ?? "");
  const [notify, setNotify] = useState(on ? t.notify : true);
  const [tsig, setTsig] = useState(on ? !!t.tsig : true);
  const [algorithm, setAlgorithm] = useState<TsigAlgorithm>(t?.tsig?.algorithm ?? "hmac-sha256");
  const [secret, setSecret] = useState("");
  const [newSecret, setNewSecret] = useState(false);
  const save = useAction(setDnsTransfers);
  const e = save.error?.fields ?? {};
  const nameServerError = Object.entries(e).find(([k]) => k.startsWith("nameServers"))?.[1];

  return (
    <Modal
      title={`Zone transfers of ${zone.name}`}
      onClose={onClose}
      footer={<><button type="button" className="btn" onClick={onClose}>Cancel</button><button type="submit" form="dns-transfers" className="btn btn-primary" disabled={save.busy}>{save.busy ? "Saving…" : "Save"}</button></>}
    >
      <form id="dns-transfers" noValidate onSubmit={async (ev) => {
        ev.preventDefault();
        const r = await save.run(zone.id, { addresses: addressList(addresses), notify, nameServers: addressList(nameServers), tsig, algorithm, secret, newSecret });
        if (r.ok) onClose();
      }}>
        <FormError error={save.error} shown={["addresses", "nameServers", "algorithm", "secret", ...Object.keys(e).filter((k) => k.startsWith("nameServers."))]} />
        <Field label="Addresses of your secondary DNS servers" hint="Public IP addresses, one on each line. Only these addresses can copy the zone." error={e.addresses}>
          {(p) => <textarea {...p} className="textarea" rows={3} value={addresses} spellCheck={false} onChange={(ev) => setAddresses(ev.target.value)} />}
        </Field>
        <Field label="Host names of your secondaries (optional)" hint="One on each line, for example ns3.example.net. The zone gets an NS record for each one." error={nameServerError}>
          {(p) => <textarea {...p} className="textarea" rows={2} value={nameServers} spellCheck={false} onChange={(ev) => setNameServers(ev.target.value)} />}
        </Field>
        <label className="check" style={{ marginTop: 10 }}><input type="checkbox" checked={notify} onChange={(ev) => setNotify(ev.target.checked)} />Send NOTIFY after each change</label>
        <label className="check"><input type="checkbox" checked={tsig} onChange={(ev) => setTsig(ev.target.checked)} />Sign the transfers with a TSIG key</label>
        {tsig ? (
          <>
            <Field label="Algorithm" error={e.algorithm}>
              {(p) => <select {...p} className="select" value={algorithm} onChange={(ev) => setAlgorithm(ev.target.value as TsigAlgorithm)}>{ALGORITHMS.map((a) => <option key={a.value} value={a.value}>{a.label}</option>)}</select>}
            </Field>
            <Field label="Secret (optional)" hint={`The key name is transfer.${zone.name}. ${t?.tsig ? "Leave the secret empty to keep the current secret." : "Leave the secret empty and the panel makes one."}`} error={e.secret}>
              {(p) => <input {...p} className="input" type="password" autoComplete="off" spellCheck={false} value={secret} onChange={(ev) => setSecret(ev.target.value)} />}
            </Field>
            {t?.tsig && <label className="check"><input type="checkbox" checked={newSecret} onChange={(ev) => setNewSecret(ev.target.checked)} />Make a new secret</label>}
          </>
        ) : (
          <div className="alert alert-warning" role="status" style={{ marginTop: 10 }}><p>With no TSIG key, the DNS servers check only the address of each secondary.</p></div>
        )}
        {t && t.primaries.length > 0 && <p className="muted small" style={{ marginTop: 14 }}>Configure your secondaries to copy the zone from {t.primaries.join(", ")}.</p>}
      </form>
    </Modal>
  );
}
