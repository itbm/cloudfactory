import { useState } from "react";
import { useAction, useStore } from "../api/hooks";
import { contextCustomers, inContext } from "../api/selectors";
import { createDatabaseServer, deleteDatabaseServer, moveDatabaseServer, retryDatabaseServer, setDatabaseServer } from "../api/store";
import type { Customer, DatabaseEngine, DatabaseEngineInfo, DatabaseLimits, DatabaseServer, Plan, State } from "../api/types";
import { Card, Field, FormError, JobError, Meter, Modal, RowMenu, Status, TableWrap } from "../components/ui";
import { bytes, GB, longDate, MB } from "../lib/format";
import { DATABASE_PORT, DATABASE_SERVER_STATUS, engineInfo, engineName, jobActive } from "../lib/labels";

const ENGINE_USE: Record<string, string> = { mariadb: "SQL databases", postgresql: "SQL databases", valkey: "Cache, sessions and queues" };

/** What an engine is for. A pack engine shows its publisher. */
const engineUse = (e: DatabaseEngineInfo) => ENGINE_USE[e.engine] ?? `From a pack of ${e.pack?.publisher ?? "a publisher"}`;

/** "8.4 · support until 30 April 2032" */
const versionText = (v: DatabaseEngineInfo["versions"][number]) =>
  !v.supportEndsAt ? v.version : `${v.version} · ${new Date(v.supportEndsAt) < new Date() ? "support ended" : "support until"} ${longDate(v.supportEndsAt)}`;

/** "MariaDB, PostgreSQL, Valkey or MySQL" */
const engineList = (engines: DatabaseEngineInfo[]) => {
  const names = engines.map((e) => e.name);
  return names.length < 2 ? names.join("") : `${names.slice(0, -1).join(", ")} or ${names[names.length - 1]}`;
};

/** "2 vCPU · 1 GB memory · 10 GB disk" */
export const limitsText = (l: DatabaseLimits) => `${l.vcpus} vCPU · ${bytes(l.memoryBytes)} memory · ${bytes(l.diskBytes)} disk`;

const tone = (ratio: number) => (ratio >= 0.9 ? "danger" : ratio >= 0.75 ? "warning" : undefined);

/**
 * True when the user selects the plan of a database server of the customer: an administrator,
 * or the reseller of the customer. The other users get the plan of the customer.
 */
function canSelectPlan(state: State, customer: Customer | undefined): boolean {
  const role = state.session.user.role;
  return role === "administrator" || (role === "reseller" && !!customer?.resellerId && customer.resellerId === state.session.contextId);
}

/**
 * The database servers of the customers of the account. A customer makes database servers as
 * it makes websites: each one is a container with one engine and its own limits.
 */
