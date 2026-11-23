import { useCallback, useEffect, useRef, useState, type FormEvent } from "react";
import { ApiError, searchLogs } from "../api/store";
import type { LogEntry, LogQuery, LogSearchResult, LogSource } from "../api/types";
import { bytes, count, percent } from "../lib/format";
import { FormError } from "../components/ui";

/** The number of entries on each page. The web fixtures have the first page of each website. */
export const LOG_PAGE = 50;

const RANGES = [
  { id: "all", label: "Any time", minutes: 0 },
  { id: "15m", label: "Last 15 minutes", minutes: 15 },
  { id: "1h", label: "Last hour", minutes: 60 },
  { id: "24h", label: "Last 24 hours", minutes: 24 * 60 },
  { id: "7d", label: "Last 7 days", minutes: 7 * 24 * 60 },
] as const;

const SOURCES: { id: LogSource; label: string }[] = [
  { id: "access", label: "Requests" },
  { id: "output", label: "Output" },
  { id: "error", label: "Errors" },
];

const CLASSES = ["2xx", "3xx", "4xx", "5xx"] as const;
const METHODS = ["GET", "POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS"];

const LOCALE = "en-GB";

/** "14 Nov 10:15:02" in the time zone of the browser. */
function logTime(iso: string): string {
  const d = new Date(iso);
  const day = d.toLocaleDateString(LOCALE, { day: "numeric", month: "short" });
  const time = d.toLocaleTimeString(LOCALE, { hour: "2-digit", minute: "2-digit", second: "2-digit" });
  return `${day} ${time}`;
}

function duration(ms: number): string {
  return ms >= 1000 ? `${(ms / 1000).toFixed(1)} s` : `${Math.round(ms)} ms`;
}

/** One line of the download: the requests in a form near the combined log format. */
function textLine(e: LogEntry): string {
  const a = e.access;
  if (!a) return `${e.at} ${e.source} ${e.text}`;
  return `${e.at} ${a.client} "${a.method} ${a.host}${a.uri} ${a.proto}" ${a.status} ${a.sizeBytes} ${a.durationMs}ms "${a.referer}" "${a.userAgent}"`;
}

type Target = { websiteId: string } | { serverId: string };

/**
 * The search of the logs: the access log of the edge proxy, and for a website also the output
 * and the errors of its containers. The newest entry comes first, and "Load older entries"
 * reads the next page of the same search.
 */
