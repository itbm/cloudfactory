import { useEffect, useState } from "react";
import { Link, useNavigate, useParams, useSearchParams } from "react-router-dom";
import { useAction, useStore } from "../api/hooks";
import { LIVE } from "../api/mode";
import { failedServices } from "../api/selectors";
import { addServer, checkConnection, installUpdates, loopbackName, newJoinToken, rebootServer, removeServer, setServer, updateAgent } from "../api/store";
import type { HostUpdatePolicy, Server, ServerRole } from "../api/types";
import { Topbar } from "../components/Layout";
import { CopyField } from "./website/AccessTab";
import { Card, ConfirmButton, Empty, Field, FormError, JobRow, Meter, Modal, PageHeader, Status } from "../components/ui";
import { relativeTime } from "../lib/format";
import { HOST_POLICY_LABEL, jobActive, roleList, SERVER_ROLE, SERVER_STATE } from "../lib/labels";

/** The command that shows why a service does not run. The edge proxy and the panel are services of their own users. */
function serviceCommand(name: string): string {
  return name === "cf-edge.service" ? "journalctl _SYSTEMD_USER_UNIT=cf-edge.service -n 50" : `systemctl status ${name}`;
}

function serverStatus(s: Server) {
  const st = SERVER_STATE[s.state];
  return <Status tone={st.tone}>{s.stateDetail && s.state !== "healthy" ? s.stateDetail : st.label}</Status>;
}

