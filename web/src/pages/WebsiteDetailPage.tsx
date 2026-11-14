import { ArrowUpRight, ShieldCheck } from "lucide-react";
import { useEffect } from "react";
import { Link, useParams } from "react-router-dom";
import { useAction, useStore } from "../api/hooks";
import { imageOf, runtimeLabel, supportEnds, tagOf, updateReady } from "../api/selectors";
import { retryWebsite, setPower, backupNow, showWebsite } from "../api/store";
import type { Website } from "../api/types";
import { Topbar } from "../components/Layout";
import { WebsiteMenu } from "../components/WebsiteMenu";
import { Card, Empty, FormError, JobRow, Live, Meter, Status, Tabs, TerraformBadge, TerraformNotice } from "../components/ui";
import { bytes, longDate, percent, ratio, relativeTime } from "../lib/format";
import { certificateUntil, jobActive, WEBSITE_STATUS } from "../lib/labels";
import { AccessTab, SshCard } from "./website/AccessTab";
import { BackupsTab } from "./website/BackupsTab";
import { DatabasesTab } from "./website/DatabasesTab";
import { DomainsTab } from "./website/DomainsTab";
import { RuntimeTab } from "./website/RuntimeTab";
import { SettingsTab } from "./website/SettingsTab";

const TABS = [
  ["overview", "Overview"],
  ["domains", "Domains"],
  ["runtime", "Runtime"],
  ["databases", "Databases"],
  ["backups", "Backups"],
  ["access", "SFTP"],
  ["settings", "Settings"],
] as const;

function UpdateBanner({ website }: { website: Website }) {
  const state = useStore();
  const image = imageOf(state, website)!;
  const running = state.jobs.find((j) => j.websiteId === website.id && jobActive(j) && j.title.startsWith("Test update"));
  if (!updateReady(state, website) || running) return null;
  // The image rollout updates all websites in stages (FR-1108). The API has no update for one website.
  const admin = state.session.user.role === "administrator";
  return (
    <section className="banner" aria-labelledby="update-title">
      <ShieldCheck className="banner-icon" size={26} aria-hidden="true" />
      <div className="banner-text">
        <h2 id="update-title">An update is ready for this website</h2>
        <p>
          {image.name} {image.available} is signed and ready. The rollout updates the websites in stages, with a health check for each website.
          If the check fails, the website goes back to {tagOf(state, website)}. {admin ? "" : "Your provider starts the rollout. You do not need to do anything."}
        </p>
      </div>
      {admin && <div className="btn-group"><Link className="btn btn-primary" to="/updates">Go to Updates</Link></div>}
    </section>
  );
}

function RuntimeCard({ website }: { website: Website }) {
  const state = useStore();
  const image = imageOf(state, website);
  const isPhp = website.runtime.kind === "frankenphp" || website.runtime.kind === "apache-php-fpm";
  return (
    <Card title="Runtime" actions={isPhp && <Link to={`/websites/${website.id}/runtime`}>Change PHP version</Link>}>
      <div className="card-body">
        <table className="kv">
          <tbody>
            <tr><th scope="row">Engine</th><td>{runtimeLabel(state, website).split(" · ")[0]} {tagOf(state, website)}{website.runtime.kind === "frankenphp" ? ` · ${website.runtime.workerMode ? "worker mode" : "classic mode"}` : ""}</td></tr>
            {website.runtime.phpVersion && <tr><th scope="row">PHP</th><td>{website.runtime.phpVersion}{supportEnds(state, website) && <span className="note-warning"> · support ends {longDate(supportEnds(state, website)!)}</span>}</td></tr>}
            <tr><th scope="row">Image</th><td><code>{website.imageReference ?? `${image?.reference}:${tagOf(state, website)}${website.runtime.phpVersion && image?.family !== "apache-php-fpm" ? `-php${website.runtime.phpVersion}` : ""}`}</code></td></tr>
            <tr>
              <th scope="row">Digest</th>
              <td>
                <code>{image?.digest}</code>{" "}
                {image?.signatureVerified ? <span className="badge badge-success">Signature verified</span> : <span className="badge badge-warning">No signature</span>}
              </td>
            </tr>
            {website.runtime.kind === "frankenphp" && (
              <tr><th scope="row">Worker mode</th><td className="muted">{website.runtime.workerMode ? "On." : "Off."} Use only for apps built for it, such as Laravel Octane.</td></tr>
            )}
            {isPhp && (
              <tr>
                <th scope="row">Extensions</th>
                <td><ul className="tag-list">{website.runtime.extensions.map((e) => <li key={e}><span className="tag">{e}</span></li>)}</ul></td>
              </tr>
            )}
          </tbody>
        </table>
      </div>
    </Card>
  );
}

