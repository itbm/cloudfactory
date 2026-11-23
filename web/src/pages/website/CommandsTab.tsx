import { useEffect, useState } from "react";
import { useAction, useLoad, useStore } from "../../api/hooks";
import { listCommands, listTerminalSessions, readCommand, readTerminalRecording, readTerminalSession, runCommand } from "../../api/store";
import type { TerminalSession, Website, WebsiteCommand } from "../../api/types";
import { Card, Field, FormError, Modal, Status, TableWrap } from "../../components/ui";
import { RecordingView, WebTerminal } from "../../features/WebTerminal";
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
  // The output of the terminal is in the recording of the session.
  if (c.source === "terminal") return <Status tone="neutral">In the terminal</Status>;
  if (c.state === "running") return <Status tone="info">Running</Status>;
  if (c.state === "failed") return <Status tone="danger">Did not start</Status>;
  if (c.timedOut) return <Status tone="warning">Stopped at the time limit</Status>;
  return <Status tone={c.exitCode === 0 ? "success" : "danger"}>Exit code {c.exitCode}</Status>;
}

/** "4 s", "2 min 5 s": the time between two times. */
function between(from: string | null, to: string | null): string | null {
  if (!from || !to) return null;
  const s = Math.max(0, Math.round((new Date(to).getTime() - new Date(from).getTime()) / 1000));
  if (s < 1) return "less than 1 s";
  if (s < 3600) return s < 60 ? `${s} s` : `${Math.floor(s / 60)} min${s % 60 ? ` ${s % 60} s` : ""}`;
  const m = Math.floor(s / 60);
  return `${Math.floor(m / 60)} h${m % 60 ? ` ${m % 60} min` : ""}`;
}

/** The time from the start to the end of a command. A command of the terminal has none. */
const duration = (c: WebsiteCommand) => (c.source === "terminal" ? null : between(c.startedAt, c.finishedAt));

/** The state of a session of the terminal, as a status with a text label. */
function SessionState({ session: s }: { session: TerminalSession }) {
  if (s.state === "opening") return <Status tone="info">Opening</Status>;
  if (s.state === "open") return <Status tone="success">Open</Status>;
  if (s.state === "failed") return <Status tone="danger">Did not open</Status>;
  return <Status tone="neutral">Closed</Status>;
}

const when = (iso: string) => `${shortDate(iso)}, ${clockTime(iso)}`;

