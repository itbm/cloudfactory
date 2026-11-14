import { useState } from "react";
import { Link } from "react-router-dom";
import { useAction, useStore } from "../api/hooks";
import { makeTunnel, removeTunnel, repairTunnel, rotateTunnelKey, setMesh, setTunnelAddress } from "../api/store";
import type { Mesh, MeshServer, MeshTunnel, Tone } from "../api/types";
import { Topbar } from "../components/Layout";
import { Card, Empty, Field, FormError, JobRow, Modal, PageHeader, RowMenu, Status, TableWrap } from "../components/ui";
import { relativeTime } from "../lib/format";
import { jobActive } from "../lib/labels";

/** The actions of the agent that change the mesh. */
const MESH_ACTIONS = ["SetMesh", "RotateMeshKey"];

const TUNNEL_STATE: Record<MeshTunnel["state"], { label: string; tone: Tone }> = {
  up: { label: "Up", tone: "success" },
  down: { label: "Down", tone: "danger" },
  waiting: { label: "Waiting for the servers", tone: "info" },
};

/** The names of the two servers of a tunnel, for example "web-1 and web-2". */
export const tunnelName = (t: MeshTunnel) => t.sides.map((s) => s.server ?? "a removed server").join(" and ");

/** The time of the most recent handshake that the two servers reported. */
function lastHandshake(t: MeshTunnel): string | null {
  return t.sides.map((s) => s.handshakeAt).filter((at): at is string => !!at).sort().at(-1) ?? null;
}

/** The server that does not get an answer through the tunnel, as a sentence. */
function downDetail(t: MeshTunnel): string {
  const silent = t.sides.filter((s) => s.reachable === false).map((s) => s.server);
  const unknown = t.sides.filter((s) => s.reachable === null).map((s) => s.server);
  if (silent.length === 2) return "The two servers get no answer from each other.";
  if (silent.length === 1) return `${silent[0]} gets no answer through the tunnel.`;
  if (unknown.length > 0) return `${unknown.join(" and ")} did not report the tunnel in the last minutes.`;
  return "";
}

function serverMeshStatus(s: MeshServer) {
  if (!s.supported) return <Status tone="warning">Update the agent</Status>;
  if (!s.current) return <Status tone="info">Gets the new state</Status>;
  if (s.tunnels === 0) return <Status tone="neutral">No tunnels</Status>;
  return <Status tone="success">In the mesh</Status>;
}

function MakeTunnelDialog({ mesh, onClose }: { mesh: Mesh; onClose: () => void }) {
  const able = mesh.servers.filter((s) => s.supported);
  const [a, setA] = useState(able[0]?.id ?? "");
  const [b, setB] = useState(able[1]?.id ?? "");
  const make = useAction(makeTunnel);
  const exists = mesh.tunnels.some((t) => t.sides.some((s) => s.serverId === a) && t.sides.some((s) => s.serverId === b));
  const options = able.map((s) => <option key={s.id} value={s.id}>{s.name}{s.location ? ` (${s.location})` : ""}</option>);
  return (
    <Modal
      title="Make tunnel"
      onClose={onClose}
      footer={<><button type="button" className="btn" onClick={onClose}>Cancel</button><button type="submit" form="tunnel" className="btn btn-primary" disabled={make.busy || !a || !b || a === b || exists}>Make tunnel</button></>}
    >
      <form id="tunnel" noValidate onSubmit={async (e) => {
        e.preventDefault();
        // The dialog closes when the API accepts the request. The page then shows the jobs.
        if ((await make.run(a, b)).ok) onClose();
      }}>
        <p>The panel gives each of the two servers the public key and the address of the other server. The traffic between them goes through the internet, and the tunnel encrypts it.</p>
        <div className="form-row">
          <Field label="First server">{(p) => <select {...p} className="input" value={a} onChange={(e) => setA(e.target.value)}>{options}</select>}</Field>
          <Field label="Second server">{(p) => <select {...p} className="input" value={b} onChange={(e) => setB(e.target.value)}>{options}</select>}</Field>
        </div>
        {a === b && <p className="muted small" role="status">Select two different servers.</p>}
        {a !== b && exists && <p className="muted small" role="status">These two servers have a tunnel already.</p>}
        <p className="muted small">Each server must accept UDP port {mesh.port} from the other server. If a firewall of your provider is in front of a server, open this port there.</p>
        <FormError error={make.error} />
      </form>
    </Modal>
  );
}