export function DatabaseServersPanel() {
  const state = useStore();
  const customers = contextCustomers(state);
  const servers = state.databaseServers
    .filter((s) => inContext(state, s.customerId))
    .sort((a, b) => a.customerId.localeCompare(b.customerId) || a.engine.localeCompare(b.engine) || a.port - b.port);
  const [creating, setCreating] = useState(false);
  const [editing, setEditing] = useState<DatabaseServer | null>(null);
  const [removing, setRemoving] = useState<DatabaseServer | null>(null);
  const [moving, setMoving] = useState<DatabaseServer | null>(null);
  const admin = state.session.user.role === "administrator";
  const retry = useAction(retryDatabaseServer);
  const remove = useAction(deleteDatabaseServer);
  const plan = (id: string) => state.plans.find((p) => p.id === id);
  const customer = (id: string) => state.customers.find((c) => c.id === id);
  const host = (id: string) => state.servers.find((s) => s.id === id)?.name;
  const showCustomer = customers.length > 1 || servers.some((s) => s.customerId !== customers[0]?.id);
  // With one customer, the card tells how many database servers its plan allows.
  const only = customers.length === 1 ? customers[0] : undefined;
  const allowed = only && plan(only.planId)?.databaseServers;

  return (
    <Card title="Database servers" actions={<button type="button" className="btn btn-primary" onClick={() => setCreating(true)}>New database server</button>}>
      <div className="card-body" style={{ paddingBottom: 12 }}>
        <p className="muted">
          A database server is one container with {engineList(state.databaseEngines)}. It has its own limits for CPU, memory and disk space, and a plan gives the number of its databases. All the websites of the customer can connect to it, also from a different server.
          {only && allowed !== undefined && <> {only.name} has {servers.filter((s) => s.customerId === only.id).length} of {allowed} database servers.</>}
        </p>
        <FormError error={retry.error ?? remove.error} />
      </div>
      <TableWrap>
        <table>
          <thead>
            <tr>
              <th scope="col">Name</th>
              <th scope="col">Plan</th>
              <th scope="col">Memory and disk</th>
              <th scope="col">Databases</th>
              <th scope="col">Host</th>
              <th scope="col">Status</th>
              <th scope="col"><span className="visually-hidden">Actions</span></th>
            </tr>
          </thead>
          <tbody>
            {servers.map((s) => {
              const status = DATABASE_SERVER_STATUS[s.status];
              const job = state.jobs.find((j) => j.databaseServerId === s.id && jobActive(j));
              const failed = s.status === "failed" ? state.jobs.find((j) => j.databaseServerId === s.id && j.state === "failed") : undefined;
              return (
                <tr key={s.id}>
                  <td>
                    <strong className="nowrap">{s.name}</strong>
                    <span className="cell-sub">{engineName(state.databaseEngines, s.engine)}{s.engineVersion && ` ${s.engineVersion}`}</span>
                    {showCustomer && <span className="cell-sub">{customer(s.customerId)?.name ?? "Removed customer"}</span>}
                  </td>
                  <td>
                    {plan(s.planId)?.name ?? "Other plan"}
                    <span className="cell-sub nowrap">{s.limits.vcpus} vCPU</span>
                  </td>
                  <td><Use server={s} /></td>
                  <td className="nowrap">{s.databases} of {s.maxDatabases}</td>
                  <td>
                    {s.networkHost && (s.networkHost !== s.host || s.networkPort !== s.port)
                      ? <><code className="nowrap">{s.networkHost}:{s.networkPort}</code><span className="cell-sub">Also <code>{s.host}:{s.port}</code>{host(s.serverId) ? ` on the server ${host(s.serverId)}` : " on its server"}</span></>
                      : <><code>{s.host}:{s.port}</code>{host(s.serverId) && <span className="cell-sub">On the server {host(s.serverId)}</span>}</>}
                  </td>
                  <td style={{ minWidth: 120 }}>
                    <Status tone={status.tone}>{status.label}</Status>
                    {s.status === "moving" && <span className="cell-sub">To {host(s.movingToServerId ?? "") ?? "a different server"}</span>}
                    {job && <span className="cell-sub">{job.steps[Math.min(job.step, job.steps.length - 1)]}…</span>}
                    {failed && <JobError job={failed} />}
                  </td>
                  <td className="cell-actions">
                    {s.status !== "removing" && s.status !== "moving" && (
                      <RowMenu label={`Actions for the database server ${s.name}`} actions={[
                        ...(s.status === "failed" ? [{ label: "Try again", onSelect: () => void retry.run(s.id) }] : []),
                        { label: "Change", onSelect: () => setEditing(s) },
                        ...(admin && engineInfo(state.databaseEngines, s.engine).moves && (s.status === "running" || s.status === "stopped") ? [{ label: "Move to a different server", onSelect: () => setMoving(s) }] : []),
                        { label: "Remove", danger: true, onSelect: () => setRemoving(s) },
                      ]} />
                    )}
                  </td>
                </tr>
              );
            })}
            {servers.length === 0 && <tr><td colSpan={7} className="muted">No database servers. The panel makes the first database server of an engine when you make a database.</td></tr>}
          </tbody>
        </table>
      </TableWrap>
      <p className="card-body muted small" style={{ paddingTop: 12 }}>
        An app connects to the first name in the Host column, for example <code>shop-db.internal:{DATABASE_PORT.mariadb}</code>. The name is the name of the database server with <code>.internal</code>, and the port is the usual port of the engine. The name is the same for each website of the customer, on the same server and on a different server. A website on the same server can also use the name of a built-in engine: there, a second database server of the same engine has the next port.{state.databaseEngines.some((e) => e.pack) && " A database server of an engine from a pack has only its network name."}
      </p>
      {creating && <CreateDialog onClose={() => setCreating(false)} />}
      {editing && <ChangeDialog server={editing} onClose={() => setEditing(null)} />}
      {moving && <MoveDialog server={moving} onClose={() => setMoving(null)} />}
      {removing && (
        removing.databases > 0 ? (
          <Modal title={`Remove database server ${removing.name}?`} onClose={() => setRemoving(null)} footer={<button type="button" className="btn btn-primary" onClick={() => setRemoving(null)}>Close</button>}>
            <p>This database server has {removing.databases} {removing.databases === 1 ? "database" : "databases"}. Remove the databases and the database users first. Then you can remove the database server.</p>
          </Modal>
        ) : (
          <Modal
            title={`Remove database server ${removing.name}?`}
            onClose={() => setRemoving(null)}
            footer={<><button type="button" className="btn" onClick={() => setRemoving(null)}>Cancel</button><button type="button" className="btn btn-danger-solid" onClick={() => { const id = removing.id; setRemoving(null); void remove.run(id); }}>Remove database server</button></>}
          >
            <p>The panel stops the container and removes its data. You cannot restore the database server. The customer can then make a new database server.</p>
          </Modal>
        )
      )}
    </Card>
  );
}

