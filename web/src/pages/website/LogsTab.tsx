import { useState } from "react";
import { useLoad } from "../../api/hooks";
import { readLogs } from "../../api/store";
import type { Website } from "../../api/types";
import { Card, FormError, Tabs } from "../../components/ui";

type Stream = "all" | "access" | "error";

const clock = (iso: string) => iso.replace("T", " ").replace(/(\.\d+)?(Z|[+-]\d\d:\d\d)$/, "");

/** The last lines that the container of the website wrote: standard output and standard error. */
export function LogsTab({ website }: { website: Website }) {
  const [stream, setStream] = useState<Stream>("all");
  const [filter, setFilter] = useState("");
  const logs = useLoad(() => readLogs(website.id, stream), [website.id, stream]);
  const lines = logs.data ?? [];
  const needle = filter.trim().toLowerCase();
  const shown = needle ? lines.filter((l) => l.text.toLowerCase().includes(needle)) : lines;
  const text = shown.map((l) => `${l.at} ${l.stream} ${l.text}`).join("\n");

  return (
    <Card
      title="Logs"
      actions={
        <div className="filters">
          <label className="visually-hidden" htmlFor="log-filter">Filter</label>
          <input id="log-filter" className="input" style={{ width: 220 }} placeholder="Filter, for example /checkout" value={filter} onChange={(e) => setFilter(e.target.value)} />
          <button type="button" className="btn" disabled={logs.loading} onClick={logs.reload}>{logs.loading ? "Loading…" : "Refresh"}</button>
          <a className="btn" download={`${website.name}-${stream}.log`} href={`data:text/plain;charset=utf-8,${encodeURIComponent(text)}`}>Download</a>
        </div>
      }
    >
      <div className="card-body">
        <Tabs label="Log type" current={stream} onSelect={(s) => setStream(s as Stream)} items={[{ id: "all", label: "All" }, { id: "access", label: "Output" }, { id: "error", label: "Errors" }]} />
        <FormError error={logs.error} />
        <pre className="log-view" tabIndex={0} aria-label="Log lines" aria-busy={logs.loading}>
          {shown.map((l, i) => (
            <div key={i} className={l.stream === "error" ? "err" : ""}><span style={{ opacity: 0.6 }}>{clock(l.at)}</span> {l.text}</div>
          ))}
          {shown.length === 0 && (logs.loading ? "Loading…" : lines.length ? "No lines match the filter." : "The website wrote no log lines.")}
        </pre>
        <p className="muted small" style={{ marginTop: 10 }}>
          These are the last {lines.length || 200} lines that the website wrote. Output is the standard output of the web server and the app, which has the access log. Errors is the standard error. Times are in UTC. The server keeps up to 10 MB of log lines for each website.
        </p>
      </div>
    </Card>
  );
}
