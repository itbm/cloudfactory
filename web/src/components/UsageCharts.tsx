import { AlertTriangle, OctagonAlert } from "lucide-react";
import { useEffect, useRef, useState } from "react";
import { Link } from "react-router-dom";
import { useLoad, useStore } from "../api/hooks";
import { readUsage } from "../api/store";
import type { UsageAlert, UsageHistory, UsagePoint, UsageRange, UsageSubject } from "../api/types";
import { bytes, relativeTime } from "../lib/format";
import { Card, FormError, Meter, Tabs } from "./ui";

export const RANGES: { id: UsageRange; label: string }[] = [
  { id: "24h", label: "24 hours" },
  { id: "7d", label: "7 days" },
  { id: "30d", label: "30 days" },
  { id: "90d", label: "90 days" },
  { id: "1y", label: "1 year" },
];

export const RESOURCE_LABEL: Record<UsageAlert["resource"], string> = {
  cpu: "CPU",
  memory: "Memory",
  disk: "Disk",
  bandwidth: "Bandwidth this month",
  processes: "Processes",
};

const pct = (v: number) => `${v >= 10 || Number.isInteger(v) ? Math.round(v) : v.toFixed(1)}%`;
const vcpus = (v: number) => (v === 0 ? "0 vCPU" : `${(v / 100).toFixed(v >= 1000 ? 0 : v >= 100 ? 1 : 2)} vCPU`);

/** The time of a point, short enough for an axis. */
function axisTime(iso: string, range: UsageRange): string {
  const d = new Date(iso);
  if (range === "24h") return d.toLocaleTimeString("en-GB", { hour: "2-digit", minute: "2-digit", timeZone: "UTC" });
  if (range === "7d") return d.toLocaleDateString("en-GB", { weekday: "short", day: "numeric", timeZone: "UTC" });
  if (range === "1y") return d.toLocaleDateString("en-GB", { month: "short", year: "numeric", timeZone: "UTC" });
  return d.toLocaleDateString("en-GB", { day: "numeric", month: "short", timeZone: "UTC" });
}

/** The time of a point in a tooltip or a table. */
function fullTime(iso: string, stepMinutes: number): string {
  const d = new Date(iso);
  const opts: Intl.DateTimeFormatOptions = { day: "numeric", month: "short", timeZone: "UTC" };
  if (stepMinutes < 1440) Object.assign(opts, { hour: "2-digit", minute: "2-digit" });
  return `${d.toLocaleString("en-GB", opts)} UTC`;
}

interface Series {
  /** The value of each point, or null for a gap. */
  values: (number | null)[];
  /** The largest value in each point, shown in the tooltip. */
  max?: (number | null)[];
  /** For a bar chart: the two parts of each value, shown in the tooltip. */
  parts?: [string, (number | null)[]][];
}

interface ChartProps {
  title: string;
  history: UsageHistory;
  series: Series;
  format: (v: number) => string;
  kind?: "line" | "bars";
  limit?: number;
  limitLabel?: string;
  /** The top of the scale. Without it, the scale fits the values and the limit. */
  top?: number;
  /** The text next to the title, for example the current value. */
  summary?: string;
}

/** A "nice" top of a scale: 1, 2, 2.5 or 5 times a power of 10, in the units of the format. */
function niceTop(v: number, binary = false): number {
  if (v <= 0) return 1;
  // Bytes: a round number of KB, MB or GB.
  const unit = binary ? 1024 ** Math.max(0, Math.floor(Math.log(v) / Math.log(1024))) : 1;
  const u = v / unit;
  const p = 10 ** Math.floor(Math.log10(u));
  for (const m of [1, 1.2, 1.6, 2, 2.4, 3, 4, 5, 6, 8, 10]) if (u <= m * p) return m * p * unit;
  return 10 * p * unit;
}

const HEIGHT = 150;
const PAD = { top: 10, right: 12, bottom: 24, left: 64 };

/**
 * One graph of a usage history: a line of the average, or bars of the traffic. A dashed line
 * shows the limit. Hover or focus shows the values of a point.
 */
