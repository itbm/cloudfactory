import { useEffect, useState } from "react";
import { Link } from "react-router-dom";
import { useLoad } from "../api/hooks";
import { duration, listAllUptimeChecks, listUptimeIncidents, type UptimeState } from "../api/uptime";
import { Topbar } from "../components/Layout";
import { Card, Empty, FormError, PageHeader, Status, TableWrap, Tabs } from "../components/ui";
import { clockTime, longDate } from "../lib/format";
import { LastResult, UPTIME_STATE, UptimeCell, UptimeStateText } from "./website/UptimeTab";

/** The page loads the checks again after this time, so the state stays current. */
const RELOAD_MS = 30_000;

type Filter = "all" | UptimeState;

/** The uptime checks of all websites that the user can see, and their incidents. */
export function UptimePage() {
  const checks = useLoad(listAllUptimeChecks, []);
  const incidents = useLoad(listUptimeIncidents, []);
  const [filter, setFilter] = useState<Filter>("all");
  const reloadChecks = checks.reload;
  const reloadIncidents = incidents.reload;
  useEffect(() => {
    const timer = window.setInterval(() => { reloadChecks(); reloadIncidents(); }, RELOAD_MS);
    return () => window.clearInterval(timer);
  }, [reloadChecks, reloadIncidents]);

  const all = checks.data?.checks ?? [];
  const summary = checks.data?.summary ?? { total: 0, up: 0, down: 0, pending: 0, paused: 0 };
  const shown = filter === "all" ? all : all.filter((c) => c.state === filter);
  const rows = incidents.data ?? [];
  const tiles: [UptimeState, string][] = [["down", "Down"], ["up", "Up"], ["pending", "No result yet"], ["paused", "Paused"]];

  return (
    <>
      <Topbar />
      <main id="main" className="content content-wide">
        <PageHeader
          title="Uptime checks"
          description="The panel requests an address of each website on a schedule. When the address does not answer or gives an error, the panel sends an email to the users of the website. Add a check on the Uptime tab of a website."
        />
        <FormError error={checks.error ?? incidents.error} />
        {!checks.loading && !checks.error && summary.total === 0 ? (
          <Empty title="No uptime checks">
            <p>Open a website, and add a check on its Uptime tab. <Link to="/websites">Go to the websites</Link>.</p>
          </Empty>
        ) : (
          <>
            <div className="stats">
              {tiles.map(([state, label]) => (
                <section className="card stat" key={state} aria-label={label}>
                  <h2 className="stat-label"><Status tone={UPTIME_STATE[state].tone}>{label}</Status></h2>
                  <p className="stat-value">{summary[state]}<small>of {summary.total} {summary.total === 1 ? "check" : "checks"}</small></p>
                </section>
              ))}
            </div>
            <div className="stack">
              <Card title="Checks">
                <div className="card-body" style={{ paddingBottom: 0 }}>
                  <Tabs
                    label="Filter the checks"
                    current={filter}
                    onSelect={(f) => setFilter(f as Filter)}
                    items={[{ id: "all", label: `All (${summary.total})` }, ...tiles.map(([state, label]) => ({ id: state, label: `${label} (${summary[state]})` }))]}
                  />
                </div>
                <TableWrap>
                  <table aria-busy={checks.loading}>
                    <thead><tr><th scope="col">Website</th><th scope="col">State</th><th scope="col">Last check</th><th scope="col">Uptime</th></tr></thead>
                    <tbody>
                      {shown.map((c) => (
                        <tr key={c.id}>
                          <td>
                            <Link to={`/websites/${c.websiteId}/uptime`}>{c.websiteName}</Link>
                            <span className="cell-sub" style={{ overflowWrap: "anywhere" }}>{c.url}</span>
                          </td>
                          <td><UptimeStateText check={c} /></td>
                          <td><LastResult check={c} /></td>
                          <td className="nowrap"><UptimeCell check={c} /></td>
                        </tr>
                      ))}
                      {shown.length === 0 && <tr><td colSpan={4} className="muted">{checks.loading ? "Loading…" : "No checks in this state."}</td></tr>}
                    </tbody>
                  </table>
                </TableWrap>
              </Card>
              <Card title="Incidents">
                {rows.length === 0 ? (
                  <p className="card-body muted">{incidents.loading ? "Loading…" : "No incidents. The panel keeps the incidents for 90 days."}</p>
                ) : (
                  <TableWrap>
                    <table>
                      <thead><tr><th scope="col">Website</th><th scope="col">Started</th><th scope="col">Length</th><th scope="col">Cause</th></tr></thead>
                      <tbody>
                        {rows.slice(0, 50).map((i) => (
                          <tr key={i.id}>
                            <td>
                              <Link to={`/websites/${i.websiteId}/uptime`}>{i.websiteName}</Link>
                              <span className="cell-sub" style={{ overflowWrap: "anywhere" }}>{i.url}</span>
                            </td>
                            <td className="nowrap">{longDate(i.startedAt)}<span className="cell-sub">{clockTime(i.startedAt)}</span></td>
                            <td className="nowrap">
                              {i.resolvedAt ? duration(i.durationSeconds) : <Status tone="danger">Still down</Status>}
                              <span className="cell-sub">{i.resolvedAt ? (i.endedByPause ? "Ended when the check was paused" : `Up again at ${clockTime(i.resolvedAt)}`) : `For ${duration(i.durationSeconds)}`}</span>
                            </td>
                            <td>{i.error}</td>
                          </tr>
                        ))}
                      </tbody>
                    </table>
                  </TableWrap>
                )}
              </Card>
            </div>
          </>
        )}
      </main>
    </>
  );
}