/**
 * The memory and the disk limits of the database server. For a database server that runs, it
 * also shows the memory and the disk space that it uses, from the last report of the server.
 */
function Use({ server }: { server: DatabaseServer }) {
  const u = server.usage;
  const limits = server.limits;
  if (server.status !== "running" || !u || (u.memoryBytes === undefined && u.diskBytes === undefined)) {
    return (
      <>
        <span className="nowrap" style={{ display: "block" }}>{bytes(limits.memoryBytes)} memory</span>
        <span className="nowrap" style={{ display: "block" }}>{bytes(limits.diskBytes)} disk</span>
      </>
    );
  }
  const memory = u.memoryBytes ?? 0;
  const disk = u.diskBytes ?? 0;
  // The size of the file system of the data. It is the disk limit, less the space that the file system itself uses.
  const diskSize = u.diskSizeBytes || limits.diskBytes;
  return (
    <>
      <span className="nowrap" style={{ display: "block" }}>
        <Meter inline value={memory / limits.memoryBytes} label={`Memory: ${bytes(memory)} of ${bytes(limits.memoryBytes)}`} tone={tone(memory / limits.memoryBytes)} />
        {bytes(memory)} of {bytes(limits.memoryBytes)} memory
      </span>
      <span className="nowrap" style={{ display: "block" }}>
        <Meter inline value={disk / diskSize} label={`Disk: ${bytes(disk)} of ${bytes(diskSize)}`} tone={tone(disk / diskSize)} />
        {bytes(disk)} of {bytes(diskSize)} disk
      </span>
    </>
  );
}

type LimitForm = { vcpus: string; memory: string; disk: string };
const toForm = (l: DatabaseLimits): LimitForm => ({ vcpus: String(l.vcpus), memory: String(Math.round(l.memoryBytes / MB)), disk: String(+(l.diskBytes / GB).toFixed(2)) });
const fromForm = (f: LimitForm): DatabaseLimits => ({ vcpus: +f.vcpus, memoryBytes: Math.round(+f.memory * MB), diskBytes: Math.round(+f.disk * GB) });
const same = (a: DatabaseLimits, b: DatabaseLimits) => a.vcpus === b.vcpus && a.memoryBytes === b.memoryBytes && a.diskBytes === b.diskBytes;