/**
 * The web terminal, Run a command and the command log (FR-802). A user opens a shell in the
 * container of the website, or runs one command line without a terminal. The panel records each
 * command: who ran it, when, from which address, and the result. Each session of the terminal has
 * a recording of its output.
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
  const [sessionPage, setSessionPage] = useState(1);
  const sessions = useLoad(() => listTerminalSessions(id, sessionPage), [id, active, sessionPage]);
  const [session, setSession] = useState<string | null>(null);
  // The agent sends the commands of the terminal every 2 seconds.
  const [terminalOpen, setTerminalOpen] = useState(false);
  const reloadLog = log.reload;
  useEffect(() => {
    if (!terminalOpen) return;
    const t = setInterval(reloadLog, 5000);
    return () => clearInterval(t);
  }, [terminalOpen, reloadLog]);

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
      <Card title="Terminal">
        <WebTerminal websiteId={id} label={`Terminal of ${website.name}`} disabled={!running} onChange={(open) => { log.reload(); sessions.reload(); setTerminalOpen(open); }} />
        <div className="card-body" style={{ paddingTop: 0 }}>
          {!running && <p className="muted small" style={{ marginBottom: 10 }}>The website does not run. Start it before you open the terminal.</p>}
          <p className="muted small">
            The terminal is a shell in the container of the website, in the directory <code>/app</code>, as the Unix user <code>{website.unixUser}</code>, with the limits of the website. Use it for Composer, WP-CLI, Artisan and other tools. The command log keeps each command that you type, with your name, the time and your IP address. The panel keeps a recording of the output. The terminal closes after 30 minutes with no input.
          </p>
        </div>
      </Card>

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
                    {c.source === "terminal" && c.sessionId
                      ? <button type="button" className="btn btn-sm" onClick={() => setSession(c.sessionId)}>Show session<span className="visually-hidden"> of {c.command}</span></button>
                      : <button type="button" className="btn btn-sm" onClick={() => setShown(c.id)}>Show output<span className="visually-hidden"> of {c.command}</span></button>}
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

      <Card title="Terminal sessions">
        {sessions.error && <div className="card-body" style={{ paddingBottom: 0 }}><FormError error={sessions.error} /></div>}
        <TableWrap>
          <table aria-busy={sessions.loading}>
            <thead><tr><th scope="col">Opened</th><th scope="col">Person or token</th><th scope="col">State</th><th scope="col">Commands</th><th scope="col"><span className="visually-hidden">Actions</span></th></tr></thead>
            <tbody>
              {(sessions.data?.data ?? []).map((s) => (
                <tr key={s.id}>
                  <td className="nowrap">{when(s.createdAt)}<span className="cell-sub">{relativeTime(s.createdAt)}</span></td>
                  <td>{s.actor}{s.ip && <span className="cell-sub mono">{s.ip}</span>}</td>
                  <td><SessionState session={s} />{s.endReason && <span className="cell-sub">{s.endReason}</span>}</td>
                  <td>{s.commandCount}{between(s.startedAt, s.endedAt) && <span className="cell-sub">{between(s.startedAt, s.endedAt)}</span>}</td>
                  <td style={{ textAlign: "right" }}>
                    <button type="button" className="btn btn-sm" onClick={() => setSession(s.id)}>Show session<span className="visually-hidden"> of {when(s.createdAt)}</span></button>
                  </td>
                </tr>
              ))}
              {(sessions.data?.data ?? []).length === 0 && <tr><td colSpan={5} className="muted">{sessions.loading ? "Loading…" : "No one opened the terminal yet."}</td></tr>}
            </tbody>
          </table>
        </TableWrap>
        {sessions.data && sessions.data.meta.lastPage > 1 && (
          <nav className="pager" aria-label="Pages of the terminal sessions">
            <span className="muted" role="status">{sessions.data.meta.total} sessions</span>
            <span className="pager-buttons">
              <button type="button" className="btn btn-sm" disabled={sessionPage <= 1} onClick={() => setSessionPage(sessionPage - 1)}>Newer</button>
              <span className="muted">Page {sessions.data.meta.currentPage} of {sessions.data.meta.lastPage}</span>
              <button type="button" className="btn btn-sm" disabled={sessionPage >= sessions.data.meta.lastPage} onClick={() => setSessionPage(sessionPage + 1)}>Older</button>
            </span>
          </nav>
        )}
      </Card>

      {shown && <CommandOutput websiteId={id} commandId={shown} onClose={() => setShown(null)} onRunAgain={(c) => { setCommand(c); setShown(null); }} />}
      {session && <SessionView websiteId={id} sessionId={session} onClose={() => setSession(null)} />}
    </div>
  );
}

/** A session of the terminal: who opened it, its commands and the recording of its output. */
function SessionView({ websiteId, sessionId, onClose }: { websiteId: string; sessionId: string; onClose: () => void }) {
  const loaded = useLoad(async () => {
    const s = await readTerminalSession(websiteId, sessionId);
    const [commands, recording] = await Promise.all([
      listCommands(websiteId, 1, "", sessionId),
      s.hasRecording ? readTerminalRecording(websiteId, sessionId) : Promise.resolve(null),
    ]);
    return { session: s, commands: commands.data.slice().reverse(), recording };
  }, [websiteId, sessionId]);
  const d = loaded.data;
  const download = () => {
    if (!d?.recording) return;
    const url = URL.createObjectURL(new Blob([d.recording], { type: "application/x-asciicast" }));
    const a = document.createElement("a");
    a.href = url;
    a.download = `terminal-${sessionId}.cast`;
    a.click();
    setTimeout(() => URL.revokeObjectURL(url), 1000);
  };
  return (
    <Modal wide title="Terminal session" onClose={onClose} footer={
      <>
        {d?.recording && <button type="button" className="btn" onClick={download}>Download the recording</button>}
        <button type="button" className="btn btn-primary" onClick={onClose}>Close</button>
      </>
    }>
      <FormError error={loaded.error} />
      {!d ? <p className="muted">Loading…</p> : (
        <>
          <table className="kv" style={{ marginBottom: 14 }}>
            <tbody>
              <tr><th scope="row">State</th><td><SessionState session={d.session} />{d.session.endReason && <span className="cell-sub">{d.session.endReason}{d.session.exitCode !== null ? ` Exit code ${d.session.exitCode}.` : ""}</span>}</td></tr>
              <tr><th scope="row">By</th><td>{d.session.actor}{d.session.ip && <span className="cell-sub mono">{d.session.ip}</span>}</td></tr>
              <tr><th scope="row">Opened</th><td>{when(d.session.createdAt)}{between(d.session.startedAt, d.session.endedAt) && <span className="cell-sub">Open for {between(d.session.startedAt, d.session.endedAt)}</span>}</td></tr>
            </tbody>
          </table>
          <h3 className="section-title">Commands</h3>
          {d.commands.length === 0 ? <p className="muted" style={{ marginBottom: 14 }}>The session has no commands.</p> : (
            <ol className="command-list" aria-label="Commands of the session">
              {d.commands.map((c) => <li key={c.id}><span className="muted small nowrap">{clockTime(c.createdAt)}</span> <code>{c.command}</code></li>)}
            </ol>
          )}
          <h3 className="section-title">Output</h3>
          {d.recording ? (
            <>
              {d.session.recordingCut && <p className="muted small" style={{ marginBottom: 8 }}>The recording stopped at 1 MiB. The command log has all the commands.</p>}
              <RecordingView text={d.recording} label="Output of the session" />
            </>
          ) : <p className="muted">{d.session.state === "open" || d.session.state === "opening" ? "The recording is ready when the session ends." : "The session has no recording."}</p>}
        </>
      )}
    </Modal>
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
