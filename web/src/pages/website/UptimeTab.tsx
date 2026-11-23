import { useEffect, useState } from "react";
import { Link } from "react-router-dom";
import { useAction, useLoad } from "../../api/hooks";
import type { Website } from "../../api/types";
import {
  addUptimeCheck, duration, intervalText, listUptimeChecks, readUptimeCheck, removeUptimeCheck, runUptimeCheck, setUptimeCheck,
  UPTIME_LIMITS, uptimeText, type UptimeBucket, type UptimeCheck, type UptimeCheckDetail, type UptimeCheckInput, type UptimeDay,
  type UptimeIncident, type UptimeResult, type UptimeState,
} from "../../api/uptime";
import { UsageChart } from "../../components/UsageCharts";
import { Card, Empty, Field, FormError, Modal, RowMenu, Status, TableWrap, Toggle, type Tone } from "../../components/ui";
import { clockTime, longDate, relativeTime } from "../../lib/format";

/** The panel loads the checks again after this time, so the state stays current. */
const RELOAD_MS = 30_000;

export const UPTIME_STATE: Record<UptimeState, { label: string; tone: Tone }> = {
  up: { label: "Up", tone: "success" },
  down: { label: "Down", tone: "danger" },
  pending: { label: "No result yet", tone: "info" },
  paused: { label: "Paused", tone: "neutral" },
};

/** The state of a check, and for how long. */
export function UptimeStateText({ check }: { check: UptimeCheck }) {
  const s = UPTIME_STATE[check.state];
  const since = check.since && (check.state === "up" || check.state === "down") ? duration(Math.max(0, (Date.now() - new Date(check.since).getTime()) / 1000)) : null;
  return (
    <>
      <Status tone={s.tone}>{s.label}</Status>
      {check.state === "paused" && check.pausedReason && <span className="cell-sub">{check.pausedReason}</span>}
      {since && <span className="cell-sub">For {since}</span>}
      {check.state === "up" && check.failures > 0 && <span className="cell-sub note-warning">{check.failures} failed {check.failures === 1 ? "request" : "requests"}. The check tries again.</span>}
    </>
  );
}

/** The result of the last request: the status and the response time, or the error. */
export function LastResult({ check }: { check: UptimeCheck }) {
  const r = check.lastResult;
  if (!r || !check.lastCheckedAt) return <span className="muted">Not checked yet</span>;
  return (
    <>
      {r.ok
        ? <span>{r.status ?? ""}{r.responseMs !== null ? ` · ${r.responseMs} ms` : ""}</span>
        : <span className="text-danger">{r.error}</span>}
      <span className="cell-sub">{relativeTime(check.lastCheckedAt)}</span>
    </>
  );
}

/** The uptime of the last 24 hours and of the last 30 days. */
export function UptimeCell({ check }: { check: UptimeCheck }) {
  if (check.uptime.day === null && check.uptime.month === null) return <span className="muted">No data</span>;
  return (
    <>
      {uptimeText(check.uptime.day)} <span className="muted small">24 hours</span>
      <span className="cell-sub">{uptimeText(check.uptime.month)} in 30 days</span>
    </>
  );
}

/** What the check expects: "Status 200 to 399", and the text. */
export function expectation(check: Pick<UptimeCheck, "expectedStatus" | "keyword">): string {
  const status = check.expectedStatus === null ? "Status 200 to 399" : `Status ${check.expectedStatus}`;
  return check.keyword ? `${status} and the text "${check.keyword}"` : status;
}

const tone = (ok: boolean | null, partial = false) => (ok === null ? "none" : ok ? (partial ? "partial" : "up") : "down");

