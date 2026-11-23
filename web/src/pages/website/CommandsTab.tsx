import { useEffect, useState } from "react";
import { useAction, useLoad, useStore } from "../../api/hooks";
import { listCommands, readCommand, runCommand } from "../../api/store";
import type { Website, WebsiteCommand } from "../../api/types";
import { Card, Field, FormError, Modal, Status, TableWrap } from "../../components/ui";
import { clockTime, relativeTime, shortDate } from "../../lib/format";
import { jobActive } from "../../lib/labels";

/** The time limits that a user can select (WebsiteCommandController::TIMEOUT_MAX is 3600). */
const LIMITS = [
  [60, "1 minute"],
  [300, "5 minutes"],
  [600, "10 minutes"],
  [1800, "30 minutes"],
  [3600, "1 hour"],
] as const;

const EXAMPLES = ["php artisan migrate --force", "composer install --no-dev", "wp plugin list", "php -v"];

/** The result of a command, as a status with a text label. */
export function CommandResult({ command: c }: { command: WebsiteCommand }) {
  if (c.state === "running") return <Status tone="info">Running</Status>;
  if (c.state === "failed") return <Status tone="danger">Did not start</Status>;
  if (c.timedOut) return <Status tone="warning">Stopped at the time limit</Status>;
  return <Status tone={c.exitCode === 0 ? "success" : "danger"}>Exit code {c.exitCode}</Status>;
}

/** "4 s", "2 min 5 s": the time from the start to the end of a command. */
function duration(c: WebsiteCommand): string | null {
  if (!c.startedAt || !c.finishedAt) return null;
  const s = Math.max(0, Math.round((new Date(c.finishedAt).getTime() - new Date(c.startedAt).getTime()) / 1000));
  if (s < 1) return "less than 1 s";
  return s < 60 ? `${s} s` : `${Math.floor(s / 60)} min${s % 60 ? ` ${s % 60} s` : ""}`;
}

const when = (iso: string) => `${shortDate(iso)}, ${clockTime(iso)}`;

/**
 * Run a command and the command log (FR-802). A user runs one command line in the container of
 * the website without a terminal. The panel records each command: who ran it, when, from which
 * address, and the result.
 */