/** The limits of one database server. Only an administrator sets limits that differ from the plan. */
function LimitFields({ value, onChange, errors }: { value: LimitForm; onChange: (v: LimitForm) => void; errors?: Record<string, string> }) {
  return (
    <div className="form-row" style={{ marginTop: 10 }}>
      <Field label="CPU (vCPU)" error={errors?.["limits.vcpus"]}>{(p) => <input {...p} className="input" type="number" min={0.1} step="any" value={value.vcpus} onChange={(e) => onChange({ ...value, vcpus: e.target.value })} />}</Field>
      <Field label="Memory (MB)" error={errors?.["limits.memoryBytes"]}>{(p) => <input {...p} className="input" type="number" min={512} step="any" value={value.memory} onChange={(e) => onChange({ ...value, memory: e.target.value })} />}</Field>
      <Field label="Disk (GB)" error={errors?.["limits.diskBytes"]}>{(p) => <input {...p} className="input" type="number" min={1} step="any" value={value.disk} onChange={(e) => onChange({ ...value, disk: e.target.value })} />}</Field>
    </div>
  );
}

function PlanField({ plans, value, onChange, error }: { plans: Plan[]; value: string; onChange: (id: string) => void; error?: string }) {
  return (
    <Field label="Plan" hint="The plan gives the limits and the number of databases." error={error}>
      {(p) => (
        <select {...p} className="select" value={value} onChange={(e) => onChange(e.target.value)}>
          {plans.map((pl) => <option key={pl.id} value={pl.id}>{pl.name}: {limitsText(pl.databaseLimits)} · {pl.databases} databases</option>)}
        </select>
      )}
    </Field>
  );
}