/** 48 parts of 30 minutes: green when each request succeeded, red when each request failed. */
function DayBar({ history }: { history: UptimeBucket[] }) {
  const checked = history.filter((b) => b.checks > 0);
  const failed = checked.filter((b) => b.failures > 0).length;
  const summary = checked.length === 0
    ? "No results."
    : failed === 0 ? "Each request succeeded." : `${failed} of ${checked.length} periods of 30 minutes have failed requests.`;
  return (
    <figure className="uptime-strip">
      <ol className="uptime-bar" aria-hidden="true">
        {history.map((b) => {
          const end = new Date(new Date(b.at).getTime() + 30 * 60_000).toISOString();
          const state = b.checks === 0 ? "none" : tone(b.failures < b.checks, b.failures > 0);
          const text = b.checks === 0 ? "No results" : b.failures === 0 ? `${b.checks} requests succeeded` : `${b.failures} of ${b.checks} requests failed`;
          return <li key={b.at} className={`uptime-part uptime-${state}`} title={`${clockTime(b.at)} to ${clockTime(end)}: ${text}`} />;
        })}
      </ol>
      <div className="uptime-axis" aria-hidden="true"><span>24 hours ago</span><span>Now</span></div>
      <figcaption className="muted small">{summary}</figcaption>
    </figure>
  );
}

/** 30 days: the percent of the time that the address was up on each day. */
function MonthBar({ days }: { days: UptimeDay[] }) {
  const known = days.filter((d) => d.uptime !== null);
  const bad = known.filter((d) => (d.uptime ?? 100) < 100).length;
  const summary = known.length === 0 ? "No results." : bad === 0 ? "Up all the time." : `Down on ${bad} of ${known.length} days.`;
  return (
    <figure className="uptime-strip">
      <ol className="uptime-bar uptime-bar-days" aria-hidden="true">
        {days.map((d) => {
          const state = d.uptime === null ? "none" : d.uptime >= 100 ? "up" : d.uptime >= 99 ? "partial" : "down";
          const text = d.uptime === null ? "No results" : d.downSeconds > 0 ? `${uptimeText(d.uptime)} up, down for ${duration(d.downSeconds)}` : "Up all the time";
          return <li key={d.date} className={`uptime-part uptime-${state}`} title={`${longDate(`${d.date}T12:00:00Z`)}: ${text}`} />;
        })}
      </ol>
      <div className="uptime-axis" aria-hidden="true"><span>30 days ago</span><span>Today</span></div>
      <figcaption className="muted small">{summary}</figcaption>
    </figure>
  );
}

