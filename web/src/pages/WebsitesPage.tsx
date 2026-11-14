import { Plus } from "lucide-react";
import { useMemo, useState } from "react";
import { Link } from "react-router-dom";
import { useStore } from "../api/hooks";
import { accountUsage, attention, failedServices, runtimeLabel, supportEnds, updateReady, visibleWebsites } from "../api/selectors";
import type { RuntimeKind, State, Website } from "../api/types";
import { Topbar } from "../components/Layout";
import { WebsiteMenu } from "../components/WebsiteMenu";
import { Card, JobRow, Live, Meter, PageHeader, Status, TerraformBadge } from "../components/ui";
import { bytes, percent, ratio, relativeTime, shortAgo, shortDate } from "../lib/format";
import { certificateText, jobActive, RUNTIME_KIND, WEBSITE_STATUS } from "../lib/labels";

const PAGE_SIZE = 7;

type Filter = "all" | "running" | "stopped" | "updates";

const FILTERS: { id: Filter; label: string; test: (s: State, w: Website) => boolean }[] = [
  { id: "all", label: "All", test: () => true },
  { id: "running", label: "Running", test: (_, w) => w.status === "running" },
  { id: "stopped", label: "Stopped", test: (_, w) => w.status === "stopped" },
  { id: "updates", label: "Updates ready", test: (s, w) => updateReady(s, w) },
];

function UsageCards() {
  const state = useStore();
  const u = accountUsage(state);
  const cards = [
    { label: "Websites", value: String(u.websites.used), of: u.websites.limit === undefined ? "on the panel" : `of ${u.websites.limit} on your plan`, ratio: u.websites.limit ? ratio(u.websites.used, u.websites.limit) : 0 },
    { label: "CPU", value: `${u.cpuPercent}%`, of: `of ${u.vcpus} vCPU`, ratio: u.cpuPercent / 100 },
    { label: "Memory", value: bytes(u.memoryBytes.used), of: `of ${bytes(u.memoryBytes.limit)}`, ratio: ratio(u.memoryBytes.used, u.memoryBytes.limit) },
    { label: "Disk", value: bytes(u.diskBytes.used), of: `of ${bytes(u.diskBytes.limit)}`, ratio: ratio(u.diskBytes.used, u.diskBytes.limit) },
  ];
  return (
    <div className="stats">
      {cards.map((c) => (
        <section className="card stat" key={c.label} aria-label={c.label}>
          <h2 className="stat-label">{c.label}</h2>
          <p className="stat-value">
            {c.value}
            <small>{c.of}</small>
          </p>
          <Meter value={c.ratio} label={`${c.label}: ${c.value} ${c.of}`} />
        </section>
      ))}
    </div>
  );
}