export function UsageChart({ title, history, series, format, kind = "line", limit, limitLabel, top, summary }: ChartProps) {
  const box = useRef<HTMLDivElement>(null);
  const [width, setWidth] = useState(560);
  const [hover, setHover] = useState<number | null>(null);
  useEffect(() => {
    const el = box.current;
    if (!el || typeof ResizeObserver === "undefined") return;
    const ro = new ResizeObserver(([entry]) => setWidth(Math.max(240, Math.round(entry.contentRect.width))));
    ro.observe(el);
    return () => ro.disconnect();
  }, []);

  const points = history.points;
  const n = points.length;
  const values = series.values;
  const known = values.filter((v): v is number => v !== null);
  const scaleTop = top ?? niceTop(Math.max(...known, limit && limit <= Math.max(...known, 0) * 3 ? limit : 0, 0) * 1.1, format === bytes);
  const plotW = width - PAD.left - PAD.right;
  const plotH = HEIGHT - PAD.top - PAD.bottom;
  const x = (i: number) => PAD.left + (n <= 1 ? plotW / 2 : (i / (n - 1)) * plotW);
  const y = (v: number) => PAD.top + plotH - (Math.min(v, scaleTop) / scaleTop) * plotH;
  const slot = plotW / Math.max(1, n);

  // The line has a gap where a point has no report.
  const segments: string[] = [];
  let current = "";
  values.forEach((v, i) => {
    if (v === null) {
      if (current) segments.push(current);
      current = "";
      return;
    }
    current += `${current ? "L" : "M"}${x(i).toFixed(1)},${y(v).toFixed(1)}`;
  });
  if (current) segments.push(current);
  const areas = segments.map((d) => {
    const xs = [...d.matchAll(/[ML]([\d.]+),/g)].map((m) => m[1]);
    return `${d}L${xs[xs.length - 1]},${PAD.top + plotH}L${xs[0]},${PAD.top + plotH}Z`;
  });

  const ticks = [0, scaleTop / 2, scaleTop];
  const labels = n > 1 ? [0, Math.floor((n - 1) / 2), n - 1] : [0];
  const pointAt = (clientX: number) => {
    const rect = box.current?.querySelector("svg")?.getBoundingClientRect();
    if (!rect || n === 0) return null;
    const px = ((clientX - rect.left) / rect.width) * width;
    return Math.max(0, Math.min(n - 1, Math.round(((px - PAD.left) / plotW) * (n - 1))));
  };
  const tip = hover !== null ? points[hover] : null;
  const hv = hover !== null ? values[hover] : null;
  const showLimit = limit !== undefined && limit > 0 && limit <= scaleTop;
  const last = [...known].pop();

  return (
    <div className="usage-chart">
      <div className="usage-chart-head">
        <h3>{title}</h3>
        {summary !== undefined ? <span className="muted">{summary}</span> : last !== undefined && <span className="muted">{limit ? `${format(last)} of ${format(limit)}` : format(last)}</span>}
      </div>
      <div
        className="usage-plot"
        ref={box}
        tabIndex={0}
        role="group"
        aria-label={`${title}. ${known.length ? `Last value ${format(last!)}, largest ${format(Math.max(...known))}.` : "No values in this range."} Use the left and right arrow keys to read each point.`}
        onPointerMove={(e) => setHover(pointAt(e.clientX))}
        onPointerLeave={() => setHover(null)}
        onBlur={() => setHover(null)}
        onKeyDown={(e) => {
          if (e.key !== "ArrowLeft" && e.key !== "ArrowRight") return;
          e.preventDefault();
          setHover((h) => Math.max(0, Math.min(n - 1, (h ?? n - 1) + (e.key === "ArrowLeft" ? -1 : 1))));
        }}
      >
        <svg width="100%" height={HEIGHT} viewBox={`0 0 ${width} ${HEIGHT}`} aria-hidden="true">
          {ticks.map((t) => (
            <g key={t}>
              <line className="usage-grid" x1={PAD.left} x2={width - PAD.right} y1={y(t)} y2={y(t)} />
              <text className="usage-axis" x={PAD.left - 8} y={y(t) + 4} textAnchor="end">{format(t)}</text>
            </g>
          ))}
          {labels.map((i, k) => (
            <text key={i} className="usage-axis" x={x(i)} y={HEIGHT - 6} textAnchor={k === 0 ? "start" : k === labels.length - 1 ? "end" : "middle"}>
              {axisTime(points[i].at, history.range)}
            </text>
          ))}
          {kind === "line" ? (
            <>
              {areas.map((d, i) => <path key={`a${i}`} className="usage-area" d={d} />)}
              {segments.map((d, i) => <path key={`l${i}`} className="usage-line" d={d} />)}
            </>
          ) : (
            values.map((v, i) => {
              if (v === null || v <= 0) return null;
              const w = Math.max(1, slot - (slot > 6 ? 2 : 0.5));
              const h = Math.max(1, PAD.top + plotH - y(v));
              return <rect key={i} className="usage-bar" x={x(i) - w / 2} y={PAD.top + plotH - h} width={w} height={h} rx={Math.min(2, w / 2)} />;
            })
          )}
          {showLimit && (
            <g>
              <line className="usage-limit" x1={PAD.left} x2={width - PAD.right} y1={y(limit!)} y2={y(limit!)} />
              <text className="usage-limit-label" x={width - PAD.right} y={y(limit!) - 5} textAnchor="end">{limitLabel ?? `Limit ${format(limit!)}`}</text>
            </g>
          )}
          {hover !== null && (
            <g>
              <line className="usage-cross" x1={x(hover)} x2={x(hover)} y1={PAD.top} y2={PAD.top + plotH} />
              {kind === "line" && hv !== null && <circle className="usage-dot" cx={x(hover)} cy={y(hv)} r={4} />}
            </g>
          )}
        </svg>
        {tip && (
          <div className="usage-tip" role="status" style={{ left: `${(x(hover!) / width) * 100}%`, transform: `translateX(${hover! > n / 2 ? "-100%" : "0"})` }}>
            <div className="usage-tip-time">{fullTime(tip.at, history.stepMinutes)}</div>
            {hv === null ? <div>No report</div> : (
              <>
                <div><strong>{format(hv)}</strong>{kind === "line" ? " average" : ""}</div>
                {series.max && series.max[hover!] !== null && <div className="muted">Largest {format(series.max[hover!]!)}</div>}
                {series.parts?.map(([label, part]) => part[hover!] !== null && <div key={label} className="muted">{label} {format(part[hover!]!)}</div>)}
              </>
            )}
          </div>
        )}
      </div>
    </div>
  );
}

