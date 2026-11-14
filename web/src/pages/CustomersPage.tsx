import { useState } from "react";
import { useAction, useStore } from "../api/hooks";
import { features } from "../api/mode";
import { addCustomer, addUser, deleteUser, resetUserPassword, savePlan, setBranding, setCustomer, type NewAccess } from "../api/store";
import type { Customer, ManagedUser, Plan } from "../api/types";
import { GB, MB } from "../api/seed";
import { ConfirmIdentityFields, NO_CONFIRMATION } from "../components/ConfirmIdentity";
import { Topbar } from "../components/Layout";
import { Card, ConfirmButton, Field, FormError, Modal, PageHeader, Status, Tabs } from "../components/ui";
import { bytes, shortDate } from "../lib/format";
import { limitsText } from "../features/DatabaseServersPanel";
import { CopyField } from "./website/AccessTab";

export function CustomersPage() {
  const [tab, setTab] = useState("customers");
  return (
    <>
      <Topbar placeholder="Search servers, images, customers and jobs" />
      <main id="main" className="content content-wide">
        <PageHeader title="Customers and plans" description="Resellers add website owners and set their limits. Each customer has users who sign in. A plan sets the default limits for each website and for each database server." />
        <Tabs label="Sections" current={tab} onSelect={setTab} items={[{ id: "customers", label: "Customers" }, { id: "users", label: "Users" }, { id: "plans", label: "Plans" }, ...(features.branding ? [{ id: "branding", label: "Branding" }] : [])]} />
        {tab === "customers" && <Customers />}
        {tab === "users" && <Users />}
        {tab === "plans" && <Plans />}
        {tab === "branding" && <Branding />}
      </main>
    </>
  );
}