export function LogSearch({ target, name }: { target: Target; name: string }) {
  const website = "websiteId" in target;
  const targetKey = website ? `w:${target.websiteId}` : `s:${target.serverId}`;
  const [draft, setDraft] = useState({ q: "", client: "" });
  const [words, setWords] = useState({ q: "", client: "" });
  const [sources, setSources] = useState<LogSource[]>(["access", "output", "error"]);
  const [classes, setClasses] = useState<string[]>([]);
  const [method, setMethod] = useState("");
  const [range, setRange] = useState<(typeof RANGES)[number]["id"]>("all");
  // The time of the search. "Refresh" sets a new time, so that a relative range moves on.
  const [at, setAt] = useState(() => Date.now());
  const [result, setResult] = useState<LogSearchResult | null>(null);
  const [error, setError] = useState<ApiError | null>(null);
  const [loading, setLoading] = useState<"first" | "more" | null>("first");
  // Each new search has a new number. An answer for an earlier search is not shown.
  const generation = useRef(0);

  const minutes = RANGES.find((r) => r.id === range)?.minutes ?? 0;
  const query: LogQuery = {
    sources: website && sources.length < SOURCES.length ? sources : undefined,
    q: words.q,
    status: classes,
    method: method ? [method] : undefined,
    client: words.client,
    from: minutes ? new Date(at - minutes * 60_000).toISOString() : undefined,
  };
  const queryKey = JSON.stringify(query);

  const fail = (e: unknown) => setError(e instanceof ApiError ? e : new ApiError((e as Error).message));

  useEffect(() => {
    const turn = ++generation.current;
    setLoading("first");
    searchLogs(target, { ...query, limit: LOG_PAGE, summary: true }).then(
      (r) => { if (turn === generation.current) { setResult(r); setError(null); setLoading(null); } },
      (e: unknown) => { if (turn === generation.current) { fail(e); setLoading(null); } },
    );
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [targetKey, queryKey, at]);

  const more = useCallback(() => {
    if (!result?.next) return;
    const turn = generation.current;
    setLoading("more");
    searchLogs(target, { ...query, cursor: result.next, limit: LOG_PAGE }).then(
      (r) => { if (turn === generation.current) { setResult({ ...result, entries: [...result.entries, ...r.entries], next: r.next }); setLoading(null); } },
      (e: unknown) => { if (turn === generation.current) { fail(e); setLoading(null); } },
    );
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [result, targetKey, queryKey]);

  const submit = (e: FormEvent) => {
    e.preventDefault();
    setWords({ q: draft.q.trim(), client: draft.client.trim() });
    setAt(Date.now());
  };
  const toggle = <T,>(list: T[], value: T) => (list.includes(value) ? list.filter((v) => v !== value) : [...list, value]);
  const toggleSource = (s: LogSource) => {
    const next = toggle(sources, s);
    // A search has at least one source.
    setSources(next.length ? next : SOURCES.map((x) => x.id));
  };
  const searchFor = (key: "q" | "client", value: string) => {
    const next = { ...draft, [key]: value };
    setDraft(next);
    setWords(next);
  };
  const clear = () => {
    setDraft({ q: "", client: "" });
    setWords({ q: "", client: "" });
    setClasses([]);
    setMethod("");
    setSources(SOURCES.map((x) => x.id));
    setRange("all");
  };

  const entries = result?.entries ?? [];
  const summary = result?.summary ?? null;
  const filtered = words.q !== "" || words.client !== "" || classes.length > 0 || method !== "" || range !== "all" || (website && sources.length < SOURCES.length);
  const accessOnly = website && (classes.length > 0 || method !== "" || words.client !== "") && sources.some((s) => s !== "access");
  const download = entries.map(textLine).join("\n");

  return (
    <div className="log-search">
      <form className="log-toolbar" role="search" onSubmit={submit}>
        <label className="visually-hidden" htmlFor="log-q">Search the logs</label>
        <input id="log-q" className="input log-q" placeholder={'Search, for example wp-login -"bot"'} value={draft.q} onChange={(e) => setDraft({ ...draft, q: e.target.value })} maxLength={200} />
        <label className="visually-hidden" htmlFor="log-client">Client</label>
        <input id="log-client" className="input log-client" placeholder="Client IP or network" value={draft.client} onChange={(e) => setDraft({ ...draft, client: e.target.value })} maxLength={64} spellCheck={false} />
        <label className="visually-hidden" htmlFor="log-range">Time</label>
        <select id="log-range" className="select log-range" value={range} onChange={(e) => { setRange(e.target.value as typeof range); setAt(Date.now()); }}>
          {RANGES.map((r) => <option key={r.id} value={r.id}>{r.label}</option>)}
        </select>
        <button type="submit" className="btn btn-primary">Search</button>
      </form>

      <div className="log-filters">
        {website && (
          <div className="filters" role="group" aria-label="Sources">
            {SOURCES.map((s) => (
              <button key={s.id} type="button" className="chip chip-sm" aria-pressed={sources.includes(s.id)} onClick={() => toggleSource(s.id)}>{s.label}</button>
            ))}
          </div>
        )}
        <div className="filters" role="group" aria-label="Status codes">
          {CLASSES.map((c) => (
            <button key={c} type="button" className={`chip chip-sm chip-${c}`} aria-pressed={classes.includes(c)} onClick={() => setClasses(toggle(classes, c))}>{c}</button>
          ))}
        </div>
        <label className="visually-hidden" htmlFor="log-method">Method</label>
        <select id="log-method" className="select select-sm" value={method} onChange={(e) => setMethod(e.target.value)}>
          <option value="">Any method</option>
          {METHODS.map((m) => <option key={m} value={m}>{m}</option>)}
        </select>
        <span className="log-actions">
          {filtered && <button type="button" className="btn btn-sm btn-link" onClick={clear}>Clear filters</button>}
          <button type="button" className="btn btn-sm" disabled={loading !== null} onClick={() => setAt(Date.now())}>{loading === "first" ? "Loading…" : "Refresh"}</button>
          <a className="btn btn-sm" download={`${name}.log`} href={`data:text/plain;charset=utf-8,${encodeURIComponent(download)}`}>Download</a>
        </span>
      </div>

      <FormError error={error} />
      {summary && <LogSummaryView summary={summary} website={website} onPath={(p) => searchFor("q", `"${p}"`)} onClient={(c) => searchFor("client", c)} />}
      {accessOnly && <p className="muted small">The status, method and client filters apply only to requests. The output and the errors of the website are not shown.</p>}

      <div className="log-view" tabIndex={0} role="log" aria-label="Log lines" aria-busy={loading !== null}>
        {entries.map((e, i) => <LogRow key={`${e.at}-${i}`} entry={e} />)}
        {entries.length === 0 && <div className="log-empty">{loading === "first" ? "Loading…" : filtered ? "No entries agree with the search." : "There are no log entries yet."}</div>}
      </div>
      {result?.next && (
        <div className="log-more">
          <button type="button" className="btn btn-sm" disabled={loading !== null} onClick={more}>{loading === "more" ? "Loading…" : "Load older entries"}</button>
          <span className="muted small">{count(entries.length)} entries shown</span>
        </div>
      )}
    </div>
  );
}

function LogSummaryView({ summary, website, onPath, onClient }: { summary: NonNullable<LogSearchResult["summary"]>; website: boolean; onPath: (path: string) => void; onClient: (client: string) => void }) {
  const requests = summary.sources.access;
  const s = summary.statuses;
  const classTotal = CLASSES.reduce((n, c) => n + s[c], 0);
  const label = CLASSES.map((c) => `${c} ${count(s[c])}`).join(", ");
  return (
    <section className="log-summary" aria-label="Summary of the search">
      <dl className="log-stats">
        <div><dt>Requests</dt><dd>{count(requests)}</dd></div>
        <div><dt>Client errors</dt><dd>{requests ? percent((s["4xx"] / requests) * 100) : "–"}<small>4xx</small></dd></div>
        <div><dt>Server errors</dt><dd>{requests ? percent((s["5xx"] / requests) * 100) : "–"}<small>5xx</small></dd></div>
        <div><dt>Data sent</dt><dd>{bytes(summary.sizeBytes)}</dd></div>
        {website && <div><dt>Error lines</dt><dd>{count(summary.sources.error)}</dd></div>}
      </dl>
      {classTotal > 0 && (
        <div className="status-bar" role="img" aria-label={`Status codes: ${label}`}>
          {CLASSES.map((c) => s[c] > 0 && <span key={c} className={`s-${c}`} style={{ flexGrow: s[c] }} title={`${c}: ${count(s[c])}`} />)}
        </div>
      )}
      {(summary.topPaths.length > 0 || summary.topClients.length > 0) && (
        <div className="log-top">
          <TopList title="Top paths" items={summary.topPaths} onSelect={onPath} action="Search for" />
          <TopList title="Top clients" items={summary.topClients} onSelect={onClient} action="Show the requests of" />
        </div>
      )}
      {summary.oldest && <p className="muted small">The oldest entry is from {logTime(summary.oldest)}.</p>}
    </section>
  );
}

function TopList({ title, items, onSelect, action }: { title: string; items: { value: string; count: number }[]; onSelect: (value: string) => void; action: string }) {
  if (items.length === 0) return null;
  const most = items[0].count || 1;
  return (
    <div>
      <h3 className="log-top-title">{title}</h3>
      <ol>
        {items.map((i) => (
          <li key={i.value}>
            <button type="button" className="log-top-item" aria-label={`${action} ${i.value}`} onClick={() => onSelect(i.value)}>
              <span className="log-top-bar" style={{ width: `${(i.count / most) * 100}%` }} />
              <code>{i.value}</code>
              <span>{count(i.count)}</span>
            </button>
          </li>
        ))}
      </ol>
    </div>
  );
}

function LogRow({ entry }: { entry: LogEntry }) {
  const a = entry.access;
  const time = <time dateTime={entry.at} className="dim">{logTime(entry.at)}</time>;
  if (!a) {
    return (
      <div className={`log-row ${entry.source === "error" ? "err" : ""}`}>
        {time} <span className="log-src">{entry.source === "error" ? "err" : "out"}</span> <span className="log-text">{entry.text}</span>
      </div>
    );
  }
  return (
    <details className="log-row">
      <summary>
        {time} <span className={`st st-${Math.floor(a.status / 100)}`}>{a.status || "–"}</span> <span className="m">{a.method}</span>{" "}
        <span className="log-text"><span className="dim">{a.host}</span>{a.uri}</span>{" "}
        <span className="dim">{a.client} · {bytes(a.sizeBytes)} · {duration(a.durationMs)}</span>
      </summary>
      <dl>
        <dt>Time</dt><dd>{entry.at}</dd>
        <dt>Client</dt><dd>{a.client}</dd>
        <dt>Request</dt><dd>{a.method} {a.host}{a.uri} {a.proto}</dd>
        <dt>Answer</dt><dd>{a.status}, {bytes(a.sizeBytes)} in {duration(a.durationMs)}</dd>
        {a.userAgent && <><dt>User agent</dt><dd>{a.userAgent}</dd></>}
        {a.referer && <><dt>Referrer</dt><dd>{a.referer}</dd></>}
      </dl>
    </details>
  );
}