function CreateDialog({ onClose }: { onClose: () => void }) {
  const state = useStore();
  const admin = state.session.user.role === "administrator";
  const customers = contextCustomers(state);
  const [customerId, setCustomerId] = useState(() => customers.find((c) => c.id === state.session.contextId)?.id ?? customers[0]?.id ?? "");
  const customer = customers.find((c) => c.id === customerId);
  const [engine, setEngine] = useState<DatabaseEngine>("mariadb");
  const info = engineInfo(state.databaseEngines, engine);
  const [version, setVersion] = useState("");
  const [name, setName] = useState("");
  const [planId, setPlanId] = useState(customer?.planId ?? "");
  const [serverId, setServerId] = useState("");
  const [custom, setCustom] = useState(false);
  const plan = state.plans.find((p) => p.id === planId) ?? state.plans.find((p) => p.id === customer?.planId);
  const [limits, setLimits] = useState<LimitForm>(() => toForm(plan?.databaseLimits ?? { vcpus: 1, memoryBytes: 512 * MB, diskBytes: 5 * GB }));
  const create = useAction(createDatabaseServer);
  const selectPlan = canSelectPlan(state, customer);

  // The servers that have a website of the customer. The websites on the same server use the database server.
  const hosts = new Map<string, string[]>();
  for (const w of state.websites) if (w.customerId === customerId) hosts.set(w.serverId, [...(hosts.get(w.serverId) ?? []), w.name]);
  const hostLabel = (id: string, sites: string[]) => {
    const known = state.servers.find((s) => s.id === id)?.name;
    const count = `${sites.length} ${sites.length === 1 ? "website" : "websites"}`;
    return known ? `${known} · ${count}` : `The server of ${sites[0]}${sites.length > 1 ? ` and ${sites.length - 1} more` : ""}`;
  };
  // The name that the panel gives when the field is empty.
  const own = state.databaseServers.filter((s) => s.customerId === customerId);
  let free = "";
  for (let n = 1; ; n++) {
    free = `${engine}-${n}`;
    if (!own.some((s) => s.name === free)) break;
  }
  const allowed = state.plans.find((p) => p.id === customer?.planId)?.databaseServers;
  const full = allowed !== undefined && own.length >= allowed;

  const pickCustomer = (id: string) => {
    setCustomerId(id);
    setServerId("");
    const next = customers.find((c) => c.id === id)?.planId ?? "";
    setPlanId(next);
    const p = state.plans.find((x) => x.id === next);
    if (p) setLimits(toForm(p.databaseLimits));
  };
  const pickPlan = (id: string) => {
    setPlanId(id);
    const p = state.plans.find((x) => x.id === id);
    if (p) setLimits(toForm(p.databaseLimits));
  };

  return (
    <Modal
      wide
      title="New database server"
      onClose={onClose}
      footer={<><button type="button" className="btn" onClick={onClose}>Cancel</button><button type="submit" form="new-dbs" className="btn btn-primary" disabled={create.busy}>Create database server</button></>}
    >
      <form id="new-dbs" noValidate onSubmit={async (e) => {
        e.preventDefault();
        const r = await create.run({ customerId, engine, name, planId: selectPlan ? planId : undefined, serverId: serverId || undefined, limits: admin && custom ? fromForm(limits) : undefined, version: info.versions.length ? version || info.versions[0].version : undefined });
        if (r.ok) onClose();
      }}>
        <FormError error={create.error} shown={["name", "version", "planId", "serverId", "customerId", "limits.vcpus", "limits.memoryBytes", "limits.diskBytes"]} />
        {customers.length > 1 && (
          <Field label="Customer" error={create.error?.fields.customerId}>
            {(p) => (
              <select {...p} className="select" value={customerId} onChange={(e) => pickCustomer(e.target.value)}>
                {customers.map((c) => <option key={c.id} value={c.id}>{c.name}</option>)}
              </select>
            )}
          </Field>
        )}
        {customer && allowed !== undefined && (
          <p className={full ? "text-danger small" : "muted small"} style={{ marginTop: 8 }}>
            {customer.name} has {own.length} of {allowed} database servers.{full && " The plan of the customer allows no more."}
          </p>
        )}
        <fieldset className="radio-cards" style={{ marginTop: 14, gridTemplateColumns: "repeat(auto-fit, minmax(160px, 1fr))" }}>
          <legend>Engine</legend>
          {state.databaseEngines.map((e) => (
            <label className="radio-card" key={e.engine}>
              <input type="radio" name="dbs-engine" checked={engine === e.engine} onChange={() => { setEngine(e.engine); setVersion(""); }} />
              <span><strong>{e.name}</strong><span>{engineUse(e)}</span></span>
            </label>
          ))}
        </fieldset>
        {create.error?.fields.engine && <p className="text-danger small" role="alert" style={{ marginTop: 6 }}>{create.error.fields.engine}</p>}
        {info.versions.length > 0 && (
          <div style={{ marginTop: 14 }}>
            <Field label="Version" hint={<>The database server keeps this version. {info.name} comes from a pack: a database server of it cannot move to a different server, and has no Adminer and no access from anywhere.</>} error={create.error?.fields.version}>
              {(p) => (
                <select {...p} className="select" value={version || info.versions[0].version} onChange={(e) => setVersion(e.target.value)}>
                  {info.versions.map((v) => <option key={v.version} value={v.version}>{info.name} {versionText(v)}</option>)}
                </select>
              )}
            </Field>
          </div>
        )}
        <div style={{ marginTop: 14 }}>
          <Field label="Name" hint={<>Optional. With no name, the panel gives the name <code>{free}</code>. Use lower-case letters, digits and hyphens.</>} error={create.error?.fields.name}>
            {(p) => <input {...p} className="input" value={name} placeholder={free} onChange={(e) => setName(e.target.value)} autoCapitalize="none" spellCheck={false} />}
          </Field>
        </div>
        {(hosts.size > 1 || create.error?.fields.serverId) && (
          <div style={{ marginTop: 14 }}>
            <Field label="Server" hint="The websites of the customer on a different server connect through an encrypted tunnel." error={create.error?.fields.serverId}>
              {(p) => (
                <select {...p} className="select" value={serverId} onChange={(e) => setServerId(e.target.value)}>
                  <option value="">Automatic: the server with the most websites of the customer</option>
                  {[...hosts].map(([id, sites]) => <option key={id} value={id}>{hostLabel(id, sites)}</option>)}
                </select>
              )}
            </Field>
          </div>
        )}
        <div style={{ marginTop: 14 }}>
          {selectPlan
            ? <PlanField plans={state.plans} value={planId} onChange={pickPlan} error={create.error?.fields.planId} />
            : plan && <p>Plan: <strong>{plan.name}</strong>. <span className="muted">{limitsText(plan.databaseLimits)} · {plan.databases} databases. Only an administrator or your reseller can select a different plan.</span></p>}
        </div>
        {admin && (
          <>
            <label className="check" style={{ marginTop: 14 }}><input type="checkbox" checked={custom} onChange={(e) => setCustom(e.target.checked)} />Set limits that are different from the plan</label>
            {custom && <LimitFields value={limits} onChange={setLimits} errors={create.error?.fields} />}
          </>
        )}
      </form>
    </Modal>
  );
}