function TunnelAddressDialog({ server, onClose }: { server: MeshServer; onClose: () => void }) {
  const [address, setAddress] = useState(server.tunnelAddress ?? "");
  const save = useAction(setTunnelAddress);
  return (
    <Modal
      title={`Tunnel address of ${server.name}`}
      onClose={onClose}
      footer={<><button type="button" className="btn" onClick={onClose}>Cancel</button><button type="submit" form="tunnel-address" className="btn btn-primary" disabled={save.busy}>Save</button></>}
    >
      <form id="tunnel-address" noValidate onSubmit={async (e) => {
        e.preventDefault();
        // An API error stays in the dialog, next to the field.
        if ((await save.run(server.id, address.trim() || null)).ok) onClose();
      }}>
        <p>The other servers send the tunnel traffic for {server.name} to this public IP address. With no address, the panel uses the address that it connects to the agent with.</p>
        <Field label="Public IP address" hint="Empty: the panel finds the address." error={save.error?.fields.tunnelAddress}>
          {(p) => <input {...p} className="input mono" value={address} onChange={(e) => setAddress(e.target.value)} placeholder="203.0.113.10" autoCapitalize="none" />}
        </Field>
        <FormError error={save.error} shown={["tunnelAddress"]} />
      </form>
    </Modal>
  );
}

/** A question before an action that stops tunnels. */
function Confirm({ title, confirm, onConfirm, onClose, children }: { title: string; confirm: string; onConfirm: () => void; onClose: () => void; children: React.ReactNode }) {
  return (
    <Modal
      title={title}
      onClose={onClose}
      footer={<><button type="button" className="btn" onClick={onClose}>Cancel</button><button type="button" className="btn btn-danger-solid" onClick={() => { onClose(); onConfirm(); }}>{confirm}</button></>}
    >
      {children}
    </Modal>
  );
}

function SettingsCard({ mesh }: { mesh: Mesh }) {
  const [range, setRange] = useState(mesh.range);
  const [port, setPort] = useState(String(mesh.port));
  const save = useAction(setMesh);
  const locked = mesh.tunnels.length > 0;
  const changed = range.trim() !== mesh.range || Number(port) !== mesh.port;
  return (
    <Card title="Settings">
      <form className="card-body" noValidate onSubmit={(e) => {
        e.preventDefault();
        void save.run({ range: range.trim(), port: Number(port) });
      }}>
        <div className="form-row">
          <Field label="Address range" hint="A private IPv4 network. Each server gets one address from it." error={save.error?.fields.range}>
            {(p) => <input {...p} className="input mono" value={range} onChange={(e) => setRange(e.target.value)} disabled={locked} autoCapitalize="none" />}
          </Field>
          <Field label="UDP port" hint="The port of the tunnels on each server." error={save.error?.fields.port}>
            {(p) => <input {...p} className="input mono" inputMode="numeric" value={port} onChange={(e) => setPort(e.target.value)} disabled={locked} />}
          </Field>
        </div>
        <FormError error={save.error} shown={["range", "port"]} />
        {locked
          ? <p className="muted small" style={{ margin: "12px 0 0" }}>The servers have addresses from this range. To change the range or the port, remove all tunnels first.</p>
          : <p style={{ margin: "16px 0 0" }}><button type="submit" className="btn btn-primary" disabled={save.busy || !changed}>Save</button></p>}
      </form>
    </Card>
  );
}