export function ResourcesCard({ website }: { website: Website }) {
  const state = useStore();
  const plan = state.plans.find((p) => p.id === website.planId);
  const { use, limits } = website;
  const rows = [
    ["CPU", `${percent(use.cpuPercent)} of ${limits.vcpus} vCPU`, use.cpuPercent / 100],
    ["Memory", `${bytes(use.memoryBytes)} of ${bytes(limits.memoryBytes)}`, ratio(use.memoryBytes, limits.memoryBytes)],
    ["Disk", `${bytes(use.diskBytes)} of ${bytes(limits.diskBytes)}`, ratio(use.diskBytes, limits.diskBytes)],
    ["Processes", `${use.processes} of ${limits.processes}`, ratio(use.processes, limits.processes)],
    ["Disk I/O", `${bytes(use.diskIoBytesPerSecond)}/s of ${bytes(limits.diskIoBytesPerSecond)}/s`, ratio(use.diskIoBytesPerSecond, limits.diskIoBytesPerSecond)],
  ] as const;
  return (
    <Card title="Resources">
      <div className="card-body">
        {rows.map(([label, text, value]) => (
          <div className="resource" key={label}>
            <div className="row"><span>{label}</span><span>{text}</span></div>
            <Meter value={value} label={`${label}: ${text}`} tone={value >= 0.9 ? "danger" : value >= 0.75 ? "warning" : undefined} />
          </div>
        ))}
        <p className="muted small" style={{ marginTop: 16 }}>
          Limits come from the {plan?.name} plan. <Link to={`/websites/${website.id}/settings`}>Change plan</Link>
        </p>
      </div>
    </Card>
  );
}

function JobsCard({ website }: { website: Website }) {
  const state = useStore();
  const jobs = state.jobs.filter((j) => j.websiteId === website.id).slice(0, 5);
  return (
    <Card title="Jobs" actions={<Live />}>
      <div className="card-body">
        {jobs.length ? <ul className="job-list">{jobs.map((j) => <JobRow key={j.id} job={j} />)}</ul> : <p className="muted">No jobs yet.</p>}
      </div>
    </Card>
  );
}

function DomainsCard({ website }: { website: Website }) {
  return (
    <Card title="Domains" actions={<Link className="btn" to={`/websites/${website.id}/domains`}>Add domain</Link>}>
      <div className="card-body">
        <ul className="list">
          {website.domains.map((d) => (
            <li key={d.name} style={{ display: "block" }}>
              <strong style={{ fontWeight: 500 }}>{d.name}</strong>
              {d.kind === "primary" && <span className="cell-sub">Primary · {certificateUntil(d, website.status === "failed")}</span>}
              {d.kind === "redirect" && <span className="cell-sub">Redirects to the primary domain</span>}
              {d.kind === "alias" && (
                d.dnsPointsHere
                  ? <span className="cell-sub">Alias · {certificateUntil(d)}</span>
                  : <span className="cell-sub note-warning">Alias · DNS does not point here yet. <Link to={`/websites/${website.id}/domains`} className="text-warning">Check DNS</Link></span>
              )}
            </li>
          ))}
        </ul>
      </div>
    </Card>
  );
}

function BackupsCard({ website }: { website: Website }) {
  const state = useStore();
  const last = state.snapshots.find((s) => s.websiteId === website.id);
  const p = state.backupPolicy;
  const backup = useAction(backupNow);
  return (
    <Card title="Backups">
      <div className="card-body">
        <p className="muted">
          {last ? <>Last snapshot {relativeTime(last.at)} ({bytes(last.sizeBytes)}). </> : "No snapshot yet. "}
          Kept: {p.daily} daily, {p.weekly} weekly, {p.monthly} monthly. Stored off-server in object storage.
        </p>
        <div className="btn-group" style={{ marginTop: 14 }}>
          <Link className="btn" to={`/websites/${website.id}/backups`}>Restore…</Link>
          <button type="button" className="btn" disabled={backup.busy} onClick={() => void backup.run(website.id)}>Back up now</button>
        </div>
        <FormError error={backup.error} />
      </div>
    </Card>
  );
}