const value = (points: UsagePoint[], key: keyof UsagePoint) => points.map((p) => (p[key] as number | null | undefined) ?? null);
const sum = (a: (number | null)[], b: (number | null)[]) => a.map((v, i) => (v === null && b[i] === null ? null : (v ?? 0) + (b[i] ?? 0)));

/** The list of the open alerts of one website, server or database server. */
export function AlertNotices({ alerts }: { alerts: UsageAlert[] }) {
  if (!alerts.length) return null;
  return (
    <div className="stack-tight">
      {alerts.map((a) => (
        <div key={a.id} className={`alert ${a.level === "critical" ? "alert-danger" : "alert-warning"} usage-alert`} role="status">
          {a.level === "critical" ? <OctagonAlert size={18} aria-hidden="true" /> : <AlertTriangle size={18} aria-hidden="true" />}
          <p>
            <strong>{a.level === "critical" ? "Critical" : "Warning"}: {RESOURCE_LABEL[a.resource].toLowerCase()} at {Math.round(a.percent)}% of the limit.</strong>{" "}
            {a.usedText} of {a.limitText}, since {relativeTime(a.openedAt)}. {ALERT_ADVICE[a.resource]}
          </p>
        </div>
      ))}
    </div>
  );
}

/** What the user can do about each kind of alert. */
export const ALERT_ADVICE: Record<UsageAlert["resource"], string> = {
  cpu: "Requests can become slow. Look for slow pages or a busy background task, or move to a plan with more vCPUs.",
  memory: "At the limit, the website stops a process to free memory. Lower the memory of the app, or move to a plan with more memory.",
  disk: "At the limit, the website cannot write files and uploads fail. Remove old files and backups, or move to a plan with more disk space.",
  bandwidth: "The panel does not stop the website at the limit. Your provider can ask you to move to a larger plan.",
  processes: "At the limit, the website cannot start new processes. Look for processes that do not stop.",
};