export function NetworkPage() {
  const state = useStore();
  const mesh = state.mesh;
  const [dialog, setDialog] = useState<null | { kind: "make" } | { kind: "address" | "rotate"; server: MeshServer } | { kind: "remove"; tunnel: MeshTunnel }>(null);
  const repair = useAction(repairTunnel);
  const remove = useAction(removeTunnel);
  const rotate = useAction(rotateTunnelKey);
  const error = repair.error ?? remove.error ?? rotate.error;

  if (!mesh) {
    return (
      <>
        <Topbar placeholder="Search servers, images, customers and jobs" />
        <main id="main" className="content content-wide">
          <PageHeader title="Network" />
          <Empty title="The panel did not give the network"><p>Load the page again. If the network does not show, update the panel.</p></Empty>
        </main>
      </>
    );
  }

  const jobs = state.jobs.filter((j) => j.action && MESH_ACTIONS.includes(j.action));
  const shownJobs = [...jobs.filter(jobActive), ...jobs.filter((j) => !jobActive(j))].slice(0, 6);
  const busy = jobs.some(jobActive);
  const able = mesh.servers.filter((s) => s.supported).length;
  const old = mesh.servers.filter((s) => !s.supported);

  return (
    <>
      <Topbar placeholder="Search servers, images, customers and jobs" />
      <main id="main" className="content content-wide">
        <PageHeader
          title="Network"
          description="Encrypted tunnels between your servers. A tunnel lets two servers send private traffic to each other through the internet. The panel gives each server the keys of the other servers, monitors each tunnel, and repairs a tunnel that stops."
          actions={<button type="button" className="btn btn-primary" disabled={able < 2} onClick={() => setDialog({ kind: "make" })}>Make tunnel</button>}
        />
        <div className="stats">
          <section className="card stat"><h2 className="stat-label">Servers in the mesh</h2><p className="stat-value">{mesh.servers.filter((s) => s.tunnels > 0).length}</p></section>
          <section className="card stat"><h2 className="stat-label">Tunnels</h2><p className="stat-value">{mesh.tunnels.length}</p></section>
          <section className="card stat"><h2 className="stat-label">Up</h2><p className="stat-value">{mesh.tunnels.filter((t) => t.state === "up").length}</p></section>
          <section className="card stat"><h2 className="stat-label">Down</h2><p className="stat-value">{mesh.tunnels.filter((t) => t.state === "down").length}</p></section>
        </div>
        <div className="stack">
        <FormError error={error} />
        {old.length > 0 && (
          <div className="alert alert-warning" role="status">
            <p><strong>{old.map((s) => s.name).join(", ")} cannot have tunnels.</strong> The agent of {old.length === 1 ? "this server" : "these servers"} is from an earlier release. Update the agent on the page of the server.</p>
          </div>
        )}

        <Card title="Tunnels">
          {mesh.tunnels.length === 0
            ? <div className="card-body"><p className="muted">No tunnels yet. {able < 2 ? "A tunnel needs two servers with a current agent." : "Make a tunnel between two servers."}</p></div>
            : (
              <TableWrap>
                <table>
                  <thead><tr><th scope="col">Servers</th><th scope="col">State</th><th scope="col">Last handshake</th><th scope="col">Round-trip time</th><th scope="col"><span className="visually-hidden">Actions</span></th></tr></thead>
                  <tbody>
                    {mesh.tunnels.map((t) => {
                      const st = TUNNEL_STATE[t.state];
                      const handshake = lastHandshake(t);
                      return (
                        <tr key={t.id}>
                          <td><span className="cell-title mono">{tunnelName(t)}</span></td>
                          <td><Status tone={st.tone}>{st.label}</Status>{t.state === "down" && downDetail(t) && <div className="cell-sub">{downDetail(t)}</div>}</td>
                          <td className="nowrap">{handshake ? relativeTime(handshake) : "Never"}</td>
                          <td className="nowrap">{t.roundTripMs === null ? "–" : `${t.roundTripMs.toLocaleString("en-GB", { maximumFractionDigits: 1 })} ms`}</td>
                          <td className="cell-actions">
                            <RowMenu label={`Actions for the tunnel between ${tunnelName(t)}`} actions={[
                              { label: "Repair", onSelect: () => void repair.run(t.id), disabled: busy },
                              { label: "Remove", onSelect: () => setDialog({ kind: "remove", tunnel: t }), danger: true, disabled: busy },
                            ]} />
                          </td>
                        </tr>
                      );
                    })}
                  </tbody>
                </table>
              </TableWrap>
            )}
        </Card>

        <Card title="Servers">
          <TableWrap>
            <table>
              <thead><tr><th scope="col">Server</th><th scope="col">Mesh address</th><th scope="col">Tunnel address</th><th scope="col">Public key</th><th scope="col">Tunnels</th><th scope="col">Status</th><th scope="col"><span className="visually-hidden">Actions</span></th></tr></thead>
              <tbody>
                {mesh.servers.map((s) => (
                  <tr key={s.id}>
                    <td><Link className="cell-title mono" to={`/servers/${s.id}`}>{s.name}</Link>{s.location && <div className="cell-sub">{s.location}</div>}</td>
                    <td>{s.address ? <code>{s.address}</code> : "–"}</td>
                    <td>{s.tunnelAddress ? <code>{s.tunnelAddress}</code> : <span className="muted">Automatic</span>}</td>
                    {/* The start of the key is sufficient to compare it with "wg show" on the server. */}
                    <td>{s.publicKey ? <code title={s.publicKey}>{s.publicKey.slice(0, 12)}…</code> : "–"}</td>
                    <td>{s.tunnels}</td>
                    <td>{serverMeshStatus(s)}</td>
                    <td className="cell-actions">
                      <RowMenu label={`Actions for ${s.name}`} actions={[
                        { label: "Set tunnel address", onSelect: () => setDialog({ kind: "address", server: s }) },
                        { label: "Make new key", onSelect: () => setDialog({ kind: "rotate", server: s }), disabled: !s.supported || busy },
                      ]} />
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </TableWrap>
        </Card>

        <div className="grid-2">
          <SettingsCard key={`${mesh.range} ${mesh.port}`} mesh={mesh} />
          <Card title="Jobs">
            <div className="card-body">{shownJobs.length ? <ul className="job-list">{shownJobs.map((j) => <JobRow key={j.id} job={j} />)}</ul> : <p className="muted">No jobs yet.</p>}</div>
          </Card>
        </div>
        </div>
      </main>

      {dialog?.kind === "make" && <MakeTunnelDialog mesh={mesh} onClose={() => setDialog(null)} />}
      {dialog?.kind === "address" && <TunnelAddressDialog server={dialog.server} onClose={() => setDialog(null)} />}
      {dialog?.kind === "remove" && (
        <Confirm title="Remove tunnel" confirm="Remove tunnel" onClose={() => setDialog(null)} onConfirm={() => void remove.run(dialog.tunnel.id)}>
          <p>The panel removes the tunnel between <strong>{tunnelName(dialog.tunnel)}</strong> from the two servers. Private traffic between them stops.</p>
        </Confirm>
      )}
      {dialog?.kind === "rotate" && (
        <Confirm title={`Make a new key on ${dialog.server.name}`} confirm="Make new key" onClose={() => setDialog(null)} onConfirm={() => void rotate.run(dialog.server.id)}>
          <p>{dialog.server.name} makes a new tunnel key. The private key does not leave the server.</p>
          <p>{dialog.server.tunnels > 0
            ? `The ${dialog.server.tunnels === 1 ? "tunnel" : `${dialog.server.tunnels} tunnels`} of this server will stop for a short time, until the other servers have the new public key. The panel sends it automatically.`
            : "This server has no tunnels, so no traffic stops."}</p>
        </Confirm>
      )}
    </>
  );
}
