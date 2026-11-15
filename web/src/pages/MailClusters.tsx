import { useEffect, useState } from "react";
import { Link, useSearchParams } from "react-router-dom";
import { Check, Circle, LoaderCircle, X } from "lucide-react";
import { useAction, useStore } from "../api/hooks";
import { createMailCluster, refresh, retryMailCluster, type NewClusterMember } from "../api/store";
import type { MailCluster, Server } from "../api/types";
import { Card, Field, FormError, Meter, Modal, Status, TableWrap } from "../components/ui";

/*
 * Mail clusters. A cluster has 3 or 5 servers with the email role. Each member has all the
 * mail, so the mail service continues when one server fails. The members can be in different
 * regions.
 */

const STATUS: Record<MailCluster["status"], { label: string; tone: "info" | "success" | "danger" }> = {
  setting_up: { label: "Setting up", tone: "info" },
  running: { label: "Running", tone: "success" },
  failed: { label: "Failed", tone: "danger" },
};

/** The steps of the setup, in order, as the panel does them. */
const PHASES: { phase: MailCluster["phase"]; label: string }[] = [
  { phase: "prepare", label: "Each server makes its keys" },
  { phase: "bootstrap", label: "The first server starts the database" },
  { phase: "join", label: "The other servers join the database" },
  { phase: "first", label: "The first mail server starts" },
  { phase: "rest", label: "The other mail servers start" },
];

const STEP: Record<MailCluster["members"][number]["step"], string> = {
  new: "Waiting",
  prepared: "Has keys",
  joined: "In the database",
  running: "Running",
};

/** A server that can be a member: it has the email role, no mail server, and is online. */
function eligible(s: Server) {
  return s.roles.includes("email") && !s.mail && s.state !== "offline";
}

/** The address of the agent, without the port. It is usually the public address of the server. */
function hostOf(address?: string) {
  if (!address) return "";
  const v6 = address.match(/^\[(.+)\]:\d+$/);
  if (v6) return v6[1];
  return address.replace(/:\d+$/, "");
}

export function MailClustersCard() {
  const state = useStore();
  const [params, setParams] = useSearchParams();
  const clusters = state.mailClusters;
  const free = state.servers.filter(eligible);
  const open = clusters.find((c) => c.id === params.get("cluster"));
  // The setup changes each few seconds. The panel reads it more often than the other data.
  const busy = clusters.some((c) => c.status === "setting_up");
  useEffect(() => {
    if (!busy) return;
    const t = setInterval(() => { if (document.visibilityState === "visible") void refresh(); }, 5000);
    return () => clearInterval(t);
  }, [busy]);
  if (clusters.length === 0 && !state.servers.some((s) => s.roles.includes("email"))) return null;
  return (
    <Card
      title="Mail clusters"
      actions={<button type="button" className="btn btn-sm" disabled={free.length < 3} onClick={() => setParams({ newCluster: "1" })}>New mail cluster</button>}
    >
      {clusters.length === 0
        ? (
          <div className="card-body">
            <p>A mail cluster puts the same mail on 3 or 5 servers. When one server fails, the other servers continue to receive and deliver mail. The servers can be in different regions.</p>
            {free.length < 3 && <p className="muted small" style={{ marginTop: 8 }}>You need 3 servers with the email role that do not have a mail server. {free.length === 0 ? "No server" : free.length === 1 ? "1 server" : `${free.length} servers`} can be a member now.</p>}
          </div>
        )
        : (
          <TableWrap>
            <table>
              <thead><tr><th scope="col">Cluster</th><th scope="col">Shared name</th><th scope="col">Members</th><th scope="col">Mail domains</th><th scope="col">Status</th><th scope="col"><span className="visually-hidden">Actions</span></th></tr></thead>
              <tbody>
                {clusters.map((c) => (
                  <tr key={c.id}>
                    <td><button type="button" className="btn-link cell-title mono" onClick={() => setParams({ cluster: c.id })}>{c.name}</button></td>
                    <td><code>{c.hostname}</code></td>
                    <td>{c.members.map((m) => m.serverName).join(", ")}</td>
                    <td>{c.domains}</td>
                    <td><Status tone={STATUS[c.status].tone}>{STATUS[c.status].label}</Status>{c.status === "setting_up" && <span className="muted small"> {phaseIndex(c) + 1} of {PHASES.length}</span>}</td>
                    <td style={{ textAlign: "right" }}><button type="button" className="btn btn-sm" aria-label={`Details of ${c.name}`} onClick={() => setParams({ cluster: c.id })}>Details</button></td>
                  </tr>
                ))}
              </tbody>
            </table>
          </TableWrap>
        )}
      {params.get("newCluster") && <NewClusterDialog servers={free} onClose={() => setParams({})} onMade={(id) => setParams({ cluster: id })} />}
      {open && <ClusterDialog cluster={open} onClose={() => setParams({})} />}
    </Card>
  );
}