/**
 * Moves a database server to a different server. The data goes through the encrypted tunnel
 * between the two servers. The database server keeps its name and its network address.
 */
function MoveDialog({ server, onClose }: { server: DatabaseServer; onClose: () => void }) {
  const state = useStore();
  const targets = state.servers.filter((s) => s.id !== server.serverId && s.roles.includes("web")).sort((a, b) => a.name.localeCompare(b.name));
  const [serverId, setServerId] = useState(() => targets.find((s) => s.state === "healthy")?.id ?? targets[0]?.id ?? "");
  const move = useAction(moveDatabaseServer);
  const from = state.servers.find((s) => s.id === server.serverId)?.name ?? "its server";
  // The websites with a database in the database server. Those on the old server lose the name of the engine.
  const websiteIds = new Set(state.databases.filter((d) => d.databaseServerId === server.id).map((d) => d.websiteId));
  const onOld = state.websites.filter((w) => websiteIds.has(w.id) && w.serverId === server.serverId);

  return (
    <Modal
      wide
      title={`Move database server ${server.name}`}
      onClose={onClose}
      footer={<><button type="button" className="btn" onClick={onClose}>Cancel</button><button type="submit" form="move-dbs" className="btn btn-primary" disabled={move.busy || !serverId}>Move database server</button></>}
    >
      <form id="move-dbs" noValidate onSubmit={async (e) => {
        e.preventDefault();
        const r = await move.run(server.id, serverId);
        if (r.ok) onClose();
      }}>
        <FormError error={move.error} shown={["serverId"]} />
        <p>
          The panel moves the database server from {from} to the new server. The data goes through the encrypted tunnel between the two servers. The database server keeps its name, its databases, its users and its network name <code>{server.networkHost ?? server.host}</code>.
        </p>
        <Field label="New server" error={move.error?.fields.serverId}>
          {(p) => (
            <select {...p} className="select" value={serverId} onChange={(e) => setServerId(e.target.value)}>
              {targets.map((s) => <option key={s.id} value={s.id}>{s.name}{s.state !== "healthy" ? ` · ${s.state}` : ""}</option>)}
            </select>
          )}
        </Field>
        {targets.length === 0 && <p className="muted small">There is no other server with the web role.</p>}
        <div className="alert alert-warning alert-stack" style={{ marginTop: 14 }}>
          <p><strong>Before you move it</strong></p>
          <ul className="notice-list">
            <li>The database server does not operate while its data moves. The time depends on the size of the data: about one minute for each 5 GB on a 1 Gbit/s connection.</li>
            <li>
              After the move, a website connects with the network name <code>{server.networkHost ? `${server.networkHost}:${server.networkPort}` : "<name>.internal"}</code>. An app that uses <code>{server.host}:{server.port}</code> loses the connection. Change it to the network name first.
              {onOld.length > 0 && <> These websites are on {from} now: {onOld.map((w) => w.name).join(", ")}.</>}
            </li>
            <li>Each website with a database in the database server must be in a network with it, or on the new server.</li>
          </ul>
        </div>
      </form>
    </Modal>
  );
}

