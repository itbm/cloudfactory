import { useState } from "react";
import { Link } from "react-router-dom";
import { useAction, useStore } from "../api/hooks";
import { addAlias, addMailbox, removeAlias, removeMailbox, setMailboxQuota } from "../api/store";
import type { MailAlias } from "../api/types";
import { Card, ConfirmButton, Field, FormError, Meter, Modal } from "../components/ui";
import { bytes } from "../lib/format";

/** Mailboxes, aliases and forwarders (FR-601, FR-603). With domains, it shows those domains only. */
export function MailPanel({ domains }: { domains?: string[] }) {
  const state = useStore();
  const inScope = (address: string) => !domains || domains.includes(address.split("@")[1]);
  const mailboxes = state.mailboxes.filter((m) => inScope(m.address));
  const aliases = state.aliases.filter((a) => inScope(a.source));
  const [dialog, setDialog] = useState<null | "mailbox" | MailAlias["kind"]>(null);
  const [quota, setQuota] = useState<{ id: string; gb: string } | null>(null);
  const [address, setAddress] = useState(domains ? `@${domains[0]}` : "");
  const [size, setSize] = useState("5");
  const [dest, setDest] = useState("");
  const mbx = useAction(addMailbox);
  const als = useAction(addAlias);
  const setQ = useAction(setMailboxQuota);
  const allDomains = Array.from(new Set(state.websites.flatMap((w) => w.domains.map((d) => d.name.replace(/^www\./, "")))));

  const close = () => {
    setDialog(null);
    setAddress(domains ? `@${domains[0]}` : "");
    setDest("");
    mbx.clear();
    als.clear();
  };

  return (
    <div className="stack">
      <Card title="Mailboxes" actions={<button type="button" className="btn btn-primary" onClick={() => setDialog("mailbox")}>New mailbox</button>}>
        <div className="table-wrap">
          <table>
            <thead><tr><th scope="col">Address</th><th scope="col">Use</th><th scope="col">Access</th><th scope="col"><span className="visually-hidden">Actions</span></th></tr></thead>
            <tbody>
              {mailboxes.map((m) => {
                const pct = Math.round((m.usedBytes / m.quotaBytes) * 100);
                return (
                  <tr key={m.id}>
                    <td><strong>{m.address}</strong></td>
                    <td style={{ minWidth: 200 }}>
                      <div className="row" style={{ display: "flex", justifyContent: "space-between", marginBottom: 6 }}>
                        <span>{bytes(m.usedBytes)} of {bytes(m.quotaBytes)}</span>
                        <span className={pct >= 90 ? "text-warning" : "muted"}>{pct}%{pct >= 90 ? " · almost full" : ""}</span>
                      </div>
                      <Meter value={pct / 100} label={`${m.address}: ${pct}% full`} tone={pct >= 90 ? "warning" : undefined} />
                    </td>
                    <td className="small muted">IMAP, JMAP and SMTP on <code>mail-01.cloudfactory.io</code></td>
                    <td>
                      <div className="btn-group" style={{ justifyContent: "flex-end" }}>
                        <button type="button" className="btn btn-sm" onClick={() => setQuota({ id: m.id, gb: String(Math.round(m.quotaBytes / 1024 ** 3)) })}>Change size</button>
                        <ConfirmButton className="btn btn-sm btn-danger" title={`Remove ${m.address}?`} confirm="Remove mailbox" message={<p>All mail in the mailbox is removed. You can restore it from a backup for the retention period.</p>} onConfirm={() => removeMailbox(m.id)}>Remove</ConfirmButton>
                      </div>
                    </td>
                  </tr>
                );
              })}
              {mailboxes.length === 0 && <tr><td colSpan={4} className="muted">No mailboxes.</td></tr>}
            </tbody>
          </table>
        </div>
      </Card>
      <Card
        title="Aliases and forwarders"
        actions={
          <div className="btn-group">
            <button type="button" className="btn" onClick={() => setDialog("alias")}>New alias</button>
            <button type="button" className="btn" onClick={() => setDialog("forwarder")}>New forwarder</button>
          </div>
        }
      >
        <div className="table-wrap">
          <table>
            <thead><tr><th scope="col">Address</th><th scope="col">Type</th><th scope="col">Goes to</th><th scope="col"><span className="visually-hidden">Actions</span></th></tr></thead>
            <tbody>
              {aliases.map((a) => (
                <tr key={a.id}>
                  <td><strong>{a.source}</strong></td>
                  <td>{a.kind === "alias" ? "Alias" : "Forwarder"}</td>
                  <td>{a.destinations.join(", ")}</td>
                  <td className="cell-actions" style={{ width: 120 }}>
                    <ConfirmButton className="btn btn-sm btn-danger" title={`Remove ${a.source}?`} confirm="Remove" message={<p>Mail to this address is rejected after you remove it.</p>} onConfirm={() => removeAlias(a.id)}>Remove</ConfirmButton>
                  </td>
                </tr>
              ))}
              {aliases.length === 0 && <tr><td colSpan={4} className="muted">No aliases or forwarders.</td></tr>}
            </tbody>
          </table>
        </div>
        <p className="card-body muted small" style={{ paddingTop: 12 }}>
          When the panel controls the DNS zone, it makes the SPF, DKIM and DMARC records. <Link to="/dns">See DNS zones</Link>.
        </p>
      </Card>

      {dialog === "mailbox" && (
        <Modal title="New mailbox" onClose={close} footer={<><button type="button" className="btn" onClick={close}>Cancel</button><button type="submit" form="mbx" className="btn btn-primary">Create mailbox</button></>}>
          <form id="mbx" noValidate onSubmit={async (e) => { e.preventDefault(); if ((await mbx.run(address, Number(size))).ok) close(); }}>
            <FormError error={mbx.error} />
            <Field label="Address" error={mbx.error?.fields.address} hint={`Domains: ${allDomains.slice(0, 4).join(", ")}…`}>
              {(p) => <input {...p} className="input" type="email" value={address} onChange={(e) => setAddress(e.target.value)} placeholder="hello@example.com" autoCapitalize="none" />}
            </Field>
            <Field label="Size in GB" error={mbx.error?.fields.quota}>
              {(p) => <input {...p} className="input" type="number" min={1} max={100} value={size} onChange={(e) => setSize(e.target.value)} />}
            </Field>
          </form>
        </Modal>
      )}
      {(dialog === "alias" || dialog === "forwarder") && (
        <Modal title={dialog === "alias" ? "New alias" : "New forwarder"} onClose={close} footer={<><button type="button" className="btn" onClick={close}>Cancel</button><button type="submit" form="als" className="btn btn-primary">Create</button></>}>
          <form id="als" noValidate onSubmit={async (e) => { e.preventDefault(); if ((await als.run(dialog, address, dest.split(","))).ok) close(); }}>
            <FormError error={als.error} />
            <p className="muted">{dialog === "alias" ? "An alias sends mail to a mailbox on this panel." : "A forwarder sends a copy of each message to other addresses."}</p>
            <Field label="Address" error={als.error?.fields.source}>
              {(p) => <input {...p} className="input" type="email" value={address} onChange={(e) => setAddress(e.target.value)} placeholder="info@example.com" autoCapitalize="none" />}
            </Field>
            <Field label="Goes to" hint="One or more addresses, with a comma between them." error={als.error?.fields.destinations}>
              {(p) => <input {...p} className="input" value={dest} onChange={(e) => setDest(e.target.value)} placeholder="hello@example.com" autoCapitalize="none" />}
            </Field>
          </form>
        </Modal>
      )}
      {quota && (
        <Modal title="Change mailbox size" onClose={() => { setQuota(null); setQ.clear(); }} footer={<><button type="button" className="btn" onClick={() => setQuota(null)}>Cancel</button><button type="submit" form="quota" className="btn btn-primary">Save</button></>}>
          <form id="quota" noValidate onSubmit={async (e) => { e.preventDefault(); if ((await setQ.run(quota.id, Number(quota.gb))).ok) setQuota(null); }}>
            <FormError error={setQ.error} />
            <Field label="Size in GB">
              {(p) => <input {...p} className="input" type="number" min={1} max={100} value={quota.gb} onChange={(e) => setQuota({ ...quota, gb: e.target.value })} />}
            </Field>
          </form>
        </Modal>
      )}
    </div>
  );
}

