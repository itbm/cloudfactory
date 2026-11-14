import { Link } from "react-router-dom";
import { useStore } from "../api/hooks";
import { failedServices, platformAttention } from "../api/selectors";
import type { Server } from "../api/types";
import { Topbar } from "../components/Layout";
import { Card, JobRow, Live, Meter, PageHeader, Status, TableWrap } from "../components/ui";
import { openFailures } from "../lib/jobFailure";
import { jobActive, SERVER_STATE } from "../lib/labels";

const meterTone = (percent: number) => (percent >= 90 ? "danger" : percent >= 75 ? "warning" : undefined);

function Use({ server, part, value }: { server: Server; part: string; value: number }) {
  return (
    <span className="nowrap">
      <Meter inline value={value / 100} label={`${part} of ${server.name}: ${value}%`} tone={meterTone(value)} />
      {value}%
    </span>
  );
}

/** A server with a necessary part that does not run has a fault, also when its state is "healthy". */
const faulty = (s: Server) => s.state === "degraded" || s.state === "offline" || !!s.connectionError || failedServices(s.services).length > 0;

/**
 * The first page of the administrator console: the health of the platform. It shows the
 * servers, what needs attention, the jobs that failed, and the setup items that are open.
 */
export function OverviewPage() {
  const state = useStore();
  const servers = state.servers;
  const bad = servers.filter(faulty);
  const running = state.websites.filter((w) => w.status === "running").length;
  const failedSites = state.websites.filter((w) => w.status === "failed").length;
  const suspended = state.customers.filter((c) => c.suspended).length;
  const failures = openFailures(state.jobs);
  const active = state.jobs.filter(jobActive);
  const attention = platformAttention(state);
  const setup = state.setup;
  const stats = [
    { label: "Servers", value: String(servers.length), note: bad.length ? `${bad.length} ${bad.length === 1 ? "needs" : "need"} attention` : "All healthy", tone: bad.length ? "warning" : "success", to: "/servers" },
    { label: "Websites", value: String(state.websites.length), note: failedSites ? `${running} running, ${failedSites} failed` : `${running} running`, tone: failedSites ? "warning" : "success", to: "/websites" },
    { label: "Customers", value: String(state.customers.length), note: suspended ? `${suspended} suspended` : "None suspended", tone: "neutral", to: "/customers" },
    { label: "Failed jobs", value: String(failures.length), note: failures.length ? "Not resolved" : "None to resolve", tone: failures.length ? "danger" : "success", to: "/jobs" },
  ] as const;
  return (
    <>
      <Topbar placeholder="Search servers, images, customers and jobs" />
      <main id="main" className="content content-wide">
        <PageHeader title="Overview" description="The health of the platform: servers, updates and jobs." />
        {setup && !setup.complete && (
          <section className="banner" aria-labelledby="setup-title">
            <div className="banner-text">
              <h2 id="setup-title">{setup.open} setup {setup.open === 1 ? "item is" : "items are"} open</h2>
              <p>{setup.items.filter((i) => i.state === "open").map((i) => i.title).join(". ")}.</p>
            </div>
            <div className="btn-group"><Link className="btn btn-primary" to="/setup">Finish setup</Link></div>
          </section>
        )}
        <div className="stats">
          {stats.map((c) => (
            <Link className="card stat stat-link" key={c.label} to={c.to} aria-label={`${c.label}: ${c.value}. ${c.note}`}>
              <h2 className="stat-label">{c.label}</h2>
              <p className="stat-value">{c.value}</p>
              <Status tone={c.tone}>{c.note}</Status>
            </Link>
          ))}
        </div>
        <Card title="Servers" actions={<Link className="btn" to="/servers">All servers</Link>}>
          <TableWrap>
            <table>
              <thead><tr><th scope="col">Server</th><th scope="col">Status</th><th scope="col">CPU</th><th scope="col">Memory</th><th scope="col">Disk</th><th scope="col">Agent</th><th scope="col">Updates</th></tr></thead>
              <tbody>
                {servers.map((s) => (
                  <tr key={s.id}>
                    <td><Link className="cell-title mono" to={`/servers/${s.id}`}>{s.name}</Link>{s.panel && <span className="cell-sub">Panel server</span>}</td>
                    <td><Status tone={faulty(s) && s.state === "healthy" ? "warning" : SERVER_STATE[s.state].tone}>{faulty(s) && s.state === "healthy" ? "Needs attention" : SERVER_STATE[s.state].label}</Status></td>
                    <td><Use server={s} part="CPU" value={s.cpuPercent} /></td>
                    <td><Use server={s} part="Memory" value={s.memoryPercent} /></td>
                    <td><Use server={s} part="Disk" value={s.diskPercent} /></td>
                    <td><code>{s.agentVersion}</code>{s.agentUpdateAvailable && <span className="cell-sub note-warning">Update ready</span>}</td>
                    <td className="nowrap">{s.pendingUpdates.length ? `${s.pendingUpdates.length} pending` : "None"}{s.rebootRequired && <span className="cell-sub note-warning">Restart necessary</span>}</td>
                  </tr>
                ))}
                {servers.length === 0 && <tr><td colSpan={7} className="muted">No server has joined the panel. <Link to="/servers">Add a server</Link>.</td></tr>}
              </tbody>
            </table>
          </TableWrap>
        </Card>
        <div className="two-col">
          <Card title="Needs attention">
            <div className="card-body">
              {attention.length === 0 ? <p className="muted">Nothing needs your attention.</p> : attention.slice(0, 8).map((a) => (
                <div key={a.id} className={`alert${a.tone === "warning" ? " alert-warning" : ""}`}>
                  <p><strong>{a.subject}</strong> {a.text}</p>
                  <Link className="btn" to={a.href}>{a.action}</Link>
                </div>
              ))}
              {attention.length > 8 && <p className="muted small" style={{ marginTop: 12 }}>And {attention.length - 8} more. Open Servers, and Images and updates, to see them.</p>}
            </div>
          </Card>
          <Card title="Jobs" actions={<Live />}>
            <div className="card-body">
              {active.length === 0 && failures.length === 0
                ? <p className="muted">No job is in progress, and no failed job waits for you.</p>
                : <ul className="job-list">{[...active, ...failures].slice(0, 6).map((j) => <JobRow key={j.id} job={j} />)}</ul>}
              <p style={{ marginTop: 12 }}><Link to="/jobs" className="small">See all jobs and activity</Link></p>
            </div>
          </Card>
        </div>
      </main>
    </>
  );
}