function phaseIndex(c: MailCluster) {
  return c.phase === "done" ? PHASES.length : PHASES.findIndex((p) => p.phase === c.phase);
}

function ClusterDialog({ cluster: c, onClose }: { cluster: MailCluster; onClose: () => void }) {
  const retry = useAction(retryMailCluster);
  const at = phaseIndex(c);
  return (
    <Modal title={`Mail cluster ${c.name}`} wide onClose={onClose} footer={<button type="button" className="btn" onClick={onClose}>Close</button>}>
      <p>Shared name <code>{c.hostname}</code> <Status tone={STATUS[c.status].tone}>{STATUS[c.status].label}</Status></p>
      {c.status !== "running" && (
        <>
          <Meter value={at / PHASES.length} label={`Setup: step ${Math.min(at + 1, PHASES.length)} of ${PHASES.length}`} />
          <ol className="cluster-steps" aria-label="Steps of the setup">
            {PHASES.map((p, i) => {
              const state = i < at ? "done" : i > at ? "todo" : c.status === "failed" ? "failed" : "now";
              const Icon = { done: Check, now: LoaderCircle, failed: X, todo: Circle }[state];
              return (
                <li key={p.phase} className={`cluster-step cluster-step-${state}`}>
                  <Icon size={16} aria-hidden="true" />
                  <span>{p.label}</span>
                  <span className="visually-hidden">{{ done: "Done", now: "In progress", failed: "Failed", todo: "Not started" }[state]}</span>
                </li>
              );
            })}
          </ol>
        </>
      )}
      {c.status === "failed" && (
        <div className="alert alert-danger" role="alert">
          <p>{c.error ?? "A job of the setup failed."} Look at the <Link to="/jobs">jobs</Link>, correct the cause, then start the step again. Only the servers that did not complete the step do it again.</p>
          <p style={{ marginTop: 8 }}><button type="button" className="btn" disabled={retry.busy} onClick={() => void retry.run(c.id)}>Start the step again</button></p>
        </div>
      )}
      <FormError error={retry.error} />
      <h3 className="section-title">Members</h3>
      <TableWrap>
        <table>
          <thead><tr><th scope="col">#</th><th scope="col">Server</th><th scope="col">Host name</th><th scope="col">Public address</th><th scope="col">Private address</th><th scope="col">Region</th><th scope="col">Step</th></tr></thead>
          <tbody>
            {c.members.map((m) => (
              <tr key={m.serverId}>
                <td>{m.number}</td>
                <td className="nowrap"><Link className="mono" to={`/servers/${m.serverId}`}>{m.serverName}</Link>{m.serverState === "offline" && <> <Status tone="danger">Offline</Status></>}</td>
                <td><code>{m.hostname}</code></td>
                <td><code>{m.endpoint}</code></td>
                <td><code>{m.address}</code></td>
                <td>{m.region}</td>
                <td className="nowrap">{STEP[m.step]}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </TableWrap>
      <h3 className="section-title">DNS records to add</h3>
      <p className="muted small">The shared name has the address of each member. The MX records of the mail domains use it. Mail programs connect to the host name of one member.</p>
      <TableWrap>
        <table>
          <thead><tr><th scope="col">Type</th><th scope="col">Name</th><th scope="col">Value</th></tr></thead>
          <tbody>
            {c.records.map((r, i) => <tr key={i}><td>{r.type}</td><td><code>{r.name}</code></td><td><code>{r.value}</code></td></tr>)}
          </tbody>
        </table>
      </TableWrap>
    </Modal>
  );
}

function NewClusterDialog({ servers, onClose, onMade }: { servers: Server[]; onClose: () => void; onMade: (id: string) => void }) {
  const [name, setName] = useState("");
  const [hostname, setHostname] = useState("");
  // The order of selection is the order of the members. The first member starts the database.
  const [members, setMembers] = useState<NewClusterMember[]>([]);
  const make = useAction(createMailCluster);
  const toggle = (s: Server, on: boolean) => setMembers(on
    ? [...members, { serverId: s.id, hostname: "", endpoint: hostOf(s.agentAddress), region: 0 }]
    : members.filter((m) => m.serverId !== s.id));
  const change = (id: string, patch: Partial<NewClusterMember>) => setMembers(members.map((m) => (m.serverId === id ? { ...m, ...patch } : m)));
  const sizeOk = members.length === 3 || members.length === 5;
  const f = make.error?.fields ?? {};
  return (
    <Modal
      title="New mail cluster"
      wide
      onClose={onClose}
      footer={<><button type="button" className="btn" onClick={onClose}>Cancel</button><button type="submit" form="mail-cluster" className="btn btn-primary" disabled={make.busy || !sizeOk || !name.trim() || !hostname.trim()}>Make the cluster</button></>}
    >
      <form id="mail-cluster" noValidate onSubmit={async (e) => {
        e.preventDefault();
        const r = await make.run(name, hostname, members);
        if (r.ok) onMade(r.value.id);
      }}>
        <p>Select 3 or 5 servers. With an even number, the cluster cannot continue when the network between the two halves fails. Each server gets all the mail, and the servers connect through a private network on UDP port 51877.</p>
        <FormError error={make.error} shown={["name", "hostname", ...Object.keys(f).filter((k) => k.startsWith("members."))]} />
        <div className="form-row">
          <Field label="Name" hint="Lower-case letters, digits and hyphens." error={f.name}>{(p) => <input {...p} className="input mono" value={name} onChange={(e) => setName(e.target.value)} placeholder="eu" autoCapitalize="none" spellCheck={false} />}</Field>
          <Field label="Shared name" hint="The MX records of the mail domains use this name." error={f.hostname}>{(p) => <input {...p} className="input" value={hostname} onChange={(e) => setHostname(e.target.value)} placeholder="mail.example.net" autoCapitalize="none" spellCheck={false} />}</Field>
        </div>
        <h3 className="section-title">Servers <span className="muted small">({members.length} selected)</span></h3>
        {f.members && <p className="error">{f.members}</p>}
        <ul className="cluster-members">
          {servers.map((s) => {
            const i = members.findIndex((m) => m.serverId === s.id);
            const m = members[i];
            const err = (k: string) => (i >= 0 ? f[`members.${i}.${k}`] : undefined);
            return (
              <li key={s.id} className={m ? "cluster-member cluster-member-on" : "cluster-member"}>
                <label className="check">
                  <input type="checkbox" checked={!!m} disabled={!m && members.length >= 5} onChange={(e) => toggle(s, e.target.checked)} />
                  <span className="mono">{s.name}</span>
                  {s.location && <span className="muted small">{s.location}</span>}
                  {m && <span className="muted small">Member {i + 1}{i === 0 ? ": starts the database" : ""}</span>}
                </label>
                {err("serverId") && <p className="error">{err("serverId")}</p>}
                {m && (
                  <div className="form-row">
                    <Field label="Host name" hint="Mail programs connect to this name." error={err("hostname")}>{(p) => <input {...p} className="input" value={m.hostname} onChange={(e) => change(s.id, { hostname: e.target.value })} placeholder={`${s.name}.example.net`} autoCapitalize="none" spellCheck={false} />}</Field>
                    <Field label="Public address" error={err("endpoint")}>{(p) => <input {...p} className="input mono" value={m.endpoint} onChange={(e) => change(s.id, { endpoint: e.target.value })} placeholder="192.0.2.1" />}</Field>
                    <Field label="Region" hint="Servers with the same number are near to each other." error={err("region")}>{(p) => <input {...p} className="input" type="number" min={0} max={99} value={m.region} onChange={(e) => change(s.id, { region: Number(e.target.value) || 0 })} />}</Field>
                  </div>
                )}
              </li>
            );
          })}
        </ul>
        <p className="muted small">The panel cannot move a mail server that has its own data into a cluster. Each server needs Linux 6.12 or later.</p>
      </form>
    </Modal>
  );
}