function ChangeDialog({ server, onClose }: { server: DatabaseServer; onClose: () => void }) {
  const state = useStore();
  const admin = state.session.user.role === "administrator";
  const customer = state.customers.find((c) => c.id === server.customerId);
  const [name, setName] = useState(server.name);
  const [planId, setPlanId] = useState(server.planId);
  const [limits, setLimits] = useState<LimitForm>(() => toForm(server.limits));
  const change = useAction(setDatabaseServer);
  const selectPlan = canSelectPlan(state, customer);
  const plan = state.plans.find((p) => p.id === planId);
  // The plans that the list of the user has. The plan of the database server is always in the list.
  const plans = state.plans;
  const pickPlan = (id: string) => {
    setPlanId(id);
    const p = state.plans.find((x) => x.id === id);
    if (p) setLimits(toForm(p.databaseLimits));
  };
  const next = admin ? fromForm(limits) : planId !== server.planId ? plan?.databaseLimits : undefined;
  const restart = next !== undefined && !same(next, server.limits);

  return (
    <Modal
      wide
      title={`Change database server ${server.name}`}
      onClose={onClose}
      footer={<><button type="button" className="btn" onClick={onClose}>Cancel</button><button type="submit" form="edit-dbs" className="btn btn-primary" disabled={change.busy}>Save changes</button></>}
    >
      <form id="edit-dbs" noValidate onSubmit={async (e) => {
        e.preventDefault();
        const body: Parameters<typeof setDatabaseServer>[1] = {};
        if (name.trim() !== server.name) body.name = name.trim();
        if (selectPlan && planId !== server.planId) body.planId = planId;
        if (admin && !same(fromForm(limits), server.limits)) body.limits = fromForm(limits);
        if (Object.keys(body).length === 0) return onClose();
        const r = await change.run(server.id, body);
        if (r.ok) onClose();
      }}>
        <FormError error={change.error} shown={["name", "planId", "limits.vcpus", "limits.memoryBytes", "limits.diskBytes"]} />
        <Field label="Name" hint="Use lower-case letters, digits and hyphens. The host name and the port stay the same." error={change.error?.fields.name}>
          {(p) => <input {...p} className="input" value={name} onChange={(e) => setName(e.target.value)} autoCapitalize="none" spellCheck={false} />}
        </Field>
        <div style={{ marginTop: 14 }}>
          {selectPlan
            ? <PlanField plans={plans} value={planId} onChange={pickPlan} error={change.error?.fields.planId} />
            : <p>Plan: <strong>{plan?.name ?? "Other plan"}</strong>. <span className="muted">{limitsText(server.limits)} · {server.maxDatabases} databases. Only an administrator or your reseller can select a different plan.</span></p>}
        </div>
        {admin && (
          <fieldset className="form-section">
            <legend>Limits</legend>
            <p className="hint">A new plan gives its limits. You can set limits that are different from the plan.</p>
            <LimitFields value={limits} onChange={setLimits} errors={change.error?.fields} />
          </fieldset>
        )}
        {restart && (
          <p className="muted small" style={{ marginTop: 12 }}>
            The panel restarts the database server to apply the new limits. The websites cannot connect for a short time. The server does not make the disk smaller than the data on it: then the database server keeps its limits.
          </p>
        )}
      </form>
    </Modal>
  );
}
