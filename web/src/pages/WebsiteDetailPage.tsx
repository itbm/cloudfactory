import { ArrowUpRight, ShieldCheck } from "lucide-react";
import { useEffect } from "react";
import { Link, useParams } from "react-router-dom";
import { useAction, useStore } from "../api/hooks";
import { backupsReady, imageOf, runtimeLabel, supportEnds, tagOf, updateReady } from "../api/selectors";
import { retryWebsite, setPower, backupNow, showWebsite } from "../api/store";
import type { Website } from "../api/types";
import { Topbar } from "../components/Layout";
import { UsagePanel, WebsiteAlertSummary } from "../components/UsageCharts";
import { WebsiteMenu } from "../components/WebsiteMenu";
import { Card, Empty, FormError, JobError, JobRow, Live, Meter, Status, Tabs, TerraformBadge, TerraformNotice } from "../components/ui";
import { bytes, longDate, percent, ratio, relativeTime } from "../lib/format";
import { certificateUntil, DISK_LIMIT_NOT_APPLIED, jobActive, WEBSITE_KIND, WEBSITE_STATUS } from "../lib/labels";
import { AccessTab, SshCard } from "./website/AccessTab";
import { BackupsTab } from "./website/BackupsTab";
import { CronTab } from "./website/CronTab";
import { DatabasesTab } from "./website/DatabasesTab";
import { DeploysTab } from "./website/DeploysTab";
import { DomainsTab } from "./website/DomainsTab";
import { FilesTab } from "./website/FilesTab";
import { LogsTab } from "./website/LogsTab";
import { RuntimeTab } from "./website/RuntimeTab";
import { SettingsTab } from "./website/SettingsTab";
import { PluginActionsCard } from "../components/PluginActions";

const TABS = [
  ["overview", "Overview"],
  ["usage", "Usage"],
  ["domains", "Domains"],
  ["runtime", "Runtime"],
  ["files", "Files"],
  ["databases", "Databases"],
  ["deploys", "Deploys"],
  ["cron", "Cron"],
  ["logs", "Logs"],
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
      {admin && <div className="btn-group"><Link className="btn btn-primary" to="/updates">Open Images and updates</Link></div>}
    </section>
  );
}

