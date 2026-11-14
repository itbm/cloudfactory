import { useState } from "react";
import { useNavigate, useParams } from "react-router-dom";
import { useAction, useStore } from "../api/hooks";
import { addCustomer, addUser, deleteUser, resetUserPassword, setCustomer, type NewAccess } from "../api/store";
import type { Customer, ManagedUser } from "../api/types";
import { Topbar } from "../components/Layout";
import { Card, Field, FormError, Modal, PageHeader, RowMenu, Status, TableWrap, Tabs, type RowAction } from "../components/ui";
import { bytes, shortDate } from "../lib/format";
import { ROLE_LABEL } from "../lib/roles";
import { CopyField } from "./website/AccessTab";

/** The customers and their users. Only an administrator and a reseller have this page. */
export function CustomersPage() {
  const { tab: asked } = useParams();
  const navigate = useNavigate();
  const admin = useStore().session.user.role === "administrator";
  const tab = asked === "users" ? "users" : "customers";
  return (
    <>
      <Topbar placeholder={admin ? "Search servers, images, customers and jobs" : undefined} />
      <main id="main" className="content content-wide">
        <PageHeader
          title="Customers"
          description={admin
            ? "Resellers and website owners. Each customer has a plan, and users who sign in."
            : "The website owners that you give service to. Each customer has a plan, and users who sign in."}
        />
        <Tabs label="Sections" current={tab} onSelect={(id) => navigate(id === "users" ? "/customers/users" : "/customers")} items={[{ id: "customers", label: "Customers" }, { id: "users", label: "Users" }]} />
        {tab === "customers" ? <Customers /> : <Users />}
      </main>
    </>
  );
}

function Customers() {
  const state = useStore();
  const [open, setOpen] = useState(false);
  const [form, setForm] = useState<Pick<Customer, "name" | "email" | "planId" | "role">>({ name: "", email: "", planId: state.plans[0]?.id ?? "", role: "owner" });
  // Audit finding B5: the panel sends no email, so it makes the first user and shows a temporary password.
  const [createUser, setCreateUser] = useState(true);
  const [access, setAccess] = useState<NewAccess | null>(null);
  const [planFor, setPlanFor] = useState<Customer | null>(null);
  const [suspending, setSuspending] = useState<Customer | null>(null);
  const add = useAction(addCustomer);
  const change = useAction(setCustomer);
  const admin = state.session.user.role === "administrator";
  // Batch 3, issue 2: a reseller cannot change its own plan or suspend itself.
  const own = (c: Customer) => !admin && c.id === state.session.contextId;
  const plan = (id: string) => state.plans.find((p) => p.id === id);
  const reseller = (c: Customer) => state.customers.find((r) => r.id === c.resellerId)?.name;
  // A reseller sees its customers. Its own account is on the page "My account".
  const customers = state.customers.filter((c) => !own(c));
  const actions = (c: Customer): RowAction[] => [
    { label: "Change plan…", onSelect: () => setPlanFor(c) },
    c.suspended
      ? { label: "Lift the suspension", onSelect: () => void change.run(c.id, { suspended: false }), disabled: change.busy }
      : { label: "Suspend…", onSelect: () => setSuspending(c), danger: true },
  ];
  return (
    <Card title="Customers" actions={<button type="button" className="btn btn-primary" onClick={() => setOpen(true)}>Add customer</button>}>
      {change.error && !planFor && <div className="card-body"><FormError error={change.error} /></div>}
      <TableWrap>
        <table>
          <thead><tr><th scope="col">Customer</th>{admin && <th scope="col">Role</th>}<th scope="col">Plan</th><th scope="col">Websites</th><th scope="col">Since</th><th scope="col">Status</th><th scope="col"><span className="visually-hidden">Actions</span></th></tr></thead>
          <tbody>
            {customers.map((c) => (
              <tr key={c.id}>
                <td><strong>{c.name}</strong><span className="cell-sub">{c.email}{admin && reseller(c) && ` · customer of ${reseller(c)}`}</span></td>
                {admin && <td>{c.role === "reseller" ? "Reseller" : "Website owner"}</td>}
                <td>{plan(c.planId)?.name ?? "No plan"}</td>
                <td>{state.websites.filter((w) => w.customerId === c.id).length} of {plan(c.planId)?.websites ?? "?"}</td>
                <td>{shortDate(c.createdAt)}</td>
                <td>{c.suspended ? <Status tone="danger">Suspended</Status> : <Status tone="success">Active</Status>}</td>
                <td className="cell-actions"><RowMenu label={`Actions for ${c.name}`} actions={actions(c)} /></td>
              </tr>
            ))}
            {customers.length === 0 && <tr><td colSpan={admin ? 7 : 6} className="muted">No customers yet. Add the first customer.</td></tr>}
          </tbody>
        </table>
      </TableWrap>
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
      {planFor && <PlanChangeDialog customer={planFor} onClose={() => { setPlanFor(null); change.clear(); }} />}
      {suspending && (
        <Modal
          title={`Suspend ${suspending.name}?`}
          onClose={() => setSuspending(null)}
          footer={<><button type="button" className="btn" onClick={() => setSuspending(null)}>Cancel</button><button type="button" className="btn btn-danger-solid" onClick={() => { void change.run(suspending.id, { suspended: true }); setSuspending(null); }}>Suspend</button></>}
        >
          <p>The panel stops the websites of {suspending.name}{suspending.role === "reseller" ? " and of its customers" : ""}. The users of the customer cannot sign in. The files, the databases and the {plan(suspending.planId)?.name} plan stay.</p>
          <p style={{ marginTop: 8 }}>You can lift the suspension at any time. The panel then starts the websites again.</p>
        </Modal>
      )}
      {access && <AccessDialog access={access} onClose={() => setAccess(null)} />}
    </Card>
  );
}

