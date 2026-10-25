import { useState } from "react";
import { useAction, useStore } from "../api/hooks";
import { addCustomer, savePlan, setBranding, setCustomer } from "../api/store";
import type { Customer, Plan } from "../api/types";
import { GB, MB } from "../api/seed";
import { Topbar } from "../components/Layout";
import { Card, ConfirmButton, Field, FormError, Modal, PageHeader, Status, Tabs } from "../components/ui";
import { bytes, shortDate } from "../lib/format";

export function CustomersPage() {
  const [tab, setTab] = useState("customers");
  return (
    <>
      <Topbar placeholder="Search servers, images, customers and jobs" />
      <main id="main" className="content content-wide">
        <PageHeader title="Customers and plans" description="Resellers add website owners and set their limits. A plan sets the default limits for each website." />
        <Tabs label="Sections" current={tab} onSelect={setTab} items={[{ id: "customers", label: "Customers" }, { id: "plans", label: "Plans" }, { id: "branding", label: "Branding" }]} />
        {tab === "customers" && <Customers />}
        {tab === "plans" && <Plans />}
        {tab === "branding" && <Branding />}
      </main>
    </>
  );
}

function Customers() {
  const state = useStore();
  const [open, setOpen] = useState(false);
  const [form, setForm] = useState<Pick<Customer, "name" | "email" | "planId" | "role">>({ name: "", email: "", planId: "plan_standard", role: "owner" });
  const add = useAction(addCustomer);
  const plan = (id: string) => state.plans.find((p) => p.id === id)?.name;
  return (
    <Card title="Customers" actions={<button type="button" className="btn btn-primary" onClick={() => setOpen(true)}>Add customer</button>}>
      <div className="table-wrap">
        <table>
          <thead><tr><th scope="col">Customer</th><th scope="col">Role</th><th scope="col">Plan</th><th scope="col">Websites</th><th scope="col">Since</th><th scope="col">Status</th><th scope="col"><span className="visually-hidden">Actions</span></th></tr></thead>
          <tbody>
            {state.customers.map((c) => (
              <tr key={c.id}>
                <td><strong>{c.name}</strong><span className="cell-sub">{c.email}{c.resellerId && ` · via ${state.customers.find((r) => r.id === c.resellerId)?.name}`}</span></td>
                <td>{c.role === "reseller" ? "Reseller" : "Website owner"}</td>
                <td>
                  <label className="visually-hidden" htmlFor={`plan-${c.id}`}>Plan for {c.name}</label>
                  <select id={`plan-${c.id}`} className="select" value={c.planId} onChange={(e) => setCustomer(c.id, { planId: e.target.value })}>
                    {state.plans.map((p) => <option key={p.id} value={p.id}>{p.name}</option>)}
                  </select>
                </td>
                <td>{state.websites.filter((w) => w.customerId === c.id).length} of {state.plans.find((p) => p.id === c.planId)?.websites}</td>
                <td>{shortDate(c.createdAt)}</td>
                <td>{c.suspended ? <Status tone="danger">Suspended</Status> : <Status tone="success">Active</Status>}</td>
                <td className="cell-actions" style={{ width: 140 }}>
                  {c.suspended
                    ? <button type="button" className="btn btn-sm" onClick={() => setCustomer(c.id, { suspended: false })}>Unsuspend</button>
                    : <ConfirmButton className="btn btn-sm btn-danger" title={`Suspend ${c.name}?`} confirm="Suspend" message={<p>The websites of {c.name} show a suspended page. Mail is still received. The {plan(c.planId)} plan stays.</p>} onConfirm={() => setCustomer(c.id, { suspended: true })}>Suspend</ConfirmButton>}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      {open && (
        <Modal title="Add customer" onClose={() => { setOpen(false); add.clear(); }} footer={<><button type="button" className="btn" onClick={() => setOpen(false)}>Cancel</button><button type="submit" form="cus" className="btn btn-primary">Add customer</button></>}>
          <form id="cus" noValidate onSubmit={async (e) => { e.preventDefault(); if ((await add.run(form)).ok) setOpen(false); }}>
            <FormError error={add.error} />
            <Field label="Name" error={add.error?.fields.name}>{(p) => <input {...p} className="input" value={form.name} onChange={(e) => setForm({ ...form, name: e.target.value })} />}</Field>
            <Field label="Email" hint="The panel sends an invitation to this address." error={add.error?.fields.email}>{(p) => <input {...p} className="input" type="email" value={form.email} onChange={(e) => setForm({ ...form, email: e.target.value })} />}</Field>
            <div className="form-row">
              <Field label="Role">{(p) => <select {...p} className="select" value={form.role} onChange={(e) => setForm({ ...form, role: e.target.value as Customer["role"] })}><option value="owner">Website owner</option><option value="reseller">Reseller</option></select>}</Field>
              <Field label="Plan">{(p) => <select {...p} className="select" value={form.planId} onChange={(e) => setForm({ ...form, planId: e.target.value })}>{state.plans.map((pl) => <option key={pl.id} value={pl.id}>{pl.name}</option>)}</select>}</Field>
            </div>
          </form>
        </Modal>
      )}
    </Card>
  );
}

const EMPTY_PLAN: Plan = { id: "", name: "", limits: { vcpus: 1, memoryBytes: 1 * GB, diskBytes: 10 * GB, diskIoBytesPerSecond: 30 * MB, processes: 75 }, websites: 5, mailboxes: 25, databases: 10, phpExtensions: ["opcache", "intl", "gd", "zip"] };
const ALL_EXTENSIONS = ["opcache", "intl", "gd", "imagick", "redis", "sodium", "zip", "bcmath", "soap", "xdebug", "pgsql", "mysqli", "pdo_mysql", "pdo_pgsql", "exif", "apcu"];

function Plans() {
  const state = useStore();
  const [editing, setEditing] = useState<Plan | null>(null);
  return (
    <Card title="Plans" actions={<button type="button" className="btn btn-primary" onClick={() => setEditing(EMPTY_PLAN)}>New plan</button>}>
      <div className="table-wrap">
        <table>
          <thead><tr><th scope="col">Plan</th><th scope="col">Websites</th><th scope="col">CPU</th><th scope="col">Memory</th><th scope="col">Disk</th><th scope="col">Disk I/O</th><th scope="col">Processes</th><th scope="col">Mailboxes</th><th scope="col"><span className="visually-hidden">Actions</span></th></tr></thead>
          <tbody>
            {state.plans.map((p) => (
              <tr key={p.id}>
                <td><strong>{p.name}</strong><span className="cell-sub">{p.phpExtensions.length} PHP extensions</span></td>
                <td>{p.websites}</td>
                <td>{p.limits.vcpus} vCPU</td>
                <td>{bytes(p.limits.memoryBytes)}</td>
                <td>{bytes(p.limits.diskBytes)}</td>
                <td>{bytes(p.limits.diskIoBytesPerSecond)}/s</td>
                <td>{p.limits.processes}</td>
                <td>{p.mailboxes}</td>
                <td className="cell-actions" style={{ width: 100 }}><button type="button" className="btn btn-sm" onClick={() => setEditing(p)}>Edit</button></td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      {editing && <PlanDialog plan={editing} onClose={() => setEditing(null)} />}
    </Card>
  );
}

function PlanDialog({ plan, onClose }: { plan: Plan; onClose: () => void }) {
  const [f, setF] = useState({
    name: plan.name, websites: String(plan.websites), mailboxes: String(plan.mailboxes), databases: String(plan.databases),
    vcpus: String(plan.limits.vcpus), memory: String(plan.limits.memoryBytes / MB), disk: String(plan.limits.diskBytes / GB), io: String(plan.limits.diskIoBytesPerSecond / MB), processes: String(plan.limits.processes),
    ext: plan.phpExtensions,
  });
  const save = useAction(savePlan);
  const num: [keyof typeof f, string][] = [["websites", "Websites"], ["mailboxes", "Mailboxes"], ["databases", "Databases"], ["vcpus", "CPU (vCPU)"], ["memory", "Memory (MB)"], ["disk", "Disk (GB)"], ["io", "Disk I/O (MB/s)"], ["processes", "Processes"]];
  return (
    <Modal wide title={plan.id ? `Edit ${plan.name}` : "New plan"} onClose={onClose} footer={<><button type="button" className="btn" onClick={onClose}>Cancel</button><button type="submit" form="plan" className="btn btn-primary">Save plan</button></>}>
      <form id="plan" noValidate onSubmit={async (e) => {
        e.preventDefault();
        const r = await save.run({ id: plan.id, name: f.name, websites: +f.websites, mailboxes: +f.mailboxes, databases: +f.databases, phpExtensions: f.ext, limits: { vcpus: +f.vcpus, memoryBytes: +f.memory * MB, diskBytes: +f.disk * GB, diskIoBytesPerSecond: +f.io * MB, processes: +f.processes } });
        if (r.ok) onClose();
      }}>
        <FormError error={save.error} />
        <Field label="Name" error={save.error?.fields.name}>{(p) => <input {...p} className="input" value={f.name} onChange={(e) => setF({ ...f, name: e.target.value })} />}</Field>
        <div className="form-row" style={{ marginTop: 14 }}>
          {num.map(([k, label]) => <Field key={k} label={label}>{(p) => <input {...p} className="input" type="number" min={0} step="any" value={f[k] as string} onChange={(e) => setF({ ...f, [k]: e.target.value })} />}</Field>)}
        </div>
        <fieldset className="checkbox-list" style={{ marginTop: 14 }}>
          <legend>PHP extensions that owners can turn on</legend>
          {ALL_EXTENSIONS.map((x) => (
            <label key={x} className="check"><input type="checkbox" checked={f.ext.includes(x)} onChange={(e) => setF({ ...f, ext: e.target.checked ? [...f.ext, x] : f.ext.filter((y) => y !== x) })} /><code>{x}</code></label>
          ))}
        </fieldset>
      </form>
    </Modal>
  );
}

/** FR-106: the reseller sets the logo, the colours and the panel domain. */
function Branding() {
  const state = useStore();
  const [f, setF] = useState(state.branding);
  const [saved, setSaved] = useState(false);
  const save = useAction(async () => { setBranding(f); setSaved(true); });
  return (
    <div className="grid-2">
      <Card title="Branding for your customers">
        <form className="card-body" noValidate onSubmit={(e) => { e.preventDefault(); setSaved(false); void save.run(); }}>
          <FormError error={save.error} />
          <Field label="Brand name">{(p) => <input {...p} className="input" value={f.name} onChange={(e) => setF({ ...f, name: e.target.value })} />}</Field>
          <Field label="Main colour" hint="The panel checks the contrast of the text on this colour." error={save.error?.fields.colour}>
            {(p) => (
              <span style={{ display: "flex", gap: 8 }}>
                <input type="color" aria-label="Pick a colour" value={f.colour} onChange={(e) => setF({ ...f, colour: e.target.value })} style={{ width: 52, height: 44, border: 0, background: "none" }} />
                <input {...p} className="input mono" value={f.colour} onChange={(e) => setF({ ...f, colour: e.target.value })} />
              </span>
            )}
          </Field>
          <Field label="Panel domain" hint="Point a CNAME record for this domain to panel.cloudfactory.io." error={save.error?.fields.panelDomain}>{(p) => <input {...p} className="input mono" value={f.panelDomain} onChange={(e) => setF({ ...f, panelDomain: e.target.value })} />}</Field>
          <Field label="Support email">{(p) => <input {...p} className="input" type="email" value={f.supportEmail} onChange={(e) => setF({ ...f, supportEmail: e.target.value })} />}</Field>
          <Field label="Logo" hint="SVG or PNG, up to 200 KB.">{(p) => <input {...p} type="file" accept="image/svg+xml,image/png" />}</Field>
          <div className="btn-group" style={{ marginTop: 14, alignItems: "center" }}>
            <button type="submit" className="btn btn-primary">Save branding</button>
            {saved && !save.error && <span className="text-success" role="status">Saved.</span>}
          </div>
        </form>
      </Card>
      <Card title="Preview">
        <div className="card-body">
          <div className="brand-preview" style={{ background: "#0f1b33" }}>
            <span className="swatch" style={{ background: f.colour, borderColor: f.colour }} aria-hidden="true" />
            {f.name || "Your brand"}
          </div>
          <p className="muted small" style={{ marginTop: 12 }}>Customers sign in at <code>https://{f.panelDomain}</code>.</p>
          <button type="button" className="btn" style={{ marginTop: 12, background: f.colour, color: "#fff", borderColor: f.colour }}>Example button</button>
        </div>
      </Card>
    </div>
  );
}
