import { useEffect, useState } from "react";
import { Link, useNavigate, useParams, useSearchParams } from "react-router-dom";
import { useAction, useStore } from "../api/hooks";
import { LIVE } from "../api/mode";
import { addServer, installUpdates, newJoinToken, rebootServer, setServer, updateAgent } from "../api/store";
import type { HostUpdatePolicy, Server, ServerRole } from "../api/types";
import { Topbar } from "../components/Layout";
import { CopyField } from "./website/AccessTab";
import { Card, ConfirmButton, Empty, Field, FormError, JobRow, Meter, Modal, PageHeader, Status } from "../components/ui";
import { relativeTime } from "../lib/format";
import { HOST_POLICY_LABEL, roleList, SERVER_ROLE, SERVER_STATE } from "../lib/labels";

function serverStatus(s: Server) {
  const st = SERVER_STATE[s.state];
  return <Status tone={st.tone}>{s.stateDetail && s.state !== "healthy" ? s.stateDetail : st.label}</Status>;
}

export function ServersTable() {
  const state = useStore();
  return (
    <div className="table-wrap">
      <table>
        <thead><tr><th scope="col">Server</th><th scope="col">Location</th><th scope="col">Roles</th><th scope="col">Agent</th><th scope="col">CPU</th><th scope="col">Status</th></tr></thead>
        <tbody>
          {state.servers.map((s) => (
            <tr key={s.id}>
              <td><Link className="cell-title mono" to={`/servers/${s.id}`}>{s.name}</Link></td>
              <td>{s.location}</td>
              <td>{roleList(s.roles)}</td>
              <td><code>{s.agentVersion}</code></td>
              <td>{s.cpuPercent}%</td>
              <td>{serverStatus(s)}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

const ROLES: ServerRole[] = ["web", "database", "email", "dns", "backup"];

function AddServerDialog({ onClose }: { onClose: () => void }) {
  const state = useStore();
  const navigate = useNavigate();
  const [name, setName] = useState("");
  const [location, setLocation] = useState("");
  const [roles, setRoles] = useState<ServerRole[]>(["web"]);
  const add = useAction(addServer);
  // In live mode each dialog gets a new token, because the panel shows a token only once.
  useEffect(() => { if (LIVE) void newJoinToken(); }, []);
  const name_ = name.trim().toLowerCase();
  const cmd = LIVE
    ? `curl -fsSL https://get.cloudfactory.io | sh -s -- --join ${state.joinToken || "…"} --panel https://${window.location.host}${name_ ? ` --name ${name_}` : ""} --roles ${roles.join(",") || "web"}`
    : `curl -fsSL https://get.cloudfactory.io | sh -s -- --join ${state.joinToken}`;
  return (
    <Modal
      title="Add server"
      wide
      onClose={onClose}
      footer={<><button type="button" className="btn" onClick={onClose}>Cancel</button><button type="submit" form="srv" className="btn btn-primary">Wait for the server</button></>}
    >
      <form id="srv" noValidate onSubmit={async (e) => {
        e.preventDefault();
        const r = await add.run(name, location, roles);
        if (r.ok) navigate(r.value.serverId ? `/servers/${r.value.serverId}` : "/servers");
      }}>
        <FormError error={add.error} />
        <p>1. Get a new server with Debian 13 or Ubuntu 26.04 LTS, on x86-64 or ARM64.</p>
        {LIVE && <p>2. Give the server a name and roles. The command below changes to match.</p>}
        <p>{LIVE ? "3" : "2"}. Sign in to the server as root and run this command. The script checks the system first. It does not change the system until all checks pass.</p>
        <CopyField value={cmd} label="Join command" />
        <p className="muted small">The token is for one server and expires in 24 hours. <button type="button" className="btn-link" onClick={() => void newJoinToken()}>Make a new token</button></p>
        {!LIVE && <p>3. Give the server a name and roles. The panel shows it when the agent connects.</p>}
        <div className="form-row">
          <Field label="Name" error={add.error?.fields.name}>{(p) => <input {...p} className="input mono" value={name} onChange={(e) => setName(e.target.value)} placeholder="web-03" autoCapitalize="none" />}</Field>
          {!LIVE && <Field label="Location">{(p) => <input {...p} className="input" value={location} onChange={(e) => setLocation(e.target.value)} placeholder="London" />}</Field>}
        </div>
        <fieldset className="checkbox-list">
          <legend>Roles</legend>
          {ROLES.map((r) => (
            <label key={r} className="check">
              <input type="checkbox" checked={roles.includes(r)} onChange={(e) => setRoles(e.target.checked ? [...roles, r] : roles.filter((x) => x !== r))} />
              {SERVER_ROLE[r].charAt(0).toUpperCase() + SERVER_ROLE[r].slice(1)}
            </label>
          ))}
        </fieldset>
        {add.error?.fields.roles && <p className="error text-danger">{add.error.fields.roles}</p>}
      </form>
    </Modal>
  );
}

export function ServersPage() {
  const { id } = useParams();
  const [params, setParams] = useSearchParams();
  const state = useStore();
  if (id) return <ServerDetail id={id} />;
  const updates = state.servers.reduce((n, s) => n + s.pendingUpdates.length, 0);
  const reboots = state.servers.filter((s) => s.rebootRequired).length;
  return (
    <>
      <Topbar placeholder="Search servers, images, customers and jobs" />
      <main id="main" className="content content-wide">
        <PageHeader
          title="Servers"
          description="One panel controls all servers. Each server has one or more roles. Only the operating system, systemd, Podman, gVisor and the agent operate directly on the host."
          actions={<button type="button" className="btn btn-primary" onClick={() => setParams({ add: "1" })}>Add server</button>}
        />
        <div className="stats">
          <section className="card stat"><h2 className="stat-label">Servers</h2><p className="stat-value">{state.servers.length}</p></section>
          <section className="card stat"><h2 className="stat-label">Healthy</h2><p className="stat-value">{state.servers.filter((s) => s.state === "healthy").length}</p></section>
          <section className="card stat"><h2 className="stat-label">Pending OS updates</h2><p className="stat-value">{updates}</p></section>
          <section className="card stat"><h2 className="stat-label">Need a reboot</h2><p className="stat-value">{reboots}</p></section>
        </div>
        <Card title="All servers"><ServersTable /></Card>
      </main>
      {params.get("add") && <AddServerDialog onClose={() => setParams({})} />}
    </>
  );
}

function ServerDetail({ id }: { id: string }) {
  const state = useStore();
  const server = state.servers.find((s) => s.id === id);
  const install = useAction(installUpdates);
  const reboot = useAction(rebootServer);
  const settings = useAction(setServer);
  const [windowText, setWindowText] = useState(server?.maintenanceWindow ?? "");
  if (!server) {
    return <><Topbar /><main id="main" className="content"><Empty title="This server does not exist"><p><Link to="/servers">Go to all servers</Link></p></Empty></main></>;
  }
  const jobs = state.jobs.filter((j) => j.serverId === server.id).slice(0, 5);
  const busy = jobs.some((j) => j.state === "running");
  const sites = state.websites.filter((w) => w.serverId === server.id);
  return (
    <>
      <Topbar placeholder="Search servers, images, customers and jobs" />
      <main id="main" className="content content-wide">
        <ol className="breadcrumbs"><li><Link to="/servers">Servers</Link></li><li aria-current="page">{server.name}</li></ol>
        <div className="detail-header">
          <div>
            <div className="detail-title"><h1 className="mono">{server.name}</h1>{serverStatus(server)}</div>
            <p className="detail-meta">{server.location} · {roleList(server.roles)} · {server.os} · {server.arch} · agent {server.agentVersion}</p>
          </div>
          <div className="btn-group">
            {server.agentVersion !== "0.9.4" && <button type="button" className="btn" disabled={busy} onClick={() => void updateAgent(server.id)}>Update agent</button>}
            <ConfirmButton className="btn" danger={false} title={`Reboot ${server.name}?`} confirm="Reboot now" message={<p>The websites on this server stop for less than 3 minutes. The panel reboots only one server of each role at a time. To reboot in the maintenance window ({server.maintenanceWindow}), wait for the planned reboot.</p>} onConfirm={() => void reboot.run(server.id)}>
              Reboot now
            </ConfirmButton>
          </div>
        </div>
        <FormError error={reboot.error ?? install.error ?? settings.error} />
        <div className="detail-grid mt">
          <div className="stack">
            <Card title="Operating system updates" actions={server.pendingUpdates.length > 0 && <button type="button" className="btn btn-primary" disabled={busy} onClick={() => void install.run(server.id)}>Install updates now</button>}>
              <div className="card-body">
                <table className="kv">
                  <tbody>
                    <tr><th scope="row">Pending</th><td>{server.pendingUpdates.length} ({server.pendingUpdates.filter((p) => p.security).length} security)</td></tr>
                    <tr><th scope="row">Last update</th><td>{relativeTime(server.lastUpdateAt)}</td></tr>
                    <tr><th scope="row">Reboot</th><td>{server.rebootRequired ? <Status tone="warning">Needed. Planned in the maintenance window.</Status> : <Status tone="success">Not needed</Status>}</td></tr>
                    <tr><th scope="row">Kernel</th><td><code>{server.kernel}</code></td></tr>
                  </tbody>
                </table>
              </div>
              {server.pendingUpdates.length > 0 && (
                <div className="table-wrap">
                  <table>
                    <thead><tr><th scope="col">Package</th><th scope="col">From</th><th scope="col">To</th><th scope="col">Type</th></tr></thead>
                    <tbody>
                      {server.pendingUpdates.map((p) => (
                        <tr key={p.name}><td><code>{p.name}</code></td><td><code>{p.from}</code></td><td><code>{p.to}</code></td><td>{p.security ? <span className="badge badge-warning">Security</span> : "Other"}</td></tr>
                      ))}
                    </tbody>
                  </table>
                </div>
              )}
            </Card>
            <Card title="Update policy">
              <div className="card-body">
                <Field label="Host updates" hint="Security updates install each day, in less than 24 hours after their release.">
                  {(p) => (
                    <select {...p} className="select" value={server.updatePolicy} onChange={(e) => void settings.run(server.id, { updatePolicy: e.target.value as HostUpdatePolicy })}>
                      {Object.entries(HOST_POLICY_LABEL).map(([k, v]) => <option key={k} value={k}>{v}</option>)}
                    </select>
                  )}
                </Field>
                <form className="inline-form" style={{ marginTop: 14 }} onSubmit={(e) => { e.preventDefault(); void settings.run(server.id, { maintenanceWindow: windowText }); }}>
                  <Field label="Maintenance window" hint="The agent reboots and restarts services only in this window.">
                    {(p) => <input {...p} className="input" value={windowText} onChange={(e) => setWindowText(e.target.value)} />}
                  </Field>
                  <div className="field"><span className="label" aria-hidden="true">&nbsp;</span><button type="submit" className="btn">Save</button></div>
                </form>
              </div>
            </Card>
          </div>
          <div className="stack">
            <Card title="Jobs">
              <div className="card-body">{jobs.length ? <ul className="job-list">{jobs.map((j) => <JobRow key={j.id} job={j} />)}</ul> : <p className="muted">No jobs yet.</p>}</div>
            </Card>
            <Card title="Use">
              <div className="card-body">
                {([["CPU", server.cpuPercent], ["Memory", server.memoryPercent], ["Disk", server.diskPercent]] as const).map(([l, v]) => (
                  <div className="resource" key={l}><div className="row"><span>{l}</span><span>{v}%</span></div><Meter value={v / 100} label={`${l} ${v}%`} tone={v >= 90 ? "danger" : v >= 75 ? "warning" : undefined} /></div>
                ))}
                <p className="muted small" style={{ marginTop: 14 }}>{sites.length} websites on this server.</p>
              </div>
            </Card>
            <Card title="Container tools">
              <div className="card-body">
                <table className="kv">
                  <tbody>
                    <tr><th scope="row">Podman</th><td><code>{server.podmanVersion}</code> <span className="muted small">held with apt-mark</span></td></tr>
                    <tr><th scope="row">gVisor</th><td><code>{server.gvisorVersion}</code> <span className="muted small">SHA-512 checked</span></td></tr>
                    <tr>
                      <th scope="row">Platform</th>
                      <td>
                        <label className="visually-hidden" htmlFor="platform">gVisor platform</label>
                        <select id="platform" className="select" value={server.gvisorPlatform} onChange={(e) => void settings.run(server.id, { gvisorPlatform: e.target.value as "systrap" | "kvm" })}>
                          <option value="systrap">systrap (default)</option>
                          <option value="kvm" disabled={!server.bareMetal}>KVM (bare metal only)</option>
                        </select>
                      </td>
                    </tr>
                  </tbody>
                </table>
              </div>
            </Card>
            <Card title="Services">
              <div className="card-body">
                <ul className="list">
                  {server.services.map((s) => (
                    <li key={s.name}><span>{s.name}</span><Status tone={s.state === "running" ? "success" : "danger"}>{s.state === "running" ? "Running" : s.state === "stopped" ? "Stopped" : "Failed"}</Status></li>
                  ))}
                </ul>
              </div>
            </Card>
          </div>
        </div>
      </main>
    </>
  );
}
