import { useState } from "react";
import { Link, useParams } from "react-router-dom";
import { useAction, useStore } from "../api/hooks";
import { addMailRecords, addZone, deleteRecord, saveRecord } from "../api/store";
import type { DnsRecord, DnsRecordType, DnsZone } from "../api/types";
import { Topbar } from "../components/Layout";
import { Card, ConfirmButton, Empty, Field, FormError, Modal, PageHeader, Status, TerraformBadge, TerraformNotice } from "../components/ui";
import { RECORD_TYPES } from "../lib/dns";

export function DnsPage() {
  const { zone } = useParams();
  return zone ? <ZonePage name={zone} /> : <ZoneList />;
}

function ZoneList() {
  const state = useStore();
  const [open, setOpen] = useState(false);
  const [name, setName] = useState("");
  const add = useAction(addZone);
  const zones = state.zones.filter((z) => state.session.contextId === "platform" || z.customerId === state.session.contextId);
  return (
    <>
      <Topbar />
      <main id="main" className="content">
        <PageHeader
          title="Domains and DNS"
          description="Authoritative DNS on two servers: dns-01 (London) and dns-02 (Amsterdam). The panel checks each record before it saves it."
          actions={<button type="button" className="btn btn-primary" onClick={() => setOpen(true)}>Add DNS zone</button>}
        />
        <Card title="DNS zones">
          <div className="table-wrap">
            <table>
              <thead><tr><th scope="col">Zone</th><th scope="col">Records</th><th scope="col">Mail records</th><th scope="col">Name servers</th><th scope="col">Websites</th></tr></thead>
              <tbody>
                {zones.map((z) => {
                  const sites = state.websites.filter((w) => w.domains.some((d) => d.name === z.name || d.name.endsWith(`.${z.name}`)));
                  const mail = z.records.some((r) => r.managed === "mail");
                  return (
                    <tr key={z.name}>
                      <td><Link className="cell-title" to={`/dns/${z.name}`}>{z.name}</Link> {z.managedByTerraform && <TerraformBadge />}</td>
                      <td>{z.records.length}</td>
                      <td>{mail ? <Status tone="success">SPF, DKIM, DMARC</Status> : <Status tone="neutral">None</Status>}</td>
                      <td className="small">{z.nameservers.join(", ")}</td>
                      <td>{sites.length}</td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </div>
        </Card>
        <Card title="Domains on websites">
          <div className="table-wrap">
            <table>
              <thead><tr><th scope="col">Domain</th><th scope="col">Website</th><th scope="col">Type</th><th scope="col">DNS</th></tr></thead>
              <tbody>
                {state.websites.flatMap((w) => w.domains.map((d) => (
                  <tr key={`${w.id}-${d.name}`}>
                    <td>{d.name}</td>
                    <td><Link to={`/websites/${w.id}/domains`}>{w.name}</Link></td>
                    <td>{d.kind === "primary" ? "Primary" : d.kind === "redirect" ? "Redirect" : "Alias"}</td>
                    <td>{d.dnsPointsHere ? <Status tone="success">Points here</Status> : <Status tone="warning">Does not point here</Status>}</td>
                  </tr>
                )))}
              </tbody>
            </table>
          </div>
        </Card>
      </main>
      {open && (
        <Modal title="Add DNS zone" onClose={() => { setOpen(false); add.clear(); }} footer={<><button type="button" className="btn" onClick={() => setOpen(false)}>Cancel</button><button type="submit" form="zone" className="btn btn-primary">Add zone</button></>}>
          <form id="zone" noValidate onSubmit={async (e) => { e.preventDefault(); if ((await add.run(name)).ok) { setOpen(false); setName(""); } }}>
            <FormError error={add.error} />
            <Field label="Domain" error={add.error?.fields.name} hint="Then set the name servers at your domain registrar to dns-01.cloudfactory.io and dns-02.cloudfactory.io.">
              {(p) => <input {...p} className="input" value={name} onChange={(e) => setName(e.target.value)} placeholder="example.com" autoCapitalize="none" />}
            </Field>
          </form>
        </Modal>
      )}
    </>
  );
}

const MANAGED_LABEL = { mail: "Mail", website: "Website", ns: "Name server" } as const;

function ZonePage({ name }: { name: string }) {
  const state = useStore();
  const zone = state.zones.find((z) => z.name === name);
  const [editing, setEditing] = useState<DnsRecord | "new" | null>(null);
  const [type, setType] = useState<"all" | DnsRecordType>("all");
  if (!zone) {
    return (
      <>
        <Topbar />
        <main id="main" className="content"><Empty title="This zone does not exist"><p><Link to="/dns">Go to all zones</Link></p></Empty></main>
      </>
    );
  }
  const records = zone.records.filter((r) => type === "all" || r.type === type);
  const hasMail = zone.records.some((r) => r.managed === "mail");
  return (
    <>
      <Topbar />
      <main id="main" className="content">
        <ol className="breadcrumbs"><li><Link to="/dns">Domains and DNS</Link></li><li aria-current="page">{zone.name}</li></ol>
        <PageHeader
          title={<>{zone.name} {zone.managedByTerraform && <TerraformBadge />}</>}
          description={`Name servers: ${zone.nameservers.join(", ")}. Changes go to both servers.`}
          actions={
            <>
              {!hasMail && <button type="button" className="btn" onClick={() => addMailRecords(zone.name)}>Add mail records</button>}
              <button type="button" className="btn btn-primary" onClick={() => setEditing("new")}>Add record</button>
            </>
          }
        />
        {zone.managedByTerraform && <div style={{ marginBottom: 20 }}><TerraformNotice resource="this DNS zone" /></div>}
        <Card
          title="Records"
          actions={
            <div className="filters" role="group" aria-label="Filter by type">
              {(["all", ...RECORD_TYPES] as const).filter((t) => t === "all" || zone.records.some((r) => r.type === t)).map((t) => (
                <button key={t} type="button" className="chip" aria-pressed={type === t} onClick={() => setType(t)}>{t === "all" ? "All" : t}</button>
              ))}
            </div>
          }
        >
          <div className="table-wrap">
            <table>
              <thead><tr><th scope="col">Name</th><th scope="col">Type</th><th scope="col">Value</th><th scope="col">TTL</th><th scope="col">Made by</th><th scope="col"><span className="visually-hidden">Actions</span></th></tr></thead>
              <tbody>
                {records.map((r) => (
                  <tr key={r.id}>
                    <td><code>{r.name}</code></td>
                    <td><span className="tag">{r.type}</span></td>
                    <td style={{ maxWidth: 420, wordBreak: "break-all" }}><code>{r.priority !== undefined ? `${r.priority} ` : ""}{r.value}</code></td>
                    <td>{r.ttl}</td>
                    <td className="small muted">{r.managed ? `Panel (${MANAGED_LABEL[r.managed]})` : "You"}</td>
                    <td>
                      <div className="btn-group" style={{ justifyContent: "flex-end" }}>
                        <button type="button" className="btn btn-sm" onClick={() => setEditing(r)}>Edit<span className="visually-hidden"> {r.type} {r.name}</span></button>
                        {r.managed !== "ns" && (
                          <ConfirmButton className="btn btn-sm btn-danger" title={`Remove ${r.type} record ${r.name}?`} confirm="Remove record" message={<p>{r.managed ? "The panel made this record. If you remove it, mail or the website can stop working." : "The record is removed from both DNS servers."}</p>} onConfirm={() => deleteRecord(zone.name, r.id)}>
                            Remove
                          </ConfirmButton>
                        )}
                      </div>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </Card>
      </main>
      {editing && <RecordDialog zone={zone} record={editing === "new" ? undefined : editing} onClose={() => setEditing(null)} />}
    </>
  );
}

function RecordDialog({ zone, record, onClose }: { zone: DnsZone; record?: DnsRecord; onClose: () => void }) {
  const [form, setForm] = useState({ type: record?.type ?? ("A" as DnsRecordType), name: record?.name ?? "", value: record?.value ?? "", ttl: String(record?.ttl ?? 3600), priority: String(record?.priority ?? 10) });
  const save = useAction(saveRecord);
  const err = save.error?.fields ?? {};
  const needsPriority = form.type === "MX" || form.type === "SRV";
  return (
    <Modal
      title={record ? `Edit ${record.type} record` : "Add record"}
      onClose={onClose}
      footer={<><button type="button" className="btn" onClick={onClose}>Cancel</button><button type="submit" form="rec" className="btn btn-primary">Save record</button></>}
    >
      <form
        id="rec"
        noValidate
        onSubmit={async (e) => {
          e.preventDefault();
          const result = await save.run(zone.name, { type: form.type, name: form.name, value: form.value, ttl: Number(form.ttl), priority: needsPriority ? Number(form.priority) : undefined }, record?.id);
          if (result.ok) onClose();
        }}
      >
        <FormError error={save.error} />
        {zone.managedByTerraform && <TerraformNotice resource="this zone" />}
        {record?.managed && <div className="alert alert-warning"><p>The panel made this record. If you change it, mail or the website can stop working.</p></div>}
        <div className="form-row">
          <Field label="Type">
            {(p) => <select {...p} className="select" value={form.type} disabled={!!record} onChange={(e) => setForm({ ...form, type: e.target.value as DnsRecordType })}>{RECORD_TYPES.map((t) => <option key={t}>{t}</option>)}</select>}
          </Field>
          <Field label="Name" hint={`@ is ${zone.name}`} error={err.name}>
            {(p) => <input {...p} className="input mono" value={form.name} onChange={(e) => setForm({ ...form, name: e.target.value })} placeholder="@" spellCheck={false} />}
          </Field>
        </div>
        <Field label="Value" error={err.value}>
          {(p) => <input {...p} className="input mono" value={form.value} onChange={(e) => setForm({ ...form, value: e.target.value })} spellCheck={false} />}
        </Field>
        <div className="form-row">
          <Field label="TTL in seconds" error={err.ttl}>
            {(p) => <input {...p} className="input" type="number" value={form.ttl} onChange={(e) => setForm({ ...form, ttl: e.target.value })} />}
          </Field>
          {needsPriority && (
            <Field label="Priority" error={err.priority}>
              {(p) => <input {...p} className="input" type="number" value={form.priority} onChange={(e) => setForm({ ...form, priority: e.target.value })} />}
            </Field>
          )}
        </div>
      </form>
    </Modal>
  );
}
