import { Check } from "lucide-react";
import { useState } from "react";
import { Link } from "react-router-dom";
import { useAction, useStore } from "../api/hooks";
import { checkImage, pauseRollout, rollBackAll, setImagePolicy, startRollout } from "../api/store";
import type { Rollout, UpdatePolicy } from "../api/types";
import { Topbar } from "../components/Layout";
import { Card, ConfirmButton, FormError, PageHeader, Status, TableWrap } from "../components/ui";
import { clockTime, shortDate } from "../lib/format";
import { POLICY_LABEL } from "../lib/labels";
import { ServersTable } from "./ServersPage";

const STAGE_STATE = { done: "done", in_progress: "in progress", waiting: "waiting" } as const;

/** The browser keeps the ID of the last ended rollout that the user closed. */
const DISMISSED = "cloudfactory.rollout.dismissed";

function readDismissed(): string {
  try {
    return localStorage.getItem(DISMISSED) ?? "";
  } catch {
    return "";
  }
}

/**
 * A rollout in progress always shows. A rollout that ended shows for one day, or until the
 * user closes it. Before, the last rollout stayed on the page for all time.
 */
export function showRollout(rollout: Rollout | undefined, dismissed: string, now = Date.now()): boolean {
  if (!rollout) return false;
  if (rollout.state === "running" || rollout.state === "paused") return true;
  if (rollout.id === dismissed) return false;
  return now - new Date(rollout.finishedAt ?? rollout.startedAt).getTime() < 864e5;
}

function RolloutCard({ rollout, onDismiss }: { rollout: Rollout; onDismiss: () => void }) {
  const total = rollout.websites;
  const waiting = Math.max(0, total - rollout.healthy - rollout.rolledBack);
  const active = rollout.state === "running" || rollout.state === "paused";
  // Batch 2, finding 11: show the errors of these buttons.
  const pause = useAction(pauseRollout);
  const back = useAction(async () => { await rollBackAll(); });
  return (
    <section className="card" aria-labelledby="rollout-title" style={{ padding: 22 }}>
      <div className="card-header" style={{ padding: 0 }}>
        <div>
          <h2 id="rollout-title" style={{ fontSize: 19 }}>
            {rollout.state === "done" ? "Rolled out" : rollout.state === "rolled_back" ? "Rolled back" : "Rolling out"} {rollout.title}{" "}
            {rollout.severity === "security" && <span className="badge badge-warning" style={{ marginLeft: 6 }}>Security</span>}
            {rollout.state === "paused" && <span className="badge badge-neutral" style={{ marginLeft: 6 }}>Paused</span>}
          </h2>
          <p className="muted" style={{ marginTop: 4 }}>Started {clockTime(rollout.startedAt)}{rollout.window && <> · maintenance window {rollout.window}</>} · {total} {total === 1 ? "website" : "websites"} on {rollout.servers} {rollout.servers === 1 ? "server" : "servers"}</p>
          {!active && total === 0 && <p className="muted" style={{ marginTop: 4 }}>No website used this image, so the panel changed the version at once. New websites get {rollout.toTag}.</p>}
        </div>
        {!active && <button type="button" className="btn" onClick={onDismiss}>Dismiss</button>}
        {active && (
          <div className="btn-group">
            <button type="button" className="btn" disabled={pause.busy} onClick={() => void pause.run(rollout.state === "running")}>{rollout.state === "running" ? "Pause" : "Resume"}</button>
            <ConfirmButton className="btn btn-danger" title="Roll back all websites?" confirm="Roll back all" message={<p>All {rollout.healthy} updated websites go back to {rollout.fromTag}. Each website gets a health check after the switch.</p>} onConfirm={() => void back.run()}>
              Roll back all
            </ConfirmButton>
          </div>
        )}
      </div>
      <FormError error={pause.error ?? back.error} />
      <ol className="stages" aria-label="Stages">
        {rollout.stages.map((s, i) => (
          <li key={s.name} className={`stage stage-${s.state}`} aria-current={s.state === "in_progress" ? "step" : undefined}>
            <span className="stage-state">Stage {i + 1} · {STAGE_STATE[s.state]}</span>
            <strong>{s.name}</strong>
            <span>{s.note ?? `${s.name === "Everyone" && s.websites !== 1 ? "All " : ""}${s.websites} ${s.websites === 1 ? "website" : "websites"}`}</span>
          </li>
        ))}
      </ol>
      <div className="counts" aria-live="polite">
        <div><strong>{rollout.healthy}</strong><span>Updated and healthy</span></div>
        <div><strong>{waiting}</strong><span>Waiting</span></div>
        <div><strong className={rollout.rolledBack ? "text-danger" : ""}>{rollout.rolledBack}</strong><span>Rolled back</span></div>
      </div>
      <div className="split-meter" role="img" aria-label={`${rollout.healthy} healthy, ${rollout.rolledBack} rolled back, ${waiting} waiting`}>
        <span className="ok" style={{ width: `${total ? (rollout.healthy / total) * 100 : rollout.state === "done" ? 100 : 0}%` }} />
        <span className="bad" style={{ width: `${total ? (rollout.rolledBack / total) * 100 : 0}%` }} />
      </div>
      {rollout.rollbacks.map((r) => (
        <div key={r.website} className="alert alert-danger">
          <p><strong>{r.website}</strong> {r.reason}</p>
          <Link className="btn" to="/jobs">View logs</Link>
        </div>
      ))}
      <ul className="checks" aria-label="Checks">
        {rollout.checks.map((c) => (
          <li key={c.label} className={c.passed ? "passed" : ""}>
            {c.passed && <Check size={15} aria-hidden="true" />}
            <span>{c.passed === null ? "" : c.passed ? <span className="visually-hidden">Passed: </span> : <span className="visually-hidden">Failed: </span>}{c.label}</span>
          </li>
        ))}
      </ul>
    </section>
  );
}