function WebsitesTable({ sites }: { sites: Website[] }) {
  const state = useStore();
  return (
    <div className="table-wrap sites-table">
      <table>
        <thead>
          <tr>
            <th scope="col">Website</th>
            <th scope="col">Runtime</th>
            <th scope="col">Status</th>
            <th scope="col">Certificate</th>
            <th scope="col">CPU and memory</th>
            <th scope="col">Last change</th>
            <th scope="col"><span className="visually-hidden">Actions</span></th>
          </tr>
        </thead>
        <tbody>
          {sites.map((w) => {
            const status = WEBSITE_STATUS[w.status];
            const end = supportEnds(state, w);
            const domains = w.domains.length;
            return (
              <tr key={w.id}>
                <td>
                  <Link className="cell-title" to={`/websites/${w.id}`}>{w.name}</Link>
                  <span className="cell-sub">
                    {w.application}
                    {domains > 1 ? ` · ${domains} domains` : ""}
                  </span>
                  {w.managedByTerraform && <TerraformBadge />}
                </td>
                <td>
                  <span className="tag">{runtimeLabel(state, w)}</span>
                  {updateReady(state, w) && <div style={{ marginTop: 6 }}><span className="badge badge-warning">Update ready</span></div>}
                  {end && <div className="note-warning" style={{ marginTop: 6 }}>Support ends {shortDate(end)}</div>}
                </td>
                <td><Status tone={status.tone}>{status.label}</Status></td>
                <td className="muted" style={{ minWidth: 150, maxWidth: 170 }}>{w.status === "failed" ? "–" : certificateText(w.domains.find((d) => d.kind === "primary") ?? w.domains[0])}</td>
                {/* One column for the two values: with a column for each, the table was wider than the page. */}
                <td className="nowrap">
                  {w.status !== "running" ? "–" : (
                    <>
                      <Meter inline value={w.use.cpuPercent / 100 / w.limits.vcpus} label={`CPU ${percent(w.use.cpuPercent)}`} />
                      {percent(w.use.cpuPercent)}
                      <span className="cell-sub">{bytes(w.use.memoryBytes)} of {bytes(w.limits.memoryBytes)}</span>
                    </>
                  )}
                </td>
                <td className="nowrap">
                  {relativeTime(w.lastChange.at).replace(/^./, (c) => c.toUpperCase())}
                  {(w.lastChange.ref ?? w.lastChange.summary) && <span className={`cell-sub${w.lastChange.ref ? " mono" : ""}`}>{w.lastChange.ref ?? w.lastChange.summary}</span>}
                </td>
                <td className="cell-actions"><WebsiteMenu website={w} /></td>
              </tr>
            );
          })}
        </tbody>
      </table>
    </div>
  );
}

/** The phone layout of the list (mockup screen 4). */
function WebsiteCards({ sites }: { sites: Website[] }) {
  const state = useStore();
  return (
    <ul className="site-cards">
      {sites.map((w) => {
        const status = WEBSITE_STATUS[w.status];
        return (
          <li key={w.id}>
            <Link to={`/websites/${w.id}`}>
              <span>
                <span className="cell-title">{w.name}</span>
                <span className="cell-sub">{w.application}</span>
                <span className="site-runtime" style={{ display: "block" }}>
                  <span className="tag">{runtimeLabel(state, w)}</span>
                </span>
              </span>
              <span style={{ textAlign: "right" }}>
                <Status tone={status.tone}>{status.label}</Status>
                {updateReady(state, w) && <span style={{ display: "block", marginTop: 6 }}><span className="badge badge-warning">Update ready</span></span>}
              </span>
            </Link>
          </li>
        );
      })}
    </ul>
  );
}

function WebsiteList() {
  const state = useStore();
  const [filter, setFilter] = useState<Filter>("all");
  const [runtime, setRuntime] = useState<RuntimeKind | "any">("any");
  const [page, setPage] = useState(0);

  const all = visibleWebsites(state);
  const filtered = useMemo(() => {
    const test = FILTERS.find((f) => f.id === filter)!.test;
    return all.filter((w) => test(state, w) && (runtime === "any" || w.runtime.kind === runtime));
  }, [state, all, filter, runtime]);
  const pages = Math.max(1, Math.ceil(filtered.length / PAGE_SIZE));
  const current = Math.min(page, pages - 1);
  const shown = filtered.slice(current * PAGE_SIZE, (current + 1) * PAGE_SIZE);

  return (
    <Card
      title="All websites"
      actions={
        <div className="filters" role="group" aria-label="Filter websites">
          {FILTERS.map((f) => (
            <button
              key={f.id}
              type="button"
              className="chip"
              aria-pressed={filter === f.id}
              onClick={() => {
                setFilter(f.id);
                setPage(0);
              }}
            >
              {f.label} · {all.filter((w) => f.test(state, w)).length}
            </button>
          ))}
          <label className="visually-hidden" htmlFor="runtime-filter">Runtime</label>
          <select
            id="runtime-filter"
            className="select"
            value={runtime}
            onChange={(e) => {
              setRuntime(e.target.value as RuntimeKind | "any");
              setPage(0);
            }}
          >
            <option value="any">Any runtime</option>
            {Object.entries(RUNTIME_KIND).map(([id, label]) => (
              <option key={id} value={id}>{label}</option>
            ))}
          </select>
        </div>
      }
    >
      {all.length === 0 ? (
        <div className="placeholder">
          <h3>No websites yet</h3>
          <p>Add a website. It gets its own container, Unix user and limits.</p>
          <Link className="btn btn-primary" to="/websites/new" style={{ marginTop: 12 }}>New website</Link>
        </div>
      ) : (
        <>
          <WebsitesTable sites={shown} />
          <WebsiteCards sites={shown} />
          <div className="table-footer">
            <span aria-live="polite">Showing {shown.length} of {filtered.length} websites</span>
            <div className="btn-group">
              <button type="button" className="btn" disabled={current === 0} onClick={() => setPage(current - 1)}>Previous</button>
              <button type="button" className="btn" disabled={current >= pages - 1} onClick={() => setPage(current + 1)}>Next</button>
            </div>
          </div>
        </>
      )}
    </Card>
  );
}

