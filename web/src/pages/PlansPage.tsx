import { useState } from "react";
import { useAction, useStore } from "../api/hooks";
import { savePlan } from "../api/store";
import type { Plan } from "../api/types";
import { Topbar } from "../components/Layout";
import { Card, Field, FormError, Modal, PageHeader, TableWrap } from "../components/ui";
import { limitsText } from "../features/DatabaseServersPanel";
import { bytes, GB, MB } from "../lib/format";

const EMPTY_PLAN: Plan = { id: "", name: "", limits: { vcpus: 1, memoryBytes: 1 * GB, diskBytes: 10 * GB, diskIoBytesPerSecond: 30 * MB, processes: 75 }, websites: 5, mailboxes: 0, databases: 10, phpExtensions: ["opcache", "intl", "gd", "zip"], databaseServers: 3, databaseLimits: { vcpus: 1, memoryBytes: 512 * MB, diskBytes: 5 * GB } };
const ALL_EXTENSIONS = ["opcache", "intl", "gd", "imagick", "redis", "sodium", "zip", "bcmath", "soap", "xdebug", "pgsql", "mysqli", "pdo_mysql", "pdo_pgsql", "exif", "apcu"];

/** The plans. Only an administrator makes and changes plans. A reseller sees the plans that it can give. */
export function PlansPage() {
  const state = useStore();
  const [editing, setEditing] = useState<Plan | null>(null);
  const admin = state.session.user.role === "administrator";
  const customers = (id: string) => state.customers.filter((c) => c.planId === id).length;
  return (
    <>
      <Topbar placeholder={admin ? "Search servers, images, customers and jobs" : undefined} />
      <main id="main" className="content content-wide">
        <PageHeader
          title="Plans"
          description={admin
            ? "A plan sets how many websites and database servers a customer can have, and the limits of each website and each database server."
            : "The plans that you can give to your customers. A plan sets how many websites and database servers a customer can have, and the limits of each one. Your provider makes the plans."}
        />
    <Card title="Plans" actions={admin && <button type="button" className="btn btn-primary" onClick={() => setEditing(EMPTY_PLAN)}>New plan</button>}>
      <TableWrap>
        <table>
          <thead><tr><th scope="col">Plan</th><th scope="col">Customers</th><th scope="col">Websites</th><th scope="col">Limits of each website</th><th scope="col">Database servers</th><th scope="col">Limits of each database server</th>{admin && <th scope="col"><span className="visually-hidden">Actions</span></th>}</tr></thead>
          <tbody>
            {state.plans.map((p) => (
              <tr key={p.id}>
                <td><strong>{p.name}</strong><span className="cell-sub">{p.phpExtensions.length} PHP extensions</span></td>
                <td>{customers(p.id)}</td>
                <td>{p.websites}</td>
                <td>{p.limits.vcpus} vCPU · {bytes(p.limits.memoryBytes)} memory · {bytes(p.limits.diskBytes)} disk<span className="cell-sub">{bytes(p.limits.diskIoBytesPerSecond)}/s disk I/O · {p.limits.processes} processes</span></td>
                <td>{p.databaseServers}</td>
                <td>{limitsText(p.databaseLimits)}<span className="cell-sub">{p.databases} {p.databases === 1 ? "database" : "databases"} in each one</span></td>
                {admin && <td className="cell-actions" style={{ width: 100 }}><button type="button" className="btn btn-sm" onClick={() => setEditing(p)}>Edit</button></td>}
              </tr>
            ))}
          </tbody>
        </table>
      </TableWrap>
      {editing && <PlanDialog plan={editing} onClose={() => setEditing(null)} />}
    </Card>
      </main>
    </>
  );
}

function PlanDialog({ plan, onClose }: { plan: Plan; onClose: () => void }) {
  const [f, setF] = useState({
    name: plan.name, websites: String(plan.websites), databases: String(plan.databases),
    vcpus: String(plan.limits.vcpus), memory: String(plan.limits.memoryBytes / MB), disk: String(plan.limits.diskBytes / GB), io: String(plan.limits.diskIoBytesPerSecond / MB), processes: String(plan.limits.processes),
    databaseServers: String(plan.databaseServers), dbVcpus: String(plan.databaseLimits.vcpus), dbMemory: String(plan.databaseLimits.memoryBytes / MB), dbDisk: String(plan.databaseLimits.diskBytes / GB),
    ext: plan.phpExtensions,
  });
  const save = useAction(savePlan);
  // The third value is the field of the API, for its error.
  type Row = [Exclude<keyof typeof f, "name" | "ext">, string, string];
  const websiteFields: Row[] = [["websites", "Websites", "websites"], ["vcpus", "CPU (vCPU)", "limits.vcpus"], ["memory", "Memory (MB)", "limits.memoryBytes"], ["disk", "Disk (GB)", "limits.diskBytes"], ["io", "Disk I/O (MB/s)", "limits.diskIoBytesPerSecond"], ["processes", "Processes", "limits.processes"]];
  const databaseFields: Row[] = [["databaseServers", "Database servers", "databaseServers"], ["databases", "Databases in each one", "databases"], ["dbVcpus", "CPU (vCPU)", "databaseLimits.vcpus"], ["dbMemory", "Memory (MB)", "databaseLimits.memoryBytes"], ["dbDisk", "Disk (GB)", "databaseLimits.diskBytes"]];
  const fields = (rows: Row[]) => rows.map(([k, label, api]) => (
    <Field key={k} label={label} error={save.error?.fields[api]}>{(p) => <input {...p} className="input" type="number" min={0} step="any" value={f[k]} onChange={(e) => setF({ ...f, [k]: e.target.value })} />}</Field>
  ));
  return (
    <Modal wide title={plan.id ? `Edit ${plan.name}` : "New plan"} onClose={onClose} footer={<><button type="button" className="btn" onClick={onClose}>Cancel</button><button type="submit" form="plan" className="btn btn-primary">Save plan</button></>}>
      <form id="plan" noValidate onSubmit={async (e) => {
        e.preventDefault();
        const r = await save.run({ id: plan.id, name: f.name, websites: +f.websites, mailboxes: plan.mailboxes, databases: +f.databases, phpExtensions: f.ext, limits: { vcpus: +f.vcpus, memoryBytes: +f.memory * MB, diskBytes: +f.disk * GB, diskIoBytesPerSecond: +f.io * MB, processes: +f.processes }, databaseServers: +f.databaseServers, databaseLimits: { vcpus: +f.dbVcpus, memoryBytes: Math.round(+f.dbMemory * MB), diskBytes: Math.round(+f.dbDisk * GB) } });
        if (r.ok) onClose();
      }}>
        <FormError error={save.error} shown={["name", ...websiteFields.map((x) => x[2]), ...databaseFields.map((x) => x[2])]} />
        {plan.id && <p className="muted" style={{ marginBottom: 12 }}>The websites of this plan get the new limits and extensions. A website with its own limits keeps them.</p>}
        <Field label="Name" error={save.error?.fields.name}>{(p) => <input {...p} className="input" value={f.name} onChange={(e) => setF({ ...f, name: e.target.value })} />}</Field>
        <fieldset className="form-section">
          <legend>Websites</legend>
          <p className="hint">The number of websites of a customer with the plan, and the limits of each website.</p>
          <div className="form-row">{fields(websiteFields)}</div>
        </fieldset>
        <fieldset className="form-section">
          <legend>Database servers</legend>
          <p className="hint">The number of database servers of a customer with the plan, and the limits of each database server. A database server is one container with MariaDB, PostgreSQL or Valkey.</p>
          <div className="form-row">{fields(databaseFields)}</div>
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
