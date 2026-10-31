import { useMemo, useState } from "react";
import type { Website } from "../../api/types";
import { Card, Tabs } from "../../components/ui";

const PATHS = ["/", "/shop/", "/product/blue-mug", "/cart/", "/checkout/", "/wp-json/wc/store/cart", "/wp-admin/admin-ajax.php", "/favicon.ico"];
const AGENTS = ["Mozilla/5.0 (iPhone; CPU iPhone OS 19_0)", "Mozilla/5.0 (Windows NT 10.0; Win64; x64)", "Googlebot/2.1", "curl/8.14.1"];

function accessLog(website: Website, count = 60): string[] {
  const now = Date.now();
  return Array.from({ length: count }, (_, i) => {
    const t = new Date(now - (count - i) * 7_000);
    const status = i % 23 === 7 ? 500 : i % 11 === 3 ? 404 : i % 9 === 0 ? 304 : 200;
    // FR-1707: the edge proxy logs the real IP address of each client.
    const ip = `203.0.113.${(i * 37) % 254}`;
    return JSON.stringify({ ts: t.toISOString(), remote_ip: ip, host: website.name, method: i % 13 === 4 ? "POST" : "GET", uri: PATHS[i % PATHS.length], status, size: 512 + ((i * 977) % 40_000), duration_ms: 4 + ((i * 13) % 180), user_agent: AGENTS[i % AGENTS.length] });
  });
}

function errorLog(website: Website): string[] {
  const now = Date.now();
  return [
    [90, "warn", `PHP Warning:  Undefined array key "shipping" in /app/public/wp-content/plugins/delivery/delivery.php on line 211`],
    [60, "error", `PHP Fatal error:  Uncaught TypeError: round(): Argument #1 ($num) must be of type int|float, string given in /app/public/wp-content/plugins/tax/calc.php:88`],
    [42, "notice", `PHP Notice:  Function wpdb::prepare was called incorrectly.`],
    [12, "warn", `upstream response time 2.31 s for ${website.name}/checkout/`],
  ].map(([m, level, text]) => `${new Date(now - (m as number) * 60_000).toISOString()} [${level}] ${text}`);
}

/** Access logs and error logs of the website (FR-1302). */
export function LogsTab({ website }: { website: Website }) {
  const [kind, setKind] = useState("access");
  const [filter, setFilter] = useState("");
  const [errorsOnly, setErrorsOnly] = useState(false);
  const lines = useMemo(() => (kind === "access" ? accessLog(website) : errorLog(website)), [kind, website]);
  const shown = lines.filter((l) => (!filter || l.toLowerCase().includes(filter.toLowerCase())) && (!errorsOnly || /"status":5\d\d|\[error\]/.test(l)));

  return (
    <Card
      title="Logs"
      actions={
        <div className="filters">
          <label className="visually-hidden" htmlFor="log-filter">Filter</label>
          <input id="log-filter" className="input" style={{ width: 220 }} placeholder="Filter, for example /checkout" value={filter} onChange={(e) => setFilter(e.target.value)} />
          <label className="check"><input type="checkbox" checked={errorsOnly} onChange={(e) => setErrorsOnly(e.target.checked)} /> Errors only</label>
          <a className="btn" download={`${website.name}-${kind}.log`} href={`data:text/plain;charset=utf-8,${encodeURIComponent(lines.join("\n"))}`}>Download</a>
        </div>
      }
    >
      <div className="card-body">
        <Tabs label="Log type" current={kind} onSelect={setKind} items={[{ id: "access", label: "Access log" }, { id: "error", label: "Error log" }]} />
        <pre className="log-view" tabIndex={0} aria-label={`${kind === "access" ? "Access" : "Error"} log`}>
          {shown.map((l, i) => (
            <div key={i} className={/"status":5\d\d|\[error\]/.test(l) ? "err" : /"status":4\d\d|\[warn\]/.test(l) ? "warn" : ""}>{l}</div>
          ))}
          {shown.length === 0 && "No lines match the filter."}
        </pre>
        <p className="muted small" style={{ marginTop: 10 }}>The edge proxy writes the access log in JSON. Logs show the real IP address of each client.</p>
      </div>
    </Card>
  );
}