export function UpdatesPage() {
  const state = useStore();
  const start = useAction(startRollout);
  const check = useAction(checkImage);
  const [editing, setEditing] = useState<string | null>(null);
  const [dismissed, setDismissed] = useState(readDismissed);
  const dismiss = (id: string) => {
    setDismissed(id);
    try {
      localStorage.setItem(DISMISSED, id);
    } catch {
      // Without storage, the card comes back after the next page load, for one day.
    }
  };
  const rollout = state.rollout;
  const active = rollout && (rollout.state === "running" || rollout.state === "paused");
  return (
    <>
      <Topbar placeholder="Search servers, images, customers and jobs">
        {active && <Link to="/updates" className="pill pill-warning"><Status tone="warning">1 rollout in progress</Status></Link>}
      </Topbar>
      <main id="main" className="content content-wide">
        <PageHeader title="Images and updates" description="Every runtime is a signed container image. Roll it out in stages, check each website's health, and roll back on its own if a check fails." />
        {rollout && showRollout(rollout, dismissed) && <RolloutCard rollout={rollout} onDismiss={() => dismiss(rollout.id)} />}
        <Card title="Runtime images" className="mt" actions={<Link className="btn" to="/apps">Add your own image</Link>}>
          <FormError error={start.error ?? check.error} />
          <TableWrap>
            <table>
              <thead><tr><th scope="col">Image</th><th scope="col">Running</th><th scope="col">Available</th><th scope="col">Websites</th><th scope="col">Update policy</th><th scope="col"><span className="visually-hidden">Actions</span></th></tr></thead>
              <tbody>
                {state.images.map((i) => (
                  <tr key={i.id}>
                    <td>
                      {i.name}
                      {i.supportEndsAt && new Date(i.supportEndsAt).getTime() - Date.now() < 90 * 864e5 && <span className="cell-sub note-warning">PHP {i.phpVersion} support ends {shortDate(i.supportEndsAt)}</span>}
                      {/* Before, the full error was a part of the policy text. */}
                      {i.checkError && (
                        <details className="cell-sub check-error">
                          <summary className="note-warning">The last check for a new version failed</summary>
                          <span className="small">{i.checkError}</span>
                        </details>
                      )}
                    </td>
                    <td><code>{i.running}</code></td>
                    <td>
                      {i.available ? (
                        <><code>{i.available}</code> {i.availableNote === "rolling_out" ? <span className="text-info small">rolling out</span> : i.availableNote === "queued" ? <span className="muted small">queued next</span> : null}</>
                      ) : i.checkError
                        // Audit item U9: after a failed check, the panel does not know if the image is up to date.
                        ? <span className="note-warning">Unknown: the last check failed</span>
                        : <span className="muted">Up to date</span>}
                    </td>
                    <td>{i.websites}</td>
                    <td>
                      {editing === i.id ? (
                        <>
                          <label className="visually-hidden" htmlFor={`pol-${i.id}`}>Update policy for {i.name}</label>
                          <select id={`pol-${i.id}`} className="select" autoFocus value={i.policy} onBlur={() => setEditing(null)} onChange={(e) => { setImagePolicy(i.id, e.target.value as UpdatePolicy); setEditing(null); }}>
                            {Object.entries(POLICY_LABEL).map(([k, v]) => <option key={k} value={k}>{v}</option>)}
                          </select>
                        </>
                      ) : (
                        <>
                          {POLICY_LABEL[i.policy]}{i.policyNote && i.policyNote !== "Email owners about upgrading" ? `, ${i.policyNote.charAt(0).toLowerCase()}${i.policyNote.slice(1)}` : ""}
                          {/* Before, the text itself was the button, and nothing showed that. */}
                          {" "}<button type="button" className="btn-link small" onClick={() => setEditing(i.id)}>Change<span className="visually-hidden"> the update policy for {i.name}</span></button>
                        </>
                      )}
                      {i.policyNote === "Email owners about upgrading" && <> · <Link to="/customers">Email owners about upgrading</Link></>}
                    </td>
                    <td className="cell-actions" style={{ width: 140 }}>
                      {i.available && i.availableNote !== "rolling_out" && (
                        <button type="button" className="btn btn-sm" disabled={!!active} onClick={() => void start.run(i.id)}>Start rollout</button>
                      )}
                      {!i.available && <button type="button" className="btn btn-sm" disabled={check.busy} onClick={() => void check.run(i.id)}>Check now</button>}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </TableWrap>
        </Card>
        <Card title="Servers" className="mt" actions={<Link className="btn btn-primary" to="/servers?add=1">Add server</Link>}>
          <ServersTable />
          <div className="card-body" style={{ paddingTop: 16, borderTop: "1px solid var(--border)" }}>
            <p className="muted">To add a server, select <strong>Add server</strong>. The panel then shows the command to run on the new server.</p>
          </div>
        </Card>
      </main>
    </>
  );
}
