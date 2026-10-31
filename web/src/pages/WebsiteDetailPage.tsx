import { ArrowUpRight, ShieldCheck } from "lucide-react";
import { useEffect, useState } from "react";
import { features, LIVE } from "../api/mode";
import { Link, useParams } from "react-router-dom";
import { useAction, useStore } from "../api/hooks";
import { imageOf, runtimeLabel, supportEnds, tagOf, updateReady } from "../api/selectors";
import { applyImageUpdate, deployNow, scheduleImageUpdate, setPower, backupNow, showWebsite } from "../api/store";
import type { Website } from "../api/types";
import { Topbar } from "../components/Layout";
import { WebsiteMenu } from "../components/WebsiteMenu";
import { Card, Empty, FormError, JobRow, Live, Meter, Modal, Status, Tabs, TerraformBadge, TerraformNotice } from "../components/ui";
import { bytes, longDate, percent, ratio, relativeTime } from "../lib/format";
import { certificateUntil, WEBSITE_STATUS } from "../lib/labels";
import { AccessTab, SshCard } from "./website/AccessTab";
import { BackupsTab } from "./website/BackupsTab";
import { CronTab } from "./website/CronTab";
import { DatabasesTab } from "./website/DatabasesTab";
import { DeploysTab } from "./website/DeploysTab";
import { DomainsTab } from "./website/DomainsTab";
import { EmailTab } from "./website/EmailTab";
import { FilesTab } from "./website/FilesTab";
import { LogsTab } from "./website/LogsTab";
import { RuntimeTab } from "./website/RuntimeTab";
import { SettingsTab } from "./website/SettingsTab";

const ALL_TABS = [
  ["overview", "Overview", true],
  ["domains", "Domains", true],
  ["runtime", "Runtime", true],
  ["databases", "Databases", true],
  ["email", "Email", features.email],
  ["files", "Files", features.files],
  ["cron", "Cron jobs", features.cron],
  ["backups", "Backups", true],
  ["logs", "Logs", features.logs],
  ["deploys", "Deploys", features.deploys],
  ["access", LIVE ? "SFTP" : "SFTP and SSH", features.access],
  ["settings", "Settings", true],
] as const;

// In live mode the panel shows only the sections that the API of this release has.
const TABS = ALL_TABS.filter(([, , shown]) => shown).map(([id, label]) => [id, label] as const);