export function ServersTable() {
  const state = useStore();
  return (
    <div className="table-wrap">
      <table>
        <thead><tr><th scope="col">Server</th><th scope="col">Location</th><th scope="col">Roles</th><th scope="col">Agent</th><th scope="col">CPU</th><th scope="col">OS updates</th><th scope="col">Status</th><th scope="col"><span className="visually-hidden">Actions</span></th></tr></thead>
        <tbody>
          {state.servers.map((s) => (
            <tr key={s.id}>
              <td><Link className="cell-title mono" to={`/servers/${s.id}`}>{s.name}</Link></td>
              <td>{s.location || "–"}</td>
              <td>{roleList(s.roles)}</td>
              <td><code>{s.agentVersion}</code></td>
              <td>{s.cpuPercent}%</td>
              <td className="nowrap">{s.pendingUpdates.length > 0 ? <Link to={`/servers/${s.id}`}>{s.pendingUpdates.length} pending</Link> : "None"}</td>
              <td>{serverStatus(s)}</td>
              {/* The name is also a link, but a button shows more clearly that the server has its own page. */}
              <td style={{ textAlign: "right" }}><Link className="btn btn-sm" to={`/servers/${s.id}`}>Manage<span className="visually-hidden"> {s.name}</span></Link></td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

/** R-14: the signed installation script from GitHub Releases, as in the README. */
const INSTALL_URL = "https://github.com/itbm/cloudfactory/releases/latest/download/install.sh";

/**
 * The command that adds a new server to the real panel. The panel gives the installation script
 * of its own release. The new server trusts the panel CA by the fingerprint of its root certificate.
 */
export function joinCommand(p: { installUrl?: string; token: string; panelHost: string; caSha256?: string; name: string; roles: string[] }): string {
  return `curl -fsSL ${p.installUrl ?? INSTALL_URL} | sh -s -- --join ${p.token} --panel https://${p.panelHost}${p.caSha256 ? ` --panel-ca-sha256 ${p.caSha256}` : ""}${p.name ? ` --name ${p.name}` : ""} --roles ${p.roles.join(",") || "web"}`;
}

const ROLES: ServerRole[] = ["web", "database", "email", "dns", "backup"];

function AddServerDialog({ onClose }: { onClose: () => void }) {
  const state = useStore();
  const navigate = useNavigate();
  const [name, setName] = useState("");
  const [location, setLocation] = useState("");
  const [roles, setRoles] = useState<ServerRole[]>(["web"]);
  const add = useAction(addServer);
  const token = useAction(newJoinToken);
  const makeToken = token.run;
  // In live mode each dialog gets a new token, because the panel shows a token only once.
  useEffect(() => { if (LIVE) void makeToken(); }, [makeToken]);
  const name_ = name.trim().toLowerCase();
  const cmd = LIVE
    ? joinCommand({ installUrl: state.joinInstallUrl, token: state.joinToken, panelHost: window.location.host, caSha256: state.joinCaSha256, name: name_, roles })
    : `curl -fsSL ${INSTALL_URL} | sh -s -- --join ${state.joinToken}`;
  // A different server cannot connect to a name such as panel.localhost.
  const local = LIVE && loopbackName(window.location.hostname);
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
        {local && (
          <div className="alert alert-warning" role="status">
            <p><strong>A different server cannot connect to <code>{window.location.hostname}</code>.</strong> This name is only for the computer of the panel, so the command below fails on a new server. To add servers, install the panel with a domain or an address that the new server can reach.</p>
          </div>
        )}
        {/* The panel shows a token only once. Before the token comes, there is no command to copy. */}
        {LIVE && !state.joinToken
          ? <p className="muted" role="status">{token.error ? "The panel could not make a token." : "The panel makes a token…"}</p>
          : <CopyField value={cmd} label="Join command" />}
        <p className="muted small">The token is for one server and expires in 24 hours. <button type="button" className="btn-link" disabled={token.busy} onClick={() => void token.run()}>Make a new token</button></p>
        {LIVE && <p className="muted small">The panel must also connect to the new server, on port 7443. If the new server is behind NAT, add <code>--address</code> with its public IP address to the command.</p>}
        <FormError error={token.error} />
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

/** The number of packages that the server page shows before the user asks for all. */
const PACKAGE_ROWS = 8;

function ServerDetail({ id }: { id: string }) {
  const state = useStore();
  const server = state.servers.find((s) => s.id === id);
  const install = useAction(installUpdates);
  const reboot = useAction(rebootServer);
  const settings = useAction(setServer);
  const agent = useAction(updateAgent);
  const connection = useAction(checkConnection);
  const remove = useAction(removeServer);
  const navigate = useNavigate();
  const [windowText, setWindowText] = useState(server?.maintenanceWindow ?? "");
  const [allPackages, setAllPackages] = useState(false);
  if (!server) {
    return <><Topbar /><main id="main" className="content"><Empty title="This server does not exist"><p><Link to="/servers">Go to all servers</Link></p></Empty></main></>;
  }
  const jobs = state.jobs.filter((j) => j.serverId === server.id).slice(0, 5);
  const busy = jobs.some(jobActive);
  const sites = state.websites.filter((w) => w.serverId === server.id);
  // FR-1113: the container tools are held. The normal updates do not install them.
  const osUpdates = server.pendingUpdates.filter((p) => !p.containerTool);
  const toolUpdates = server.pendingUpdates.filter((p) => p.containerTool);
  // The security updates come first. A long list shows only its first rows until the user asks for all.
  const packages = [...osUpdates].sort((a, b) => Number(b.security) - Number(a.security));
  const securityCount = osUpdates.filter((p) => p.security).length;
  const otherCount = osUpdates.length - securityCount;
  const allPolicy = server.updatePolicy === "all";
  const failed = failedServices(server.services);
  return (
    <>
      <Topbar placeholder="Search servers, images, customers and jobs" />
      <main id="main" className="content content-wide">
        <ol className="breadcrumbs"><li><Link to="/servers">Servers</Link></li><li aria-current="page">{server.name}</li></ol>
        <div className="detail-header">
          <div>
            <div className="detail-title"><h1 className="mono">{server.name}</h1>{serverStatus(server)}</div>
            <p className="detail-meta">{[server.location, roleList(server.roles), server.os, server.arch].filter(Boolean).join(" · ")} · agent {server.agentVersion}</p>
          </div>
          <div className="btn-group">
            {(LIVE ? server.agentUpdateAvailable : server.agentVersion !== "0.9.4") && <button type="button" className="btn" disabled={busy || agent.busy} onClick={() => void agent.run(server.id)}>Update agent</button>}
            <ConfirmButton className="btn" danger={false} title={`Reboot ${server.name}?`} confirm="Reboot now" message={<p>The websites on this server stop for less than 3 minutes. The panel reboots only one server of each role at a time. To reboot in the maintenance window ({server.maintenanceWindow}), wait for the planned reboot.</p>} onConfirm={() => void reboot.run(server.id)}>
              Reboot now
            </ConfirmButton>
          </div>
        </div>
        <FormError error={reboot.error ?? install.error ?? settings.error ?? agent.error ?? connection.error ?? remove.error} />
        {server.connectionError && (
          <div className="alert alert-danger alert-stack mt" role="alert">
            <p><strong>The panel cannot connect to this server.</strong> The agent of the server sends its reports, but the panel cannot connect to the agent at <code>{server.agentAddress ?? server.name}</code>: {server.connectionError}. Each job for this server fails until the connection works.</p>
            <ul style={{ paddingLeft: 20 }}>
              <li>Make sure that the panel server can find this address in the DNS, and that port 7443 of the server is open for the panel. Then select <strong>Test the connection</strong>.</li>
              <li>If the address is wrong, remove the server from the panel. Then, on the server, delete <code>/etc/cloudfactory/agent.json</code> and run the join command again with <code>--address</code> and the IP address of the server.</li>
            </ul>
            <p><button type="button" className="btn" disabled={connection.busy} onClick={() => void connection.run(server.id)}>{connection.busy ? "Testing…" : "Test the connection"}</button></p>
          </div>
        )}
        {server.state === "degraded" && !server.connectionError && (
          <div className="alert alert-warning alert-stack mt" role="status">
            <p><strong>This server is degraded.</strong>{" "}
              {failed.length
                ? <>{failed.length === 1 ? "A necessary service does not run." : "Some necessary services do not run."} The websites on the server can be in operation, but a part of the server does not do its work.</>
                : "The last job of this server failed. Look at the jobs of the server."}
            </p>
            {failed.length > 0 && (
              <ul style={{ paddingLeft: 20 }}>
                {failed.map((s) => <li key={s.name}><code>{s.name}</code> is {s.state}.{s.detail ? ` ${s.detail}` : ` To see the cause, run on the server: ${serviceCommand(s.name)}`}</li>)}
              </ul>
            )}
          </div>
        )}
        {server.state === "offline" && (
          <div className="alert alert-danger mt" role="status">
            <p><strong>This server is offline.</strong> The agent sent no report in the last 5 minutes. Make sure that the server is on and that <code>cf-agent</code> runs: <code>systemctl status cf-agent</code>.</p>
          </div>
        )}
        <div className="detail-grid mt">
          <div className="stack">
            <Card
              title="Operating system updates"
              actions={osUpdates.length > 0 && (
                <div className="btn-group">
                  {/* With the policy "all", one action installs all packages. With the other policies, the server installs only the security updates unless the administrator asks for all. */}
                  {!allPolicy && otherCount > 0 && <button type="button" className={securityCount > 0 ? "btn" : "btn btn-primary"} disabled={busy} onClick={() => void install.run(server.id, "all")}>Install all updates</button>}
                  {allPolicy && <button type="button" className="btn btn-primary" disabled={busy} onClick={() => void install.run(server.id, "all")}>Install updates now</button>}
                  {!allPolicy && securityCount > 0 && <button type="button" className="btn btn-primary" disabled={busy} onClick={() => void install.run(server.id, "security")}>Install security updates</button>}
                </div>
              )}
            >
              <div className="card-body">
                <table className="kv">
                  <tbody>
                    <tr><th scope="row">Pending</th><td>{osUpdates.length} ({securityCount} security){!allPolicy && otherCount > 0 && <span className="cell-sub">The update policy of this server installs only security updates. The other {otherCount === 1 ? "package stays" : `${otherCount} packages stay`} until you select Install all updates.</span>}</td></tr>
                    <tr><th scope="row">Last update</th><td>{server.lastUpdateAt ? relativeTime(server.lastUpdateAt) : "Never"}</td></tr>
                    <tr><th scope="row">Reboot</th><td>{server.rebootRequired ? <Status tone="warning">Needed. Planned in the maintenance window.</Status> : <Status tone="success">Not needed</Status>}</td></tr>
                    <tr><th scope="row">Kernel</th><td><code>{server.kernel}</code></td></tr>
                  </tbody>
                </table>
              </div>
              {osUpdates.length > 0 && (
                <div className="table-wrap">
                  <table>
                    <thead><tr><th scope="col">Package</th><th scope="col">New version</th><th scope="col">Type</th></tr></thead>
                    <tbody>
                      {(allPackages ? packages : packages.slice(0, PACKAGE_ROWS)).map((p) => (
                        <tr key={p.name}>
                          <td><code>{p.name}</code></td>
                          <td><code>{p.to}</code>{p.from && <span className="cell-sub">from <code>{p.from}</code></span>}</td>
                          <td>{p.security ? <span className="badge badge-warning">Security</span> : "Other"}</td>
                        </tr>
                      ))}
                    </tbody>
                  </table>
                </div>
              )}
              {packages.length > PACKAGE_ROWS && (
                <div className="card-body">
                  <button type="button" className="btn btn-sm" aria-expanded={allPackages} onClick={() => setAllPackages(!allPackages)}>
                    {allPackages ? "Show fewer packages" : `Show all ${packages.length} packages`}
                  </button>
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
            {!server.panel && (
              <Card title="Remove this server">
                <div className="card-body">
                  <p className="muted">
                    {sites.length > 0
                      ? `This server has ${sites.length} ${sites.length === 1 ? "website" : "websites"}. Delete the websites before you remove the server.`
                      : "The panel removes the server and its jobs from its lists. It does not change the server itself."}
                  </p>
                  <p style={{ marginTop: 12 }}>
                    <ConfirmButton
                      className="btn btn-danger"
                      disabled={sites.length > 0 || busy || remove.busy}
                      title={`Remove ${server.name}?`}
                      confirm="Remove server"
                      message={<p>The panel removes <code>{server.name}</code> and its jobs. The agent on the server continues to run, but the panel does not accept its reports. To add the server again, delete <code>/etc/cloudfactory/agent.json</code> on the server and run a new join command.</p>}
                      onConfirm={async () => {
                        const r = await remove.run(server.id);
                        if (r.ok) navigate("/servers");
                      }}
                    >
                      Remove server
                    </ConfirmButton>
                  </p>
                </div>
              </Card>
            )}
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
                <p className="muted small" style={{ marginTop: 14 }}>{sites.length} {sites.length === 1 ? "website" : "websites"} on this server.</p>
              </div>
            </Card>
            <Card title="Container tools" actions={toolUpdates.length > 0 && (
              <ConfirmButton className="btn" danger={false} title={`Update the container tools of ${server.name}?`} confirm="Update container tools" message={<p>The agent installs {toolUpdates.map((p) => p.name).join(", ")} and holds them again. It restarts the website containers one at a time. Do this in the maintenance window.</p>} onConfirm={() => void install.run(server.id, "tools")}>
                Update container tools
              </ConfirmButton>
            )}>
              <div className="card-body">
                <table className="kv">
                  <tbody>
                    <tr><th scope="row">Podman</th><td><code>{server.podmanVersion}</code> <span className="muted small">held with apt-mark</span></td></tr>
                    <tr><th scope="row">gVisor</th><td><code>{server.gvisorVersion}</code> <span className="muted small">SHA-512 checked. The agent installs a new version when it updates.</span></td></tr>
                    {toolUpdates.map((p) => <tr key={p.name}><th scope="row">{p.name}</th><td><code>{p.from}</code> → <code>{p.to}</code> <span className="badge badge-info">Update ready</span></td></tr>)}
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
                    <li key={s.name}>
                      <span>{s.name}{s.optional && <span className="muted small"> (optional)</span>}{s.state !== "running" && s.detail && <span className="cell-sub">{s.detail}</span>}</span>
                      {s.state === "running" ? <Status tone="success">Running</Status> : s.optional ? <Status tone="neutral">Off</Status> : <Status tone="danger">{s.state === "stopped" ? "Stopped" : "Failed"}</Status>}
                    </li>
                  ))}
                </ul>
                <p className="muted small" style={{ marginTop: 14 }}>
                  {server.sftpPort === 22
                    ? "Website SFTP uses the SSH service of this server on port 22. Update the agent to move SFTP to its own container."
                    : <>Website SFTP: port <code>{server.sftpPort}</code>, in its own gVisor container. Port 22 is only for the administrators.</>}
                </p>
              </div>
            </Card>
          </div>
        </div>
      </main>
    </>
  );
}