function Customers() {
  const state = useStore();
  const [open, setOpen] = useState(false);
  const [form, setForm] = useState<Pick<Customer, "name" | "email" | "planId" | "role">>({ name: "", email: "", planId: state.plans.find((p) => p.id === "plan_standard")?.id ?? state.plans[0]?.id ?? "", role: "owner" });
  // Audit finding B5: the panel sends no email, so it makes the first user and shows a temporary password.
  const [createUser, setCreateUser] = useState(true);
  const [access, setAccess] = useState<NewAccess | null>(null);
  const add = useAction(addCustomer);
  const change = useAction(setCustomer);
  const admin = state.session.user.role === "administrator";
  // Batch 3, issue 2: a reseller cannot change its own plan or suspend itself.
  const own = (c: Customer) => !admin && c.id === state.session.contextId;
  const plan = (id: string) => state.plans.find((p) => p.id === id)?.name;
  return (
    <Card title="Customers" actions={state.session.user.role !== "owner" && <button type="button" className="btn btn-primary" onClick={() => setOpen(true)}>Add customer</button>}>
      {change.error && <div className="card-body"><FormError error={change.error} /></div>}
      <div className="table-wrap">
        <table>
          <thead><tr><th scope="col">Customer</th><th scope="col">Role</th><th scope="col">Plan</th><th scope="col">Websites</th><th scope="col">Database servers</th><th scope="col">Since</th><th scope="col">Status</th><th scope="col"><span className="visually-hidden">Actions</span></th></tr></thead>
          <tbody>
            {state.customers.map((c) => (
              <tr key={c.id}>
                <td><strong>{c.name}</strong><span className="cell-sub">{c.email}{c.resellerId && ` · via ${state.customers.find((r) => r.id === c.resellerId)?.name}`}</span></td>
                <td>{c.role === "reseller" ? "Reseller" : "Website owner"}</td>
                <td>
                  {own(c) ? plan(c.planId) : <>
                    <label className="visually-hidden" htmlFor={`plan-${c.id}`}>Plan for {c.name}</label>
                    <select id={`plan-${c.id}`} className="select" value={c.planId} disabled={change.busy} onChange={(e) => void change.run(c.id, { planId: e.target.value })}>
                      {state.plans.map((p) => <option key={p.id} value={p.id}>{p.name}</option>)}
                    </select>
                  </>}
                </td>
                <td>{state.websites.filter((w) => w.customerId === c.id).length} of {state.plans.find((p) => p.id === c.planId)?.websites ?? "?"}</td>
                <td>{state.databaseServers.filter((s) => s.customerId === c.id).length} of {state.plans.find((p) => p.id === c.planId)?.databaseServers ?? "?"}</td>
                <td className="nowrap">{shortDate(c.createdAt)}</td>
                <td>{c.suspended ? <Status tone="danger">Suspended</Status> : <Status tone="success">Active</Status>}</td>
                <td className="cell-actions" style={{ width: 140 }}>
                  {own(c) ? <span className="muted small">Your account</span> : c.suspended
                    ? <button type="button" className="btn btn-sm" disabled={change.busy} onClick={() => void change.run(c.id, { suspended: false })}>Unsuspend</button>
                    : <ConfirmButton className="btn btn-sm btn-danger" title={`Suspend ${c.name}?`} confirm="Suspend" message={<p>The panel stops the websites of {c.name}{c.role === "reseller" ? " and of its customers" : ""}. Mail is still received. The {plan(c.planId)} plan stays.</p>} onConfirm={() => void change.run(c.id, { suspended: true })}>Suspend</ConfirmButton>}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      {open && (
        <Modal title="Add customer" onClose={() => { setOpen(false); add.clear(); }} footer={<><button type="button" className="btn" onClick={() => setOpen(false)}>Cancel</button><button type="submit" form="cus" className="btn btn-primary">Add customer</button></>}>
          <form id="cus" noValidate onSubmit={async (e) => {
            e.preventDefault();
            const r = await add.run(form, createUser);
            if (!r.ok) return;
            setOpen(false);
            setForm({ ...form, name: "", email: "" });
            setAccess(r.value);
          }}>
            <FormError error={add.error} />
            <Field label="Name" error={add.error?.fields.name}>{(p) => <input {...p} className="input" value={form.name} onChange={(e) => setForm({ ...form, name: e.target.value })} />}</Field>
            <Field label="Email" hint={createUser ? "The first user signs in with this address." : "The contact address of the customer."} error={add.error?.fields.email}>{(p) => <input {...p} className="input" type="email" value={form.email} onChange={(e) => setForm({ ...form, email: e.target.value })} />}</Field>
            <div className="form-row" style={{ marginTop: 14 }}>
              {admin && <Field label="Role">{(p) => <select {...p} className="select" value={form.role} onChange={(e) => setForm({ ...form, role: e.target.value as Customer["role"] })}><option value="owner">Website owner</option><option value="reseller">Reseller</option></select>}</Field>}
              <Field label="Plan">{(p) => <select {...p} className="select" value={form.planId} onChange={(e) => setForm({ ...form, planId: e.target.value })}>{state.plans.map((pl) => <option key={pl.id} value={pl.id}>{pl.name}</option>)}</select>}</Field>
            </div>
            <label className="check" style={{ marginTop: 14 }}><input type="checkbox" checked={createUser} onChange={(e) => setCreateUser(e.target.checked)} />Make a user who can sign in</label>
            <p className="muted small">{createUser ? "The panel sends no email. It shows a temporary password one time, and you give it to the customer." : "The customer cannot sign in until you add a user in the Users section."}</p>
          </form>
        </Modal>
      )}
      {access && <AccessDialog access={access} onClose={() => setAccess(null)} />}
    </Card>
  );
}

/** Audit finding B5: the panel shows a temporary password one time only. */
function AccessDialog({ access, onClose }: { access: NewAccess; onClose: () => void }) {
  return (
    <Modal title={`Temporary password for ${access.email}`} onClose={onClose} footer={<button type="button" className="btn btn-primary" onClick={onClose}>Done</button>}>
      <p>The panel shows this password one time only. Give it to the user in a safe way. The panel sends no email.</p>
      <CopyField value={access.temporaryPassword} label="Temporary password" />
      <p className="muted small" style={{ marginTop: 12 }}>The user signs in with <strong>{access.email}</strong> and this password, and then must set a new password.</p>
    </Modal>
  );
}

const ROLE_LABEL: Record<ManagedUser["role"], string> = { administrator: "Administrator", reseller: "Reseller", owner: "Website owner" };

/**
 * Audit finding B5: the users who can sign in. An administrator changes all users. A reseller
 * changes the users of its own customers. A website owner sees the users of its account.
 */
function Users() {
  const state = useStore();
  const role = state.session.user.role;
  const admin = role === "administrator";
  const [open, setOpen] = useState(false);
  const [access, setAccess] = useState<NewAccess | null>(null);
  const [resetting, setResetting] = useState<ManagedUser | null>(null);
  const [deleting, setDeleting] = useState<ManagedUser | null>(null);
  // An administrator has no customer. The value "" in the form is an administrator.
  const customers = admin ? state.customers : state.customers.filter((c) => c.resellerId === state.session.contextId);
  const [form, setForm] = useState({ name: "", email: "", customerId: customers[0]?.id ?? "" });
  // Second audit, finding F09: the password and the code for a new administrator.
  const [confirm, setConfirm] = useState(NO_CONFIRMATION);
  const add = useAction(addUser);
  const remove = useAction(deleteUser);
  const closeAdd = () => { setOpen(false); setConfirm(NO_CONFIRMATION); add.clear(); };
  const customer = (id?: string) => state.customers.find((c) => c.id === id);
  const self = (u: ManagedUser) => u.id === state.session.user.id;
  const canChange = (u: ManagedUser) => !self(u) && (admin || (role === "reseller" && customer(u.customerId)?.resellerId === state.session.contextId));
  const canAdd = admin || (role === "reseller" && customers.length > 0);
  return (
    <Card title="Users" actions={canAdd && <button type="button" className="btn btn-primary" onClick={() => setOpen(true)}>Add user</button>}>
      <div className="card-body" style={{ paddingBottom: 12 }}>
        <p className="muted">A user signs in with an email and a password. The panel sends no email: a new user gets a temporary password, which you give to the user. The user must set a new password at the first sign-in.</p>
        {remove.error && <FormError error={remove.error} />}
      </div>
      <div className="table-wrap">
        <table>
          <thead><tr><th scope="col">User</th><th scope="col">Role</th><th scope="col">Customer</th><th scope="col">Sign-in</th><th scope="col">Since</th><th scope="col"><span className="visually-hidden">Actions</span></th></tr></thead>
          <tbody>
            {state.users.map((u) => (
              <tr key={u.id}>
                <td><strong>{u.name}</strong><span className="cell-sub">{u.email}</span></td>
                <td>{ROLE_LABEL[u.role]}</td>
                <td>{u.customerId ? customer(u.customerId)?.name ?? "Removed customer" : <span className="muted">All customers</span>}</td>
                <td>
                  {u.mustChangePassword ? <Status tone="warning">Temporary password</Status> : u.totpEnabled ? <Status tone="success">Password and TOTP</Status> : <Status tone="neutral">Password only</Status>}
                </td>
                <td>{u.createdAt ? shortDate(u.createdAt) : ""}</td>
                <td className="cell-actions" style={{ width: 250 }}>
                  {self(u) ? <span className="muted small">Your account</span> : canChange(u) && (
                    <div className="btn-group" style={{ justifyContent: "flex-end" }}>
                      <button type="button" className="btn btn-sm" onClick={() => setResetting(u)}>Reset password…</button>
                      {u.customerId
                        ? <ConfirmButton className="btn btn-sm btn-danger" title={`Delete ${u.email}?`} confirm="Delete user" message={<p>{u.name} can no longer sign in. The panel deletes the sessions and the API tokens of this user. The websites of the customer stay.</p>} onConfirm={() => void remove.run(u.id)}>Delete</ConfirmButton>
                        : <button type="button" className="btn btn-sm btn-danger" onClick={() => setDeleting(u)}>Delete</button>}
                    </div>
                  )}
                </td>
              </tr>
            ))}
            {state.users.length === 0 && <tr><td colSpan={6} className="muted">No users.</td></tr>}
          </tbody>
        </table>
      </div>
      {open && (
        <Modal title="Add user" onClose={closeAdd} footer={<><button type="button" className="btn" onClick={closeAdd}>Cancel</button><button type="submit" form="usr" className="btn btn-primary" disabled={add.busy}>{form.customerId ? "Add user" : "Add administrator"}</button></>}>
          <form id="usr" noValidate onSubmit={async (e) => {
            e.preventDefault();
            const r = await add.run({ name: form.name, email: form.email, customerId: form.customerId || null }, form.customerId ? undefined : confirm);
            if (!r.ok) return;
            closeAdd();
            setForm({ ...form, name: "", email: "" });
            setAccess(r.value);
          }}>
            <FormError error={add.error} shown={["name", "email", "customerId", "password", "totp"]} />
            <Field label="Name" error={add.error?.fields.name}>{(p) => <input {...p} className="input" value={form.name} onChange={(e) => setForm({ ...form, name: e.target.value })} />}</Field>
            <Field label="Email" hint="The user signs in with this address." error={add.error?.fields.email}>{(p) => <input {...p} className="input" type="email" value={form.email} onChange={(e) => setForm({ ...form, email: e.target.value })} />}</Field>
            <Field label="Customer" hint={form.customerId ? "The user gets the role of the customer and sees only the websites of the customer." : "An administrator sees and changes all customers, servers and settings."} error={add.error?.fields.customerId}>
              {(p) => (
                <select {...p} className="select" value={form.customerId} onChange={(e) => setForm({ ...form, customerId: e.target.value })}>
                  {customers.map((c) => <option key={c.id} value={c.id}>{c.name} ({c.role === "reseller" ? "reseller" : "website owner"})</option>)}
                  {admin && <option value="">No customer: administrator</option>}
                </select>
              )}
            </Field>
            {!form.customerId && <ConfirmIdentityFields value={confirm} onChange={setConfirm} error={add.error} />}
          </form>
        </Modal>
      )}
      {resetting && <ResetDialog user={resetting} onClose={() => setResetting(null)} onDone={(a) => { setResetting(null); setAccess(a); }} />}
      {deleting && <DeleteAdministratorDialog user={deleting} onClose={() => setDeleting(null)} />}
      {access && <AccessDialog access={access} onClose={() => setAccess(null)} />}
    </Card>
  );
}

function ResetDialog({ user, onClose, onDone }: { user: ManagedUser; onClose: () => void; onDone: (access: NewAccess) => void }) {
  const [removeTotp, setRemoveTotp] = useState(false);
  const [confirm, setConfirm] = useState(NO_CONFIRMATION);
  const reset = useAction(resetUserPassword);
  // Second audit, finding F09: a reset for an administrator needs the password and a code.
  const administrator = !user.customerId;
  return (
    <Modal title={`Reset the password of ${user.email}?`} onClose={onClose} footer={<><button type="button" className="btn" onClick={onClose}>Cancel</button><button type="submit" form="reset-user" className="btn btn-danger-solid" disabled={reset.busy}>Reset password</button></>}>
      <form id="reset-user" noValidate onSubmit={async (e) => { e.preventDefault(); const r = await reset.run(user.id, removeTotp, administrator ? confirm : undefined); if (r.ok) onDone(r.value); }}>
        <FormError error={reset.error} shown={["password", "totp"]} />
        <p>{user.name} gets a new temporary password. The panel signs the user out and deletes the API tokens of the user. It also removes a sign-in lock.</p>
        {user.totpEnabled && (
          <>
            <label className="check" style={{ marginTop: 10 }}><input type="checkbox" checked={removeTotp} onChange={(e) => setRemoveTotp(e.target.checked)} />Also remove TOTP</label>
            <p className="muted small">Use this when the user lost the authenticator app. The user then signs in with the password only, and sets up TOTP again.</p>
          </>
        )}
        {administrator && <ConfirmIdentityFields value={confirm} onChange={setConfirm} error={reset.error} />}
      </form>
    </Modal>
  );
}

/** Second audit, finding F09: to delete an administrator, type the password and a code. */
function DeleteAdministratorDialog({ user, onClose }: { user: ManagedUser; onClose: () => void }) {
  const [confirm, setConfirm] = useState(NO_CONFIRMATION);
  const remove = useAction(deleteUser);
  return (
    <Modal title={`Delete the administrator ${user.email}?`} onClose={onClose} footer={<><button type="button" className="btn" onClick={onClose}>Cancel</button><button type="submit" form="delete-administrator" className="btn btn-danger-solid" disabled={remove.busy}>Delete administrator</button></>}>
      <form id="delete-administrator" noValidate onSubmit={async (e) => { e.preventDefault(); if ((await remove.run(user.id, confirm)).ok) onClose(); }}>
        <FormError error={remove.error} shown={["password", "totp"]} />
        <p>{user.name} can no longer sign in. The panel deletes the sessions and the API tokens of this user. The other administrators see a notice about this change.</p>
        <ConfirmIdentityFields value={confirm} onChange={setConfirm} error={remove.error} />
      </form>
    </Modal>
  );
}

const EMPTY_PLAN: Plan = {
  id: "", name: "", limits: { vcpus: 1, memoryBytes: 1 * GB, diskBytes: 10 * GB, diskIoBytesPerSecond: 30 * MB, processes: 75 }, websites: 5, mailboxes: 25, databases: 10, phpExtensions: ["opcache", "intl", "gd", "zip"],
  databaseServers: 3, databaseLimits: { vcpus: 1, memoryBytes: 512 * MB, diskBytes: 5 * GB },
};
const ALL_EXTENSIONS = ["opcache", "intl", "gd", "imagick", "redis", "sodium", "zip", "bcmath", "soap", "xdebug", "pgsql", "mysqli", "pdo_mysql", "pdo_pgsql", "exif", "apcu"];

function Plans() {
  const state = useStore();
  const [editing, setEditing] = useState<Plan | null>(null);
  // Only an administrator makes and changes plans. A reseller sees the plans that it can give.
  const admin = state.session.user.role === "administrator";
  return (
    <Card title="Plans" actions={admin && <button type="button" className="btn btn-primary" onClick={() => setEditing(EMPTY_PLAN)}>New plan</button>}>
      <div className="table-wrap">
        <table>
          <thead><tr><th scope="col">Plan</th><th scope="col">Websites</th><th scope="col">CPU</th><th scope="col">Memory</th><th scope="col">Disk</th><th scope="col">Disk I/O</th><th scope="col">Processes</th><th scope="col">Mailboxes</th><th scope="col">Database servers</th><th scope="col"><span className="visually-hidden">Actions</span></th></tr></thead>
          <tbody>
            {state.plans.map((p) => (
              <tr key={p.id}>
                <td><strong>{p.name}</strong><span className="cell-sub">{p.phpExtensions.length} PHP extensions</span></td>
                <td>{p.websites}</td>
                <td className="nowrap">{p.limits.vcpus} vCPU</td>
                <td className="nowrap">{bytes(p.limits.memoryBytes)}</td>
                <td className="nowrap">{bytes(p.limits.diskBytes)}</td>
                <td className="nowrap">{bytes(p.limits.diskIoBytesPerSecond)}/s</td>
                <td>{p.limits.processes}</td>
                <td>{p.mailboxes}</td>
                <td>
                  {p.databaseServers}
                  <span className="cell-sub nowrap">{limitsText(p.databaseLimits)}</span>
                  <span className="cell-sub nowrap">{p.databases} {p.databases === 1 ? "database" : "databases"} in each one</span>
                </td>
                <td className="cell-actions" style={{ width: 100 }}>{admin && <button type="button" className="btn btn-sm" onClick={() => setEditing(p)}>Edit</button>}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      {editing && <PlanDialog plan={editing} onClose={() => setEditing(null)} />}
    </Card>
  );
}

type PlanForm = {
  name: string; websites: string; mailboxes: string; databases: string; vcpus: string; memory: string; disk: string; io: string; processes: string;
  databaseServers: string; dbVcpus: string; dbMemory: string; dbDisk: string; ext: string[];
};
type PlanNumber = Exclude<keyof PlanForm, "name" | "ext">;

/** The fields of a website of the plan, and of a database server of the plan. The third value is the field of the API for the error. */
const WEBSITE_FIELDS: [PlanNumber, string, string][] = [
  ["websites", "Websites", "websites"], ["vcpus", "CPU (vCPU)", "limits.vcpus"], ["memory", "Memory (MB)", "limits.memoryBytes"], ["disk", "Disk (GB)", "limits.diskBytes"],
  ["io", "Disk I/O (MB/s)", "limits.diskIoBytesPerSecond"], ["processes", "Processes", "limits.processes"], ["mailboxes", "Mailboxes", "mailboxes"],
];
const DATABASE_FIELDS: [PlanNumber, string, string][] = [
  ["databaseServers", "Database servers", "databaseServers"], ["databases", "Databases in each one", "databases"],
  ["dbVcpus", "CPU (vCPU)", "databaseLimits.vcpus"], ["dbMemory", "Memory (MB)", "databaseLimits.memoryBytes"], ["dbDisk", "Disk (GB)", "databaseLimits.diskBytes"],
];

function PlanDialog({ plan, onClose }: { plan: Plan; onClose: () => void }) {
  const [f, setF] = useState<PlanForm>({
    name: plan.name, websites: String(plan.websites), mailboxes: String(plan.mailboxes), databases: String(plan.databases),
    vcpus: String(plan.limits.vcpus), memory: String(plan.limits.memoryBytes / MB), disk: String(plan.limits.diskBytes / GB), io: String(plan.limits.diskIoBytesPerSecond / MB), processes: String(plan.limits.processes),
    databaseServers: String(plan.databaseServers), dbVcpus: String(plan.databaseLimits.vcpus), dbMemory: String(plan.databaseLimits.memoryBytes / MB), dbDisk: String(plan.databaseLimits.diskBytes / GB),
    ext: plan.phpExtensions,
  });
  const save = useAction(savePlan);
  const fields = (list: [PlanNumber, string, string][]) => list.map(([k, label, api]) => (
    <Field key={k} label={label} error={save.error?.fields[api]}>{(p) => <input {...p} className="input" type="number" min={0} step="any" value={f[k]} onChange={(e) => setF({ ...f, [k]: e.target.value })} />}</Field>
  ));
  return (
    <Modal wide title={plan.id ? `Edit ${plan.name}` : "New plan"} onClose={onClose} footer={<><button type="button" className="btn" onClick={onClose}>Cancel</button><button type="submit" form="plan" className="btn btn-primary">Save plan</button></>}>
      <form id="plan" noValidate onSubmit={async (e) => {
        e.preventDefault();
        const r = await save.run({
          id: plan.id, name: f.name, websites: +f.websites, mailboxes: +f.mailboxes, databases: +f.databases, phpExtensions: f.ext,
          limits: { vcpus: +f.vcpus, memoryBytes: +f.memory * MB, diskBytes: +f.disk * GB, diskIoBytesPerSecond: +f.io * MB, processes: +f.processes },
          databaseServers: +f.databaseServers, databaseLimits: { vcpus: +f.dbVcpus, memoryBytes: Math.round(+f.dbMemory * MB), diskBytes: Math.round(+f.dbDisk * GB) },
        });
        if (r.ok) onClose();
      }}>
        <FormError error={save.error} shown={["name", ...WEBSITE_FIELDS.map((x) => x[2]), ...DATABASE_FIELDS.map((x) => x[2])]} />
        <Field label="Name" error={save.error?.fields.name}>{(p) => <input {...p} className="input" value={f.name} onChange={(e) => setF({ ...f, name: e.target.value })} />}</Field>
        <fieldset className="form-section">
          <legend>Websites</legend>
          <p className="hint">The number of websites and mailboxes of a customer with the plan, and the limits of each website.</p>
          <div className="form-row">{fields(WEBSITE_FIELDS)}</div>
        </fieldset>
        <fieldset className="form-section">
          <legend>Database servers</legend>
          <p className="hint">The number of database servers of a customer with the plan, and the limits of each database server. A database server is one container with MariaDB, PostgreSQL or Valkey.</p>
          <div className="form-row">{fields(DATABASE_FIELDS)}</div>
          {plan.id && <p className="hint">When you change the CPU, the memory or the disk, the panel restarts the database servers that have the limits of this plan.</p>}
        </fieldset>
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