function RuntimeCard({ website }: { website: Website }) {
  const state = useStore();
  const image = imageOf(state, website);
  const isPhp = website.runtime.kind === "frankenphp" || website.runtime.kind === "apache-php-fpm";
  const isApp = website.runtime.kind === "node" || website.runtime.kind === "python";
  // The website gives the digest to all roles. Only an administrator has the list of images.
  const digest = website.runtime.digest || image?.digest;
  return (
    <Card title="Runtime" actions={(isPhp || isApp) && <Link to={`/websites/${website.id}/runtime`}>{isPhp ? "Change PHP version" : "Change runtime"}</Link>}>
      <div className="card-body">
        <table className="kv">
          <tbody>
            <tr><th scope="row">Engine</th><td>{runtimeLabel(state, website).split(" · ")[0]} {tagOf(state, website)}{website.runtime.kind === "frankenphp" ? ` · ${website.runtime.workerMode ? "worker mode" : "classic mode"}` : ""}</td></tr>
            {isApp && website.runtime.version && <tr><th scope="row">{website.runtime.kind === "node" ? "Node.js" : "Python"}</th><td>{website.runtime.version}{supportEnds(state, website) && <span className="note-warning"> · support ends {longDate(supportEnds(state, website)!)}</span>}</td></tr>}
            {website.runtime.phpVersion && <tr><th scope="row">PHP</th><td>{website.runtime.phpVersion}{supportEnds(state, website) && <span className="note-warning"> · support ends {longDate(supportEnds(state, website)!)}</span>}</td></tr>}
            {isApp && website.command && <tr><th scope="row">Start command</th><td><code>{website.command}</code></td></tr>}
            {isApp && <tr><th scope="row">Variables</th><td>{website.environment.length ? website.environment.join(", ") : <span className="muted">None</span>}</td></tr>}
            <tr><th scope="row">Image</th><td><code>{website.imageReference ?? `${image?.reference}:${tagOf(state, website)}${website.runtime.phpVersion && image?.family !== "apache-php-fpm" ? `-php${website.runtime.phpVersion}` : ""}`}</code></td></tr>
            <tr>
              <th scope="row">Digest</th>
              <td>
                {/* Audit item U8: an image that the panel knows by tag only has no digest. */}
                {digest ? <code className="digest">{digest}</code> : <span className="muted">No digest (tag only)</span>}{" "}
                {(website.runtime.signatureVerified ?? image?.signatureVerified) ? <span className="badge badge-success">Signature verified</span> : <span className="badge badge-warning">No signature</span>}
              </td>
            </tr>
            {website.runtime.kind === "frankenphp" && (
              <tr><th scope="row">Worker mode</th><td>{website.runtime.workerMode ? "On" : "Off"}<span className="cell-sub">Use it only for apps that are built for it, such as Laravel Octane.</span></td></tr>
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
  // Audit item U4: a disk limit that the server does not apply must not look like a limit that it applies.
  const diskNote = website.diskLimitApplied === false ? DISK_LIMIT_NOT_APPLIED : undefined;
  const rows: [string, string, number, string?][] = [
    ["CPU", `${percent(use.cpuPercent)} of ${limits.vcpus} vCPU`, use.cpuPercent / 100],
    ["Memory", `${bytes(use.memoryBytes)} of ${bytes(limits.memoryBytes)}`, ratio(use.memoryBytes, limits.memoryBytes)],
    ["Disk", `${bytes(use.diskBytes)} of ${bytes(limits.diskBytes)}`, ratio(use.diskBytes, limits.diskBytes), diskNote],
    ["Processes", `${use.processes} of ${limits.processes}`, ratio(use.processes, limits.processes)],
    ["Disk I/O", `${bytes(use.diskIoBytesPerSecond)}/s of ${bytes(limits.diskIoBytesPerSecond)}/s`, ratio(use.diskIoBytesPerSecond, limits.diskIoBytesPerSecond)],
  ];
  // The bandwidth limit is for a calendar month. A plan with no bandwidth limit shows no meter.
  if (limits.bandwidthBytes) rows.push(["Bandwidth this month", `${bytes(website.bandwidthBytes)} of ${bytes(limits.bandwidthBytes)}`, ratio(website.bandwidthBytes, limits.bandwidthBytes)]);
  return (
    <Card title="Resources" actions={<Link to={`/websites/${website.id}/usage`}>See graphs</Link>}>
      <div className="card-body">
        {rows.map(([label, text, value, note]) => (
          <div className="resource" key={label}>
            <div className="row"><span>{label}</span><span>{text}</span></div>
            <Meter value={value} label={note ? `${label}: ${text}. ${note}` : `${label}: ${text}`} tone={value >= 0.9 ? "danger" : value >= 0.75 ? "warning" : undefined} />
            {note && (
              <p className="note-warning" style={{ marginTop: 6 }}>
                {note}
                {state.session.user.role === "administrator" && <>. <Link to={`/servers/${website.serverId}`}>See the server</Link></>}
              </p>
            )}
          </div>
        ))}
        <p className="muted small" style={{ marginTop: 16 }}>
          Limits come from the {plan?.name} plan. {state.session.user.role === "owner" ? "Only your provider can change the plan." : <Link to={`/websites/${website.id}/settings`}>Change plan</Link>}
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
  // Audit item U5: with no object storage, a backup can only fail. The card says so, and has no buttons.
  if (!backupsReady(state)) {
    const admin = state.session.user.role === "administrator";
    return (
      <Card title="Backups">
        <div className="card-body">
          <p><Status tone="warning">Backups are not set up</Status></p>
          <p className="muted" style={{ marginTop: 8 }}>
            {admin
              ? "The panel has no object storage for backups, so it makes no backup of this website."
              : "Your provider has not set up backups on this panel, so the panel makes no backup of this website. Keep your own copy of the files and the databases."}
          </p>
          {admin && <div className="btn-group" style={{ marginTop: 14 }}><Link className="btn btn-primary" to="/backups">Set the object storage</Link></div>}
        </div>
      </Card>
    );
  }
  return (
    <Card title="Backups">
      <div className="card-body">
        <p className="muted">
          {last ? <>Last snapshot {relativeTime(last.at)} ({bytes(last.sizeBytes)}). </> : "No snapshot yet. "}
          Kept: {p.daily} daily, {p.weekly} weekly, {p.monthly} monthly. Stored in object storage, not on the server of the website.
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
      <WebsiteAlertSummary websiteId={website.id} />
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
          <PluginActionsCard target="website" targetId={website.id} targetName={website.name} />
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
      {job && <p className="cause"><span className="visually-hidden">Cause: </span><JobError job={job} /></p>}
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
              {website.kind !== "public" && <span className="tag">{WEBSITE_KIND[website.kind]}</span>}
              {website.managedByTerraform && <TerraformBadge />}
            </div>
            <p className="detail-meta">
              {website.application}{server ? ` · server ${server.name}${server.location ? ` (${server.location})` : ""}` : ""} · Unix user <code>{website.unixUser}</code> · created {longDate(website.createdAt)}
            </p>
          </div>
          <div className="btn-group">
            {/* A private website and a worker have no public address. */}
            {website.kind === "public" && (
              <a className="btn" href={`https://${primary}`} target="_blank" rel="noreferrer">
                Visit site <ArrowUpRight size={16} aria-hidden="true" />
                <span className="visually-hidden">(opens in a new tab)</span>
              </a>
            )}
            <button type="button" className="btn" disabled={website.status !== "running" || restart.busy} onClick={() => void restart.run(website.id, "restart")}>Restart</button>
            <WebsiteMenu website={website} bordered />
          </div>
        </div>
        {website.status === "failed" && <FailedNotice website={website} />}
        {website.status === "moving" && (
          <div className="alert alert-info" role="status" style={{ marginBottom: 20 }}>
            <p>This website moves to the server {state.servers.find((s) => s.id === website.movingToServerId)?.name ?? "that the administrator selected"} now. It does not operate while its files move, and it accepts no change until the move is complete.</p>
          </div>
        )}
        <Tabs label="Website sections" current={tab} items={TABS.filter(([tabId]) => website.kind === "public" || tabId !== "domains").map(([tabId, label]) => ({ id: tabId, label, href: tabId === "overview" ? `/websites/${website.id}` : `/websites/${website.id}/${tabId}` }))} />
        {website.managedByTerraform && tab !== "overview" && <div style={{ marginBottom: 20 }}><TerraformNotice resource="this website" /></div>}
        {tab === "overview" && <Overview website={website} />}
        {tab === "domains" && (website.kind === "public" ? <DomainsTab website={website} /> : (
          <Empty title="No domains">
            <p>A {WEBSITE_KIND[website.kind].toLowerCase()} has no public address, so it has no domains.</p>
          </Empty>
        ))}
        {tab === "usage" && <UsagePanel key={website.id} subject="website" id={website.id} />}
        {tab === "runtime" && <RuntimeTab website={website} />}
        {tab === "databases" && <DatabasesTab website={website} />}
        {tab === "files" && <FilesTab website={website} />}
        {tab === "deploys" && <DeploysTab key={website.id} website={website} />}
        {tab === "cron" && <CronTab key={website.id} website={website} />}
        {tab === "logs" && <LogsTab key={website.id} website={website} />}
        {tab === "backups" && <BackupsTab website={website} />}
        {tab === "access" && <AccessTab website={website} />}
        {tab === "settings" && <SettingsTab website={website} />}
      </main>
    </>
  );
}