/**
 * The usage graphs of a website, a server or a database server, with a choice of the range
 * and a table of the values.
 */
export function UsagePanel({ subject, id }: { subject: UsageSubject; id: string }) {
  const [range, setRange] = useState<UsageRange>("24h");
  const usage = useLoad(() => readUsage(subject, id, range), [subject, id, range]);
  const h = usage.data;
  const owner = useStore().session.user.role === "owner";

  return (
    <Card title="Usage" actions={<Tabs label="Time range" current={range} onSelect={(r) => setRange(r as UsageRange)} items={RANGES} />}>
      <div className="card-body">
        <FormError error={usage.error} />
        {!h && !usage.error && <p className="muted" aria-busy="true">Loading the graphs…</p>}
        {h && (
          <div aria-busy={usage.loading}>
            <AlertNotices alerts={h.alerts} />
            {subject === "website" && h.bandwidth && <BandwidthMeter used={h.bandwidth.bytes} limit={h.bandwidth.limitBytes} month={h.bandwidth.month} owner={owner} />}
            <div className="usage-grid-charts">
              {subject === "server" ? (
                <>
                  <UsageChart title="CPU" history={h} series={{ values: value(h.points, "cpuPercent"), max: value(h.points, "cpuMaxPercent") }} format={pct} top={100} />
                  <UsageChart title="Memory" history={h} series={{ values: value(h.points, "memoryPercent"), max: value(h.points, "memoryMaxPercent") }} format={pct} top={100} />
                  <UsageChart title="Disk" history={h} series={{ values: value(h.points, "diskPercent") }} format={pct} top={100} />
                  <TrafficChart h={h} title="Network traffic" />
                </>
              ) : (
                <>
                  {subject === "website" && (
                    <UsageChart title="CPU" history={h} series={{ values: value(h.points, "cpuPercent"), max: value(h.points, "cpuMaxPercent") }} format={vcpus}
                      limit={(h.limits?.vcpus ?? 0) * 100} />
                  )}
                  <UsageChart title="Memory" history={h} series={{ values: value(h.points, "memoryBytes"), max: value(h.points, "memoryMaxBytes") }} format={bytes} limit={h.limits?.memoryBytes} />
                  <UsageChart title="Disk" history={h} series={{ values: value(h.points, "diskBytes") }} format={bytes} limit={h.limits?.diskBytes} />
                  {subject === "website" && <TrafficChart h={h} title="Traffic" />}
                </>
              )}
            </div>
            <UsageTable h={h} subject={subject} />
            <p className="muted small" style={{ marginTop: 12 }}>
              {subject === "server"
                ? "CPU is the load of the server. Network traffic is the traffic of the network interfaces of the server, also the traffic of the backups and of the private networks. Times are in UTC."
                : subject === "website"
                  ? "CPU is in vCPUs: 1 vCPU is one full processor core. The graphs show the average of each point. Traffic is the size of the requests and the answers of the domains of the website. Times are in UTC."
                  : "Memory and disk space of the database server. Times are in UTC."}
            </p>
          </div>
        )}
      </div>
    </Card>
  );
}

function TrafficChart({ h, title }: { h: UsageHistory; title: string }) {
  const inn = value(h.points, "bytesIn");
  const out = value(h.points, "bytesOut");
  const total = sum(inn, out);
  const all = total.reduce<number>((a, v) => a + (v ?? 0), 0);
  return (
    <UsageChart title={title} history={h} kind="bars" series={{ values: total, parts: [["In", inn], ["Out", out]] }} format={bytes}
      summary={`${bytes(all)} in ${RANGES.find((r) => r.id === h.range)!.label}`} />
  );
}