export function CommandsTab({ website }: { website: Website }) {
  const state = useStore();
  const id = website.id;
  // The log loads again when a job of the website starts or ends: a command is a job.
  const active = state.jobs.filter((j) => j.websiteId === id && jobActive(j)).length;
  const [page, setPage] = useState(1);
  const [typed, setTyped] = useState("");
  const [search, setSearch] = useState("");
  useEffect(() => {
    const t = setTimeout(() => { setSearch(typed.trim()); setPage(1); }, 300);
    return () => clearTimeout(t);
  }, [typed]);
  const log = useLoad(() => listCommands(id, page, search), [id, active, page, search]);
  const rows = log.data?.data ?? [];
  const meta = log.data?.meta;

  const [command, setCommand] = useState("");
  const [limit, setLimit] = useState(600);
  const [result, setResult] = useState<WebsiteCommand | null>(null);
  const [shown, setShown] = useState<string | null>(null);
  const run = useAction(async () => {
    setResult(null);
    setResult(await runCommand(id, command, limit));
    log.reload();
  });
  const running = website.status === "running";

  return (
    <div className="stack">
      <Card title="Run a command">
        <form className="card-body" noValidate onSubmit={(e) => { e.preventDefault(); void run.run(); }}>
          <FormError error={run.error && !run.error.fields.command && !run.error.fields.timeoutSeconds ? run.error : null} />
          <div className="inline-form command-form">
            <Field label="Command" error={run.error?.fields.command}>
              {(p) => <input {...p} className="input mono" value={command} onChange={(e) => setCommand(e.target.value)} placeholder={EXAMPLES[0]} list="command-examples" spellCheck={false} autoCapitalize="none" autoComplete="off" />}
            </Field>
            <datalist id="command-examples">{EXAMPLES.map((c) => <option key={c} value={c} />)}</datalist>
            <Field label="Time limit" error={run.error?.fields.timeoutSeconds}>
              {(p) => (
                <select {...p} className="select" value={limit} onChange={(e) => setLimit(Number(e.target.value))}>
                  {LIMITS.map(([v, l]) => <option key={v} value={v}>{l}</option>)}
                </select>
              )}
            </Field>
            <div className="field"><span className="label" aria-hidden="true">&nbsp;</span><button type="submit" className="btn btn-primary" disabled={run.busy || !running || !command.trim()}>{run.busy ? "Running…" : "Run"}</button></div>
          </div>
          {!running && <p className="muted small" style={{ marginTop: 10 }}>The website does not run. Start it before you run a command.</p>}
          <p className="muted small" style={{ marginTop: 10 }}>
            The server runs the command in the container of the website, in the directory <code>/app</code>, as the Unix user <code>{website.unixUser}</code>. The command has the limits of the website. The command cannot read from the keyboard. The command log keeps each command, with your name, the time, your IP address and the result.
          </p>
        </form>
        {(run.busy || result) && (
          <div className="card-body" style={{ paddingTop: 0 }} aria-live="polite">
            <p style={{ marginBottom: 8 }}>
              {result ? <CommandResult command={result} /> : <Status tone="info">Running</Status>}
              {result && duration(result) && <span className="muted small"> · {duration(result)}</span>}
            </p>
            {result && <pre className="log-view" tabIndex={0} aria-label="Output of the command">{result.output || "The command gave no output."}</pre>}
          </div>
        )}
      </Card>

      <Card title="Command log" actions={
        <div className="filters">
          <label className="visually-hidden" htmlFor="command-q">Search the commands</label>
          <input id="command-q" type="search" className="input" style={{ width: 240 }} placeholder="Search the commands" value={typed} onChange={(e) => setTyped(e.target.value)} />
        </div>
      }>
        {log.error && <div className="card-body" style={{ paddingBottom: 0 }}><FormError error={log.error} /></div>}
        <TableWrap>
          <table aria-busy={log.loading}>
            <thead><tr><th scope="col">Time</th><th scope="col">Person or token</th><th scope="col">Command</th><th scope="col">Result</th><th scope="col"><span className="visually-hidden">Actions</span></th></tr></thead>
            <tbody>
              {rows.map((c) => (
                <tr key={c.id}>
                  <td className="nowrap">{when(c.createdAt)}<span className="cell-sub">{relativeTime(c.createdAt)}</span></td>
                  <td>{c.actor}{c.ip && <span className="cell-sub mono">{c.ip}</span>}</td>
                  <td><code style={{ wordBreak: "break-all" }}>{c.command}</code></td>
                  <td className="nowrap"><CommandResult command={c} />{duration(c) && <span className="cell-sub">{duration(c)}</span>}</td>
                  <td style={{ textAlign: "right" }}>
                    <button type="button" className="btn btn-sm" onClick={() => setShown(c.id)}>Show output<span className="visually-hidden"> of {c.command}</span></button>
                  </td>
                </tr>
              ))}
              {rows.length === 0 && <tr><td colSpan={5} className="muted">{log.loading ? "Loading…" : search ? "No commands match the search." : "No commands yet."}</td></tr>}
            </tbody>
          </table>
        </TableWrap>
        {meta && meta.lastPage > 1 && (
          <nav className="pager" aria-label="Pages of the command log">
            <span className="muted" role="status">{meta.total} commands</span>
            <span className="pager-buttons">
              <button type="button" className="btn btn-sm" disabled={page <= 1} onClick={() => setPage(page - 1)}>Newer</button>
              <span className="muted">Page {meta.currentPage} of {meta.lastPage}</span>
              <button type="button" className="btn btn-sm" disabled={page >= meta.lastPage} onClick={() => setPage(page + 1)}>Older</button>
            </span>
          </nav>
        )}
      </Card>

      {shown && <CommandOutput websiteId={id} commandId={shown} onClose={() => setShown(null)} onRunAgain={(c) => { setCommand(c); setShown(null); }} />}
    </div>
  );
}

/** One entry of the command log, with the end of its output. */
function CommandOutput({ websiteId, commandId, onClose, onRunAgain }: { websiteId: string; commandId: string; onClose: () => void; onRunAgain: (command: string) => void }) {
  const entry = useLoad(() => readCommand(websiteId, commandId), [websiteId, commandId]);
  const c = entry.data;
  return (
    <Modal wide title="Command" onClose={onClose} footer={
      <>
        {c && <button type="button" className="btn" onClick={() => onRunAgain(c.command)}>Use this command again</button>}
        <button type="button" className="btn btn-primary" onClick={onClose}>Close</button>
      </>
    }>
      <FormError error={entry.error} />
      {!c ? <p className="muted">Loading…</p> : (
        <>
          <p style={{ marginBottom: 10 }}><code style={{ wordBreak: "break-all" }}>{c.command}</code></p>
          <table className="kv" style={{ marginBottom: 14 }}>
            <tbody>
              <tr><th scope="row">Result</th><td><CommandResult command={c} /></td></tr>
              <tr><th scope="row">By</th><td>{c.actor}{c.ip && <span className="cell-sub mono">{c.ip}</span>}</td></tr>
              <tr><th scope="row">Time</th><td>{when(c.createdAt)}{duration(c) && <span className="cell-sub">Ran for {duration(c)}</span>}</td></tr>
              {c.timeoutSeconds ? <tr><th scope="row">Time limit</th><td>{LIMITS.find(([v]) => v === c.timeoutSeconds)?.[1] ?? `${c.timeoutSeconds} s`}</td></tr> : null}
            </tbody>
          </table>
          {c.state === "running" ? <p className="muted">The command runs now. The output shows when the command ends.</p>
            : <pre className="log-view" tabIndex={0} aria-label="Output">{c.output || "The command gave no output."}</pre>}
        </>
      )}
    </Modal>
  );
}