function IncidentTable({ incidents }: { incidents: UptimeIncident[] }) {
  if (incidents.length === 0) return <p className="muted">No incidents. The panel keeps the incidents for 90 days.</p>;
  return (
    <TableWrap>
      <table>
        <thead><tr><th scope="col">Started</th><th scope="col">Length</th><th scope="col">Cause</th></tr></thead>
        <tbody>
          {incidents.map((i) => (
            <tr key={i.id}>
              <td className="nowrap">{longDate(i.startedAt)}<span className="cell-sub">{clockTime(i.startedAt)}</span></td>
              <td className="nowrap">
                {i.resolvedAt ? duration(i.durationSeconds) : <Status tone="danger">Still down</Status>}
                {i.resolvedAt && <span className="cell-sub">{i.endedByPause ? "Ended when the check was paused" : `Up again at ${clockTime(i.resolvedAt)}`}</span>}
              </td>
              <td>{i.error}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </TableWrap>
  );
}

function ResultNotice({ result, onClose }: { result: UptimeResult; onClose: () => void }) {
  return (
    <div className={`alert ${result.ok ? "alert-success" : "alert-danger"}`} role="status">
      <p>
        {result.ok
          ? <>The check succeeded: HTTP status {result.status}{result.responseMs !== null ? ` in ${result.responseMs} ms` : ""}.</>
          : <>The check failed. {result.error}</>}
      </p>
      <button type="button" className="btn btn-sm" onClick={onClose}>Close</button>
    </div>
  );
}

/** One check: its state, the last 24 hours, the last 30 days and the incidents. */
function CheckDetail({ detail, onRun, onEdit, running, result, closeResult }: {
  detail: UptimeCheckDetail;
  onRun: () => void;
  onEdit: () => void;
  running: boolean;
  result: UptimeResult | null;
  closeResult: () => void;
}) {
  const history = {
    range: "24h" as const,
    stepMinutes: 30,
    from: detail.history[0]?.at ?? "",
    to: detail.history[detail.history.length - 1]?.at ?? "",
    points: detail.history.map((b) => ({ at: b.at, cpuPercent: null, cpuMaxPercent: null })),
    alerts: [],
  };
  const timed = detail.history.filter((b) => b.responseMs !== null);
  const average = timed.length ? Math.round(timed.reduce((s, b) => s + b.responseMs! * b.checks, 0) / timed.reduce((s, b) => s + b.checks, 0)) : null;
  return (
    <Card
      title={detail.url}
      id="uptime-detail"
      actions={(
        <div className="btn-group">
          <button type="button" className="btn btn-sm" disabled={running || detail.state === "paused"} onClick={onRun}>{running ? "Checking…" : "Check now"}</button>
          <button type="button" className="btn btn-sm" onClick={onEdit}>Edit</button>
        </div>
      )}
    >
      <div className="card-body stack">
        {result && <ResultNotice result={result} onClose={closeResult} />}
        {detail.state === "down" && detail.incident && (
          <div className="alert alert-danger alert-stack" role="alert">
            <p><strong>This address is down.</strong> The first failed request was {relativeTime(detail.incident.startedAt)}, at {clockTime(detail.incident.startedAt)}.</p>
            <p className="cause"><span className="visually-hidden">Cause: </span>{detail.incident.error}</p>
          </div>
        )}
        {detail.state === "paused" && <div className="alert alert-info" role="status"><p>{detail.pausedReason} The panel does not request the address, and it sends no email.</p></div>}
        <dl className="uptime-figures">
          <div><dt>Last 24 hours</dt><dd>{uptimeText(detail.uptime.day)}</dd></div>
          <div><dt>Last 7 days</dt><dd>{uptimeText(detail.uptime.week)}</dd></div>
          <div><dt>Last 30 days</dt><dd>{uptimeText(detail.uptime.month)}</dd></div>
          <div><dt>Response time</dt><dd>{average === null ? "No data" : `${average} ms`}</dd></div>
        </dl>
        <section aria-labelledby="uptime-day">
          <h3 id="uptime-day" className="uptime-heading">Last 24 hours</h3>
          <DayBar history={detail.history} />
        </section>
        {timed.length > 0 && (
          <UsageChart
            title="Response time"
            history={history}
            series={{ values: detail.history.map((b) => b.responseMs), max: detail.history.map((b) => b.maxResponseMs) }}
            format={(v) => `${Math.round(v)} ms`}
            summary={average === null ? undefined : `Average ${average} ms`}
          />
        )}
        <section aria-labelledby="uptime-month">
          <h3 id="uptime-month" className="uptime-heading">Last 30 days</h3>
          <MonthBar days={detail.days} />
        </section>
        <section aria-labelledby="uptime-incidents">
          <h3 id="uptime-incidents" className="uptime-heading">Incidents</h3>
          <IncidentTable incidents={detail.incidents} />
        </section>
        <table className="kv">
          <tbody>
            <tr><th scope="row">Expects</th><td>{expectation(detail)}</td></tr>
            <tr><th scope="row">Interval</th><td>Every {intervalText(detail.intervalSeconds)}, with a timeout of {detail.timeoutSeconds} seconds</td></tr>
            <tr><th scope="row">Alert</th><td>After {detail.failuresBeforeAlert} failed {detail.failuresBeforeAlert === 1 ? "request" : "requests"} in a row</td></tr>
            {detail.nextCheckAt && <tr><th scope="row">Next check</th><td>{new Date(detail.nextCheckAt).getTime() - Date.now() <= 60_000 ? "Within 1 minute" : relativeTime(detail.nextCheckAt)}</td></tr>}
          </tbody>
        </table>
      </div>
    </Card>
  );
}

const emptyInput = (website: Website): UptimeCheckInput => ({
  domain: website.domains.find((d) => d.kind === "primary")?.name ?? website.domains[0]?.name ?? "",
  scheme: "https",
  path: "/",
  intervalSeconds: 300,
  timeoutSeconds: 10,
  expectedStatus: null,
  keyword: null,
  failuresBeforeAlert: 2,
  enabled: true,
});

/** The form to add a check or to change a check. */
function CheckForm({ website, check, onClose, onSaved }: { website: Website; check: UptimeCheck | null; onClose: () => void; onSaved: (c: UptimeCheck) => void }) {
  const [input, setInput] = useState<UptimeCheckInput>(() => (check ? {
    domain: check.domain, scheme: check.scheme, path: check.path, intervalSeconds: check.intervalSeconds, timeoutSeconds: check.timeoutSeconds,
    expectedStatus: check.expectedStatus, keyword: check.keyword, failuresBeforeAlert: check.failuresBeforeAlert, enabled: check.enabled,
  } : emptyInput(website)));
  const [status, setStatus] = useState(check?.expectedStatus ? String(check.expectedStatus) : "");
  const [timeout, setTimeoutText] = useState(String(input.timeoutSeconds));
  const save = useAction(async () => {
    const body = { ...input, expectedStatus: status.trim() ? Number(status) : null, timeoutSeconds: Number(timeout), keyword: input.keyword?.trim() ? input.keyword : null };
    const saved = check ? await setUptimeCheck(website.id, check.id, body) : await addUptimeCheck(website.id, body);
    onSaved(saved);
  });
  const set = <K extends keyof UptimeCheckInput>(key: K, value: UptimeCheckInput[K]) => setInput((i) => ({ ...i, [key]: value }));
  const f = save.error?.fields ?? {};
  const FIELDS = ["domain", "scheme", "path", "intervalSeconds", "timeoutSeconds", "expectedStatus", "keyword", "failuresBeforeAlert"];
  return (
    <Modal
      wide
      title={check ? "Change the uptime check" : "Add an uptime check"}
      onClose={onClose}
      footer={<><button type="button" className="btn" onClick={onClose}>Cancel</button><button type="submit" form="uptime-check" className="btn btn-primary" disabled={save.busy}>{save.busy ? "Saving…" : check ? "Save" : "Add check"}</button></>}
    >
      <form id="uptime-check" noValidate onSubmit={(e) => { e.preventDefault(); void save.run(); }}>
        <FormError error={save.error} shown={FIELDS} />
        <div className="form-grid">
          <Field label="Domain" error={f.domain}>
            {(p) => (
              <select {...p} className="select" value={input.domain} onChange={(e) => set("domain", e.target.value)}>
                {website.domains.map((d) => <option key={d.name} value={d.name}>{d.name}{d.kind === "redirect" ? " (redirects)" : ""}</option>)}
              </select>
            )}
          </Field>
          <Field label="Protocol" error={f.scheme}>
            {(p) => (
              <select {...p} className="select" value={input.scheme} onChange={(e) => set("scheme", e.target.value as "https" | "http")}>
                <option value="https">HTTPS</option>
                <option value="http">HTTP</option>
              </select>
            )}
          </Field>
          <Field label="Path" hint={'For example "/" or "/health". A query is permitted.'} error={f.path}>
            {(p) => <input {...p} className="input mono" value={input.path} onChange={(e) => set("path", e.target.value)} spellCheck={false} autoCapitalize="none" />}
          </Field>
          <Field label="Interval" error={f.intervalSeconds}>
            {(p) => (
              <select {...p} className="select" value={input.intervalSeconds} onChange={(e) => set("intervalSeconds", Number(e.target.value))}>
                {UPTIME_LIMITS.intervals.map((s) => <option key={s} value={s}>Every {intervalText(s)}</option>)}
              </select>
            )}
          </Field>
          <Field label="Expected HTTP status" hint="Empty: each status from 200 to 399." error={f.expectedStatus}>
            {(p) => <input {...p} className="input" inputMode="numeric" value={status} onChange={(e) => setStatus(e.target.value.replace(/\D/g, "").slice(0, 3))} placeholder="200 to 399" />}
          </Field>
          <Field label="Text in the answer" hint="Optional. The check fails when the first 1 MB of the answer does not have this text. Upper and lower case are different." error={f.keyword}>
            {(p) => <input {...p} className="input" value={input.keyword ?? ""} maxLength={100} onChange={(e) => set("keyword", e.target.value)} />}
          </Field>
          <Field label="Timeout (seconds)" hint={`From ${UPTIME_LIMITS.minTimeout} to ${UPTIME_LIMITS.maxTimeout} seconds.`} error={f.timeoutSeconds}>
            {(p) => <input {...p} className="input" type="number" min={UPTIME_LIMITS.minTimeout} max={UPTIME_LIMITS.maxTimeout} value={timeout} onChange={(e) => setTimeoutText(e.target.value)} />}
          </Field>
          <Field label="Alert after" hint="The failed requests in a row before the panel sends an email. After a failed request, the panel tries again within 1 minute." error={f.failuresBeforeAlert}>
            {(p) => (
              <select {...p} className="select" value={input.failuresBeforeAlert} onChange={(e) => set("failuresBeforeAlert", Number(e.target.value))}>
                {Array.from({ length: UPTIME_LIMITS.maxFailures }, (_, i) => i + 1).map((n) => <option key={n} value={n}>{n} failed {n === 1 ? "request" : "requests"}</option>)}
              </select>
            )}
          </Field>
        </div>
        <div style={{ marginTop: 16 }}>
          <Toggle checked={input.enabled} label="The check is on" onChange={(v) => set("enabled", v)}>The check is on</Toggle>
        </div>
        {check && <p className="muted small" style={{ marginTop: 12 }}>A change of the address or of the expected answer starts the check again, with no result.</p>}
      </form>
    </Modal>
  );
}

/** Uptime checks: the panel requests an address of the website on a schedule, and sends an email when it is down. */
export function UptimeTab({ website }: { website: Website }) {
  const id = website.id;
  const list = useLoad(() => listUptimeChecks(id), [id]);
  const checks = list.data ?? [];
  const [chosen, setChosen] = useState<string | null>(null);
  const selected = checks.find((c) => c.id === chosen) ?? checks.find((c) => c.state === "down") ?? checks[0] ?? null;
  const detail = useLoad(() => (selected ? readUptimeCheck(id, selected.id) : Promise.resolve(null)), [id, selected?.id, selected?.lastCheckedAt, selected?.state, selected?.enabled]);
  const [form, setForm] = useState<{ check: UptimeCheck | null } | null>(null);
  const [removing, setRemoving] = useState<UptimeCheck | null>(null);
  const [result, setResult] = useState<{ checkId: string; result: UptimeResult } | null>(null);
  const reload = list.reload;
  useEffect(() => {
    const timer = window.setInterval(reload, RELOAD_MS);
    return () => window.clearInterval(timer);
  }, [reload]);

  const run = useAction(async (check: UptimeCheck) => {
    const r = await runUptimeCheck(id, check.id);
    setChosen(check.id);
    setResult({ checkId: check.id, result: r.result });
    list.reload();
  });
  const toggle = useAction((check: UptimeCheck) => setUptimeCheck(id, check.id, { enabled: !check.enabled }).then(list.reload));
  const remove = useAction(async (check: UptimeCheck) => {
    await removeUptimeCheck(id, check.id);
    setRemoving(null);
    if (chosen === check.id) setChosen(null);
    list.reload();
  });
  const quick = useAction(async () => {
    const saved = await addUptimeCheck(id, emptyInput(website));
    setChosen(saved.id);
    list.reload();
  });
  const primary = emptyInput(website).domain;
  const full = checks.length >= UPTIME_LIMITS.perWebsite;
  const error = list.error ?? run.error ?? toggle.error ?? remove.error ?? detail.error;

  // The data stays while the list loads again, so the page does not change between the two forms.
  const empty = list.data !== undefined && list.data.length === 0;
  return (
    <div className="stack">
      {empty ? (
        <Card title="Uptime checks">
          <div className="card-body">
            <Empty title="No uptime checks">
              <p>An uptime check requests an address of the website on a schedule. When the address does not answer, or gives an error, the panel sends an email.</p>
              <FormError error={quick.error ?? list.error} />
              <div className="btn-group" style={{ justifyContent: "center", marginTop: 14 }}>
                {primary && <button type="button" className="btn btn-primary" disabled={quick.busy} onClick={() => void quick.run()}>{quick.busy ? "Adding…" : `Check https://${primary}/ every 5 minutes`}</button>}
                <button type="button" className="btn" onClick={() => setForm({ check: null })}>Add a different check</button>
              </div>
            </Empty>
          </div>
        </Card>
      ) : (
        <Card title="Uptime checks" actions={<button type="button" className="btn" disabled={full} onClick={() => setForm({ check: null })}>Add check</button>}>
          {error && <div className="card-body" style={{ paddingBottom: 0 }}><FormError error={error} /></div>}
          <TableWrap>
            <table aria-busy={list.loading}>
              <thead><tr><th scope="col">Address</th><th scope="col">State</th><th scope="col">Last check</th><th scope="col">Uptime</th><th scope="col"><span className="visually-hidden">Actions</span></th></tr></thead>
              <tbody>
                {checks.map((c) => (
                  <tr key={c.id} aria-current={selected?.id === c.id ? "true" : undefined} className={selected?.id === c.id ? "row-selected" : undefined}>
                    <td>
                      <button type="button" className="btn-link uptime-url" onClick={() => setChosen(c.id)}>{c.url}<span className="visually-hidden">: show the details</span></button>
                      <span className="cell-sub">Every {intervalText(c.intervalSeconds)} · {expectation(c)}</span>
                    </td>
                    <td><UptimeStateText check={c} /></td>
                    <td><LastResult check={c} /></td>
                    <td className="nowrap"><UptimeCell check={c} /></td>
                    <td>
                      <RowMenu label={`Actions for ${c.url}`} actions={[
                        { label: "Check now", disabled: run.busy || c.state === "paused", onSelect: () => void run.run(c) },
                        { label: "Edit", onSelect: () => setForm({ check: c }) },
                        { label: c.enabled ? "Turn off" : "Turn on", disabled: toggle.busy, onSelect: () => void toggle.run(c) },
                        { label: "Remove", danger: true, onSelect: () => setRemoving(c) },
                      ]} />
                    </td>
                  </tr>
                ))}
                {checks.length === 0 && <tr><td colSpan={5} className="muted">{list.loading ? "Loading…" : "The panel cannot load the checks."}</td></tr>}
              </tbody>
            </table>
          </TableWrap>
          <p className="card-body muted small" style={{ paddingTop: 12 }}>
            The panel requests each address from its own server. It does not follow a redirect. When a check is down, the panel sends an email to the users of the website{full ? `. A website can have ${UPTIME_LIMITS.perWebsite} checks` : ""}. Each user can turn off these emails in <Link to="/settings/notifications">Settings</Link>.
          </p>
        </Card>
      )}
      {!empty && detail.data && (
        <CheckDetail
          detail={detail.data}
          running={run.busy}
          onRun={() => void run.run(detail.data!)}
          onEdit={() => setForm({ check: detail.data! })}
          result={result && result.checkId === detail.data.id ? result.result : null}
          closeResult={() => setResult(null)}
        />
      )}
      {form && <CheckForm website={website} check={form.check} onClose={() => setForm(null)} onSaved={(c) => { setForm(null); setChosen(c.id); list.reload(); }} />}
      {removing && (
        <Modal
          title="Remove this uptime check?"
          onClose={() => setRemoving(null)}
          footer={<><button type="button" className="btn" onClick={() => setRemoving(null)}>Cancel</button><button type="button" className="btn btn-danger-solid" disabled={remove.busy} onClick={() => void remove.run(removing)}>Remove</button></>}
        >
          <p>The panel stops requesting <code>{removing.url}</code>, and removes its results and its incidents.</p>
          <FormError error={remove.error} />
        </Modal>
      )}
    </div>
  );
}

/** The uptime on the overview of a website: the state of each check, with a link to the tab. */
export function UptimeCard({ website }: { website: Website }) {
  const list = useLoad(() => listUptimeChecks(website.id), [website.id]);
  const checks = list.data ?? [];
  return (
    <Card title="Uptime" actions={<Link to={`/websites/${website.id}/uptime`}>{checks.length ? "See details" : "Add check"}</Link>}>
      <div className="card-body">
        {list.error ? <FormError error={list.error} /> : checks.length === 0 ? (
          <p className="muted">{list.loading ? "Loading…" : "No uptime check. Add a check, and the panel sends an email when the website is down."}</p>
        ) : (
          <ul className="list">
            {checks.map((c) => (
              <li key={c.id} style={{ display: "block" }}>
                <Status tone={UPTIME_STATE[c.state].tone}>{UPTIME_STATE[c.state].label}</Status> <span style={{ wordBreak: "break-all" }}>{c.url}</span>
                <span className="cell-sub">
                  {c.state === "down" && c.incident ? `${c.incident.error} Since ${clockTime(c.incident.startedAt)}.` : c.state === "paused" ? c.pausedReason : `${uptimeText(c.uptime.month)} in the last 30 days`}
                </span>
              </li>
            ))}
          </ul>
        )}
      </div>
    </Card>
  );
}