function UpdateBanner({ website }: { website: Website }) {
  const state = useStore();
  const image = imageOf(state, website)!;
  const [changes, setChanges] = useState(false);
  const running = state.jobs.find((j) => j.websiteId === website.id && j.state === "running" && j.title.startsWith("Test update"));
  if (!updateReady(state, website) || running) return null;
  if (LIVE) {
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
  return (
    <section className="banner" aria-labelledby="update-title">
      <ShieldCheck className="banner-icon" size={26} aria-hidden="true" />
      <div className="banner-text">
        <h2 id="update-title">A security update is ready for this website</h2>
        <p>
          {image.name.split(" · ")[0]} {image.available} with the latest PHP {website.runtime.phpVersion} patch. The image is signed and passed its scan.
          We test it on a copy of this site first, then switch with no downtime. If the health check fails, we switch back.
        </p>
        {website.updateScheduledAt && <p className="small"><strong>Scheduled for {website.updateScheduledAt}.</strong></p>}
      </div>
      <div className="btn-group">
        <button type="button" className="btn btn-primary" onClick={() => void applyImageUpdate(website.id)}>Update now</button>
        <button type="button" className="btn" onClick={() => scheduleImageUpdate(website.id, "02:00")} disabled={website.updateScheduledAt === "02:00"}>
          {website.updateScheduledAt === "02:00" ? "Scheduled for 02:00" : "Schedule for 02:00"}
        </button>
        <button type="button" className="btn-link" onClick={() => setChanges(true)}>What changed</button>
      </div>
      {changes && (
        <Modal title={`What changed in ${image.available}`} onClose={() => setChanges(false)} footer={<button type="button" className="btn" onClick={() => setChanges(false)}>Close</button>}>
          <ul>
            <li>PHP {website.runtime.phpVersion}: security fixes for CVE-2026-1182 and CVE-2026-1190.</li>
            <li>FrankenPHP {image.available}: a fix for a memory leak in worker mode.</li>
            <li>Base image: updated OpenSSL and curl packages.</li>
          </ul>
          <p className="muted">Signature: verified with the Cloudfactory release key. Scan: 0 critical, 0 high. SBOM attached.</p>
        </Modal>
      )}
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

function DeploysCard({ website }: { website: Website }) {
  const live = website.releases.find((r) => r.live) ?? website.releases[0];
  return (
    <Card title="Deploys" actions={website.git && <button type="button" className="btn" onClick={() => void deployNow(website.id)}>Deploy now</button>}>
      <div className="card-body">
        {website.git ? (
          <>
            <p className="muted">From Git: <code>{website.git.repository}</code>, branch <code>{website.git.branch}</code>. {website.git.trigger === "webhook" ? "Deploys on every push." : `Checks for changes every ${website.git.scheduleMinutes} minutes.`}</p>
            {live && (
              <div className="box" style={{ marginTop: 14 }}>
                <span><code>{live.ref}</code> {live.message}</span>
                <span className={live.state === "failed" ? "text-danger" : "text-success"}>{live.state === "failed" ? "Failed" : "Succeeded"} in {live.durationSeconds} s · {relativeTime(live.at)}</span>
              </div>
            )}
            {website.previews.length > 0 && (
              <>
                <h3 style={{ marginTop: 18, marginBottom: 6 }}>Preview sites for pull requests</h3>
                <ul className="list">
                  {website.previews.map((p) => (
                    <li key={p.url} style={{ padding: "6px 0", borderTop: 0 }}>
                      <a href={p.url} className="mono">{p.url.replace("https://", "")}</a>
                      <span className="muted">{p.title}</span>
                    </li>
                  ))}
                </ul>
              </>
            )}
          </>
        ) : (
          <p className="muted">No Git repository. <Link to={`/websites/${website.id}/deploys`}>Connect a repository</Link> to deploy on each push.</p>
        )}
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
              {d.kind === "primary" && <span className="cell-sub">Primary · {certificateUntil(d.certificate)}</span>}
              {d.kind === "redirect" && <span className="cell-sub">Redirects to the primary domain</span>}
              {d.kind === "alias" && (
                d.dnsPointsHere
                  ? <span className="cell-sub">Alias · {certificateUntil(d.certificate)}</span>
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
          {features.deploys && <DeploysCard website={website} />}
        </div>
        <div className="stack">
          <JobsCard website={website} />
          <DomainsCard website={website} />
          {features.access && <SshCard website={website} />}
          <BackupsCard website={website} />
        </div>
      </div>
    </>
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
              {website.application}{server ? ` · server ${server.name} (${server.location})` : ""} · Unix user <code>{website.unixUser}</code> · created {longDate(website.createdAt)}
            </p>
          </div>
          <div className="btn-group">
            <a className="btn" href={`https://${primary}`} target="_blank" rel="noreferrer">
              Visit site <ArrowUpRight size={16} aria-hidden="true" />
              <span className="visually-hidden">(opens in a new tab)</span>
            </a>
            {features.files && <Link className="btn" to={`/websites/${website.id}/files`}>File manager</Link>}
            <button type="button" className="btn" disabled={website.status !== "running" || restart.busy} onClick={() => void restart.run(website.id, "restart")}>Restart</button>
            <WebsiteMenu website={website} bordered />
          </div>
        </div>
        <Tabs label="Website sections" current={tab} items={TABS.map(([tabId, label]) => ({ id: tabId, label, href: tabId === "overview" ? `/websites/${website.id}` : `/websites/${website.id}/${tabId}` }))} />
        {website.managedByTerraform && tab !== "overview" && <div style={{ marginBottom: 20 }}><TerraformNotice resource="this website" /></div>}
        {tab === "overview" && <Overview website={website} />}
        {tab === "domains" && <DomainsTab website={website} />}
        {tab === "runtime" && <RuntimeTab website={website} />}
        {tab === "databases" && <DatabasesTab website={website} />}
        {tab === "email" && <EmailTab website={website} />}
        {tab === "files" && <FilesTab website={website} />}
        {tab === "cron" && <CronTab website={website} />}
        {tab === "backups" && <BackupsTab website={website} />}
        {tab === "logs" && <LogsTab website={website} />}
        {tab === "deploys" && <DeploysTab website={website} />}
        {tab === "access" && <AccessTab website={website} />}
        {tab === "settings" && <SettingsTab website={website} />}
      </main>
    </>
  );
}