/**
 * Audit item U11: a change of the plan of a customer. Before, a selector in the table saved the
 * change at once. The dialog shows the limits of the two plans and the effect of the change.
 */
function PlanChangeDialog({ customer, onClose }: { customer: Customer; onClose: () => void }) {
  const state = useStore();
  const [planId, setPlanId] = useState(customer.planId);
  const change = useAction(setCustomer);
  const current = state.plans.find((p) => p.id === customer.planId);
  const next = state.plans.find((p) => p.id === planId);
  // The plan of a reseller also counts the websites of its customers.
  const ids = new Set([customer.id, ...(customer.role === "reseller" ? state.customers.filter((c) => c.resellerId === customer.id).map((c) => c.id) : [])]);
  const websites = state.websites.filter((w) => ids.has(w.customerId)).length;
  const changed = planId !== customer.planId;
  const over = !!next && websites > next.websites;
  return (
    <Modal
      title={`Change the plan of ${customer.name}`}
      onClose={onClose}
      footer={<><button type="button" className="btn" onClick={onClose}>Cancel</button><button type="button" className="btn btn-primary" disabled={!changed || over || change.busy} onClick={async () => { if ((await change.run(customer.id, { planId })).ok) onClose(); }}>{changed && next ? `Change to ${next.name}` : "Change plan"}</button></>}
    >
      <FormError error={change.error} />
      <Field label="Plan" hint={current ? `The current plan is ${current.name}.` : undefined}>
        {(p) => <select {...p} className="select" value={planId} onChange={(e) => setPlanId(e.target.value)}>{state.plans.map((pl) => <option key={pl.id} value={pl.id}>{pl.name}</option>)}</select>}
      </Field>
      {next && (
        <table className="kv" style={{ marginTop: 14 }}>
          <thead className="visually-hidden"><tr><th scope="col">Limit</th><th scope="col">Value</th></tr></thead>
          <tbody>
            <tr><th scope="row">Websites</th><td>{next.websites}{current && changed && <span className="muted"> (now {current.websites})</span>}</td></tr>
            <tr><th scope="row">Databases</th><td>{next.databases}{current && changed && <span className="muted"> (now {current.databases})</span>}</td></tr>
            <tr><th scope="row">Each website</th><td>{next.limits.vcpus} vCPU, {bytes(next.limits.memoryBytes)} memory, {bytes(next.limits.diskBytes)} disk</td></tr>
          </tbody>
        </table>
      )}
      {changed && next && over && (
        <div className="alert alert-warning alert-stack" style={{ marginTop: 14 }} role="alert">
          <p><strong>This plan is too small.</strong> {customer.name} has {websites} websites, and the {next.name} plan permits {next.websites}. Remove websites first, or select a larger plan.</p>
        </div>
      )}
      {changed && next && !over && (
        <div className="alert alert-info alert-stack" style={{ marginTop: 14 }} role="status">
          <p><strong>The effect of the change</strong></p>
          <p>New websites of {customer.name} get the limits of the {next.name} plan. {customer.name} can then have {next.websites} {next.websites === 1 ? "website" : "websites"}.</p>
          <p>{websites === 0 ? `${customer.name} has no websites now.` : `The ${websites === 1 ? "website" : `${websites} websites`} that ${customer.name} has now ${websites === 1 ? "keeps its" : "keep their"} plan and limits. To change one website, open its Settings tab.`}</p>
        </div>
      )}
    </Modal>
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

/**
 * Audit finding B5: the users who can sign in. An administrator changes all users. A reseller
 * changes the users of its own customers.
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
  const add = useAction(addUser);
  const remove = useAction(deleteUser);
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
      <TableWrap>
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
                <td className="cell-actions">
                  {self(u) ? <span className="muted small nowrap">Your account</span> : canChange(u) && (
                    <RowMenu label={`Actions for ${u.email}`} actions={[
                      { label: "Reset password…", onSelect: () => setResetting(u) },
                      { label: "Delete user…", onSelect: () => setDeleting(u), danger: true },
                    ]} />
                  )}
                </td>
              </tr>
            ))}
            {state.users.length === 0 && <tr><td colSpan={6} className="muted">No users.</td></tr>}
          </tbody>
        </table>
      </TableWrap>
      {open && (
        <Modal title="Add user" onClose={() => { setOpen(false); add.clear(); }} footer={<><button type="button" className="btn" onClick={() => { setOpen(false); add.clear(); }}>Cancel</button><button type="submit" form="usr" className="btn btn-primary" disabled={add.busy}>Add user</button></>}>
          <form id="usr" noValidate onSubmit={async (e) => {
            e.preventDefault();
            const r = await add.run({ name: form.name, email: form.email, customerId: form.customerId || null });
            if (!r.ok) return;
            setOpen(false);
            setForm({ ...form, name: "", email: "" });
            setAccess(r.value);
          }}>
            <FormError error={add.error} shown={["name", "email", "customerId"]} />
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
          </form>
        </Modal>
      )}
      {resetting && <ResetDialog user={resetting} onClose={() => setResetting(null)} onDone={(a) => { setResetting(null); setAccess(a); }} />}
      {deleting && (
        <Modal
          title={`Delete ${deleting.email}?`}
          onClose={() => setDeleting(null)}
          footer={<><button type="button" className="btn" onClick={() => setDeleting(null)}>Cancel</button><button type="button" className="btn btn-danger-solid" onClick={() => { void remove.run(deleting.id); setDeleting(null); }}>Delete user</button></>}
        >
          <p>{deleting.name} can no longer sign in. The panel deletes the sessions and the API tokens of this user. The websites of the customer stay.</p>
        </Modal>
      )}
      {access && <AccessDialog access={access} onClose={() => setAccess(null)} />}
    </Card>
  );
}

function ResetDialog({ user, onClose, onDone }: { user: ManagedUser; onClose: () => void; onDone: (access: NewAccess) => void }) {
  const [removeTotp, setRemoveTotp] = useState(false);
  const reset = useAction(resetUserPassword);
  return (
    <Modal title={`Reset the password of ${user.email}?`} onClose={onClose} footer={<><button type="button" className="btn" onClick={onClose}>Cancel</button><button type="button" className="btn btn-danger-solid" disabled={reset.busy} onClick={async () => { const r = await reset.run(user.id, removeTotp); if (r.ok) onDone(r.value); }}>Reset password</button></>}>
      <FormError error={reset.error} />
      <p>{user.name} gets a new temporary password. The panel signs the user out and deletes the API tokens of the user. It also removes a sign-in lock.</p>
      {user.totpEnabled && (
        <>
          <label className="check" style={{ marginTop: 10 }}><input type="checkbox" checked={removeTotp} onChange={(e) => setRemoveTotp(e.target.checked)} />Also remove TOTP</label>
          <p className="muted small">Use this when the user lost the authenticator app. The user then signs in with the password only, and sets up TOTP again.</p>
        </>
      )}
    </Modal>
  );
}