function Activity() {
  const state = useStore();
  const running = state.jobs.filter(jobActive).slice(0, 3);
  const rollout = state.rollout;
  return (
    <Card title="Activity" actions={<Live />}>
      <div className="card-body">
        {rollout && rollout.state === "running" && (
          <div className="progress-row">
            <div className="row">
              <span>Updating the {rollout.title.split(" with ")[1] ?? rollout.title} image on your websites</span>
              <span>{rollout.healthy} of {rollout.websites}</span>
            </div>
            <Meter value={rollout.healthy / rollout.websites} label={`Image update: ${rollout.healthy} of ${rollout.websites} websites`} />
            <p className="detail">Each site is health-checked before the old container stops.</p>
          </div>
        )}
        {running.length > 0 && <ul className="job-list" style={{ marginBottom: 12 }}>{running.map((j) => <JobRow key={j.id} job={j} />)}</ul>}
        <ul className="list" style={{ borderTop: "1px solid var(--border)" }}>
          {state.activity.slice(0, 4).map((a) => (
            <li key={a.id}>
              <span>{a.text}{a.ref && <> <code>{a.ref}</code></>}</span>
              <span className="when">{shortAgo(a.at)}</span>
            </li>
          ))}
        </ul>
        <Link to="/jobs" className="small">See all jobs and activity</Link>
      </div>
    </Card>
  );
}

function NeedsAttention() {
  const state = useStore();
  const items = attention(state);
  return (
    <Card title="Needs attention">
      <div className="card-body">
        {items.length === 0 ? <p className="muted">Nothing needs your attention.</p> : (
          items.slice(0, 5).map((a) => (
            <div key={a.id} className={`alert${a.tone === "warning" ? " alert-warning" : ""}`}>
              <p><strong>{a.subject}</strong> {a.text}</p>
              <Link className="btn" to={a.href}>{a.action}</Link>
            </div>
          ))
        )}
      </div>
    </Card>
  );
}

export function WebsitesPage() {
  const role = useStore().session.user.role;
  return (
    <>
      <Topbar
        primary={
          <Link className="btn btn-primary" to="/websites/new">
            <Plus size={18} aria-hidden="true" />
            <span className="label">New website</span>
          </Link>
        }
      >
        {/* Only an administrator has the state of the servers. */}
        {role === "administrator" && <HealthPill />}
      </Topbar>
      <main id="main" className="content">
        <PageHeader title={role === "owner" ? "My websites" : "Websites"} description="Each website runs in its own container, as its own Unix user, with its own limits." />
        <UsageCards />
        <WebsiteList />
        <div className="two-col">
          <Activity />
          <NeedsAttention />
        </div>
      </main>
    </>
  );
}

export function HealthPill() {
  const state = useStore();
  // An optional part that is off, for example the disk quotas, is not a fault.
  const bad = state.servers.filter((s) => s.state === "degraded" || s.state === "offline" || failedServices(s.services).length > 0);
  if (bad.length) {
    return <Link to="/servers" className="pill pill-warning"><Status tone="warning">{bad.length} {bad.length === 1 ? "server needs" : "servers need"} attention</Status></Link>;
  }
  return <span className="pill pill-success"><Status tone="success">All services healthy</Status></span>;
}