function BandwidthMeter({ used, limit, month, owner }: { used: number; limit: number; month: string; owner: boolean }) {
  const name = new Date(`${month}-01T00:00:00Z`).toLocaleDateString("en-GB", { month: "long", year: "numeric", timeZone: "UTC" });
  const share = limit > 0 ? used / limit : 0;
  return (
    <div className="resource usage-bandwidth">
      <div className="row"><span>Bandwidth in {name}</span><span>{limit > 0 ? `${bytes(used)} of ${bytes(limit)}` : `${bytes(used)} · no limit`}</span></div>
      {limit > 0 && <Meter value={share} label={`Bandwidth in ${name}: ${bytes(used)} of ${bytes(limit)}`} tone={share >= 0.95 ? "danger" : share >= 0.8 ? "warning" : undefined} />}
      {limit > 0 && share >= 0.8 && <p className="muted small" style={{ marginTop: 6 }}>The count starts again on the first day of each month. {owner ? "To get more, ask your provider for a larger plan." : "To give more, change the plan on the Settings tab."}</p>}
    </div>
  );
}

/** The values of the graphs as a table, for screen readers and for a copy. */
function UsageTable({ h, subject }: { h: UsageHistory; subject: UsageSubject }) {
  const rows = h.points.filter((p) => p.cpuPercent !== null || p.memoryBytes != null || p.memoryPercent != null).slice().reverse();
  const server = subject === "server";
  return (
    <details className="usage-table">
      <summary>Show the values as a table</summary>
      <div className="table-wrap" style={{ maxHeight: 320, overflow: "auto", marginTop: 8 }}>
        <table className="table">
          <thead>
            <tr>
              <th scope="col">Time (UTC)</th>
              {subject !== "database_server" && <th scope="col">CPU</th>}
              <th scope="col">Memory</th>
              <th scope="col">Disk</th>
              {subject !== "database_server" && <th scope="col">Traffic in</th>}
              {subject !== "database_server" && <th scope="col">Traffic out</th>}
            </tr>
          </thead>
          <tbody>
            {rows.map((p) => (
              <tr key={p.at}>
                <td>{fullTime(p.at, h.stepMinutes).replace(" UTC", "")}</td>
                {subject !== "database_server" && <td>{p.cpuPercent === null ? "" : server ? pct(p.cpuPercent) : vcpus(p.cpuPercent)}</td>}
                <td>{server ? pct(p.memoryPercent ?? 0) : bytes(p.memoryBytes ?? 0)}</td>
                <td>{server ? pct(p.diskPercent ?? 0) : bytes(p.diskBytes ?? 0)}</td>
                {subject !== "database_server" && <td>{bytes(p.bytesIn ?? 0)}</td>}
                {subject !== "database_server" && <td>{bytes(p.bytesOut ?? 0)}</td>}
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </details>
  );
}

/** A short notice of the open alerts of a website, for the overview of the website. */
export function WebsiteAlertSummary({ websiteId }: { websiteId: string }) {
  const alerts = useStore().usageAlerts.filter((a) => a.subjectType === "website" && a.subjectId === websiteId);
  if (!alerts.length) return null;
  const critical = alerts.some((a) => a.level === "critical");
  return (
    <div className={`alert ${critical ? "alert-danger" : "alert-warning"} usage-alert`} role="status" style={{ marginBottom: 20 }}>
      {critical ? <OctagonAlert size={18} aria-hidden="true" /> : <AlertTriangle size={18} aria-hidden="true" />}
      <p>
        <strong>{alerts.map((a) => `${RESOURCE_LABEL[a.resource]} at ${Math.round(a.percent)}%`).join(", ")} of the limit.</strong>{" "}
        <Link to={`/websites/${websiteId}/usage`}>See the usage graphs</Link>
      </p>
    </div>
  );
}
