import { Link } from "react-router-dom";
import { useStore } from "../api/hooks";
import { Topbar } from "../components/Layout";
import { Card, Empty, Meter, PageHeader, Status, TableWrap } from "../components/ui";
import { bytes, longDate, ratio } from "../lib/format";

/**
 * Audit item U1: the account of a website owner or a reseller. It shows the plan, how much of
 * the plan is in use, who gives the service, and who can sign in. An owner gets this page and
 * not the list of customers and plans.
 */
export function AccountPage() {
  const state = useStore();
  const account = state.account;
  if (!account) {
    return (
      <>
        <Topbar />
        <main id="main" className="content">
          <Empty title="The panel could not load your account"><p>Try again in a moment.</p></Empty>
        </main>
      </>
    );
  }
  const { customer, plan, usage, provider, users } = account;
  const reseller = customer.role === "reseller";
  const self = state.session.user.id;
  const provided = provider ? provider.name : "the operator of this panel";
  const databaseServers = state.databaseServers.filter((x) => x.customerId === customer.id).length;
  const rows = plan ? [
    { label: "Websites", text: `${usage.websites} of ${plan.websites}`, value: ratio(usage.websites, plan.websites) },
    { label: "Database servers", text: `${databaseServers} of ${plan.databaseServers}`, value: ratio(databaseServers, plan.databaseServers) },
  ] : [];
  return (
    <>
      <Topbar />
      <main id="main" className="content">
        <PageHeader title="My account" description={`${customer.name} · ${reseller ? "reseller" : "website owner"} since ${longDate(customer.createdAt)}`} />
        {customer.suspended && (
          <div className="alert alert-danger" role="alert" style={{ marginBottom: 20 }}>
            <p><strong>This account is suspended.</strong> Your websites are stopped. Speak to {provided} to lift the suspension.</p>
          </div>
        )}
        <div className="grid-2">
          <Card title={plan ? `Plan: ${plan.name}` : "Plan"}>
            <div className="card-body">
              {!plan ? <p className="muted">This account has no plan.</p> : (
                <>
                  {rows.map((r) => (
                    <div className="resource" key={r.label}>
                      <div className="row"><span>{r.label}</span><span>{r.text}</span></div>
                      <Meter value={r.value} label={`${r.label}: ${r.text}`} tone={r.value >= 1 ? "danger" : r.value >= 0.8 ? "warning" : undefined} />
                    </div>
                  ))}
                  <p className="muted small" style={{ marginTop: 16 }}>
                    {usage.websites >= plan.websites
                      ? `You use all the websites of your plan. To add a website, ask ${provided} for a larger plan.`
                      : `You can add ${plan.websites - usage.websites} more ${plan.websites - usage.websites === 1 ? "website" : "websites"}.`}
                    {" "}Your websites have {usage.databases} {usage.databases === 1 ? "database" : "databases"} and use {bytes(usage.diskBytes)} of disk space.
                  </p>
                  {reseller && usage.customers !== undefined && <p className="muted small" style={{ marginTop: 8 }}>You have {usage.customers} {usage.customers === 1 ? "customer" : "customers"}. Their websites are not in these numbers. <Link to="/customers">Open customers</Link></p>}
                </>
              )}
            </div>
          </Card>
          <div className="stack">
            {plan && (
              <Card title="Limits of each website">
                <div className="card-body">
                  <table className="kv">
                    <tbody>
                      <tr><th scope="row">CPU</th><td>{plan.limits.vcpus} vCPU</td></tr>
                      <tr><th scope="row">Memory</th><td>{bytes(plan.limits.memoryBytes)}</td></tr>
                      <tr><th scope="row">Disk</th><td>{bytes(plan.limits.diskBytes)}</td></tr>
                      <tr><th scope="row">Disk speed</th><td>{bytes(plan.limits.diskIoBytesPerSecond)} each second</td></tr>
                      <tr><th scope="row">Processes</th><td>{plan.limits.processes}</td></tr>
                      <tr><th scope="row">PHP extensions</th><td><ul className="tag-list">{plan.phpExtensions.map((e) => <li key={e}><span className="tag">{e}</span></li>)}</ul></td></tr>
                    </tbody>
                  </table>
                </div>
              </Card>
            )}
            <Card title="Service and contact">
              <div className="card-body">
                <table className="kv">
                  <tbody>
                    <tr><th scope="row">Provider</th><td>{provider ? <>{provider.name} · <a href={`mailto:${provider.email}`}>{provider.email}</a></> : "The operator of this panel"}</td></tr>
                    <tr><th scope="row">Your contact</th><td>{customer.email}<span className="cell-sub">The address that your provider has for invoices and for messages about this account.</span></td></tr>
                  </tbody>
                </table>
                <p className="muted small" style={{ marginTop: 12 }}>To change the plan, the contact address or the users, speak to {provided}.</p>
              </div>
            </Card>
          </div>
        </div>
        <Card title="Users who can sign in" className="mt">
          <TableWrap>
            <table>
              <thead><tr><th scope="col">User</th><th scope="col">Sign-in</th><th scope="col">Since</th></tr></thead>
              <tbody>
                {users.map((u) => (
                  <tr key={u.id}>
                    <td><strong>{u.name}</strong>{u.id === self && <span className="badge badge-neutral" style={{ marginLeft: 8 }}>You</span>}<span className="cell-sub">{u.email}</span></td>
                    <td>{u.mustChangePassword ? <Status tone="warning">Temporary password</Status> : u.totpEnabled ? <Status tone="success">Password and two-step sign-in</Status> : <Status tone="neutral">Password only</Status>}</td>
                    <td>{u.createdAt ? longDate(u.createdAt) : ""}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </TableWrap>
          <div className="card-body" style={{ paddingTop: 12 }}>
            <p className="muted small">Each user sees all the websites of this account. To turn on two-step sign-in for your own user, open <Link to="/settings">Settings</Link>.</p>
          </div>
        </Card>
      </main>
    </>
  );
}