function Overview({ website }: { website: Website }) {
  return (
    <>
      <UpdateBanner website={website} />
      <div className="detail-grid">
        <div className="stack">
          <RuntimeCard website={website} />
          <ResourcesCard website={website} />
        </div>
        <div className="stack">
          <JobsCard website={website} />
          <DomainsCard website={website} />
          <SshCard website={website} />
          <BackupsCard website={website} />
        </div>
      </div>
    </>
  );
}

/**
 * The panel could not make the website. The notice shows the cause from the job, and lets the
 * user try again. Before, the user had to delete the website and to make it again.
 */
function FailedNotice({ website }: { website: Website }) {
  const state = useStore();
  const retry = useAction(retryWebsite);
  const job = state.jobs.find((j) => j.websiteId === website.id && j.state === "failed");
  const staging = !!website.stagingOf;
  return (
    <div className="alert alert-danger alert-stack mt" role="alert">
      <p><strong>The panel could not make this website.</strong> The website has no container on the server.</p>
      {job?.error && <p className="cause"><span className="visually-hidden">Cause: </span>{job.error}</p>}
      <p>
        {staging
          ? "Delete this staging copy, and make the staging copy again."
          : <>Correct the cause, and try again. If the cause is the image, select a different image on the <Link to={`/websites/${website.id}/runtime`}>Runtime</Link> tab: the panel then makes the website with that image.</>}
      </p>
      <FormError error={retry.error} />
      {!staging && <div className="btn-group"><button type="button" className="btn btn-primary" disabled={retry.busy} onClick={() => void retry.run(website.id)}>{retry.busy ? "Starting…" : "Try again"}</button></div>}
    </div>
  );
}

export function WebsiteDetailPage() {
  const { id = "", tab: asked = "overview" } = useParams();
  const tab = TABS.some(([t]) => t === asked) ? asked : "overview";
  const state = useStore();
  const website = state.websites.find((w) => w.id === id);
  const restart = useAction(setPower);
  const known = !!website;
  useEffect(() => {
    if (known) showWebsite(id);
  }, [id, known]);

  if (!website) {
    return (
      <>
        <Topbar />
        <main id="main" className="content">
          <Empty title="This website does not exist">
            <p>It may have been removed. <Link to="/websites">Go to all websites</Link>.</p>
          </Empty>
        </main>
      </>
    );
  }

  const status = WEBSITE_STATUS[website.status];
  const server = state.servers.find((s) => s.id === website.serverId);
  const primary = website.domains.find((d) => d.kind === "primary")?.name ?? website.name;

  return (
    <>
      <Topbar />
      <main id="main" className="content content-wide">
        <ol className="breadcrumbs">
          <li><Link to="/websites">Websites</Link></li>
          <li aria-current="page">{website.name}</li>
        </ol>
        <div className="detail-header">
          <div>
            <div className="detail-title">
              <h1>{website.name}</h1>
              <span className={`status-pill status-${status.tone}`}><Status tone={status.tone}>{status.label}</Status></span>
              {website.managedByTerraform && <TerraformBadge />}
            </div>
            <p className="detail-meta">
              {website.application}{server ? ` · server ${server.name}${server.location ? ` (${server.location})` : ""}` : ""} · Unix user <code>{website.unixUser}</code> · created {longDate(website.createdAt)}
            </p>
          </div>
          <div className="btn-group">
            <a className="btn" href={`https://${primary}`} target="_blank" rel="noreferrer">
              Visit site <ArrowUpRight size={16} aria-hidden="true" />
              <span className="visually-hidden">(opens in a new tab)</span>
            </a>
            <button type="button" className="btn" disabled={website.status !== "running" || restart.busy} onClick={() => void restart.run(website.id, "restart")}>Restart</button>
            <WebsiteMenu website={website} bordered />
          </div>
        </div>
        {website.status === "failed" && <FailedNotice website={website} />}
        <Tabs label="Website sections" current={tab} items={TABS.map(([tabId, label]) => ({ id: tabId, label, href: tabId === "overview" ? `/websites/${website.id}` : `/websites/${website.id}/${tabId}` }))} />
        {website.managedByTerraform && tab !== "overview" && <div style={{ marginBottom: 20 }}><TerraformNotice resource="this website" /></div>}
        {tab === "overview" && <Overview website={website} />}
        {tab === "domains" && <DomainsTab website={website} />}
        {tab === "runtime" && <RuntimeTab website={website} />}
        {tab === "databases" && <DatabasesTab website={website} />}
        {tab === "backups" && <BackupsTab website={website} />}
        {tab === "access" && <AccessTab website={website} />}
        {tab === "settings" && <SettingsTab website={website} />}
      </main>
    </>
  );
}
