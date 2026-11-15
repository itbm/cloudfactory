import { AlertTriangle, OctagonAlert } from "lucide-react";
import { useState } from "react";
import { Link } from "react-router-dom";
import { useAction, useLoad, useStore } from "../api/hooks";
import { listUsageAlerts, setAlertSettings } from "../api/store";
import type { UsageAlert } from "../api/types";
import { Topbar } from "../components/Layout";
import { ALERT_ADVICE, RESOURCE_LABEL } from "../components/UsageCharts";
import { Card, Empty, Field, FormError, PageHeader, TableWrap, Tabs } from "../components/ui";
import { longDate, relativeTime } from "../lib/format";

const SUBJECT_LABEL: Record<UsageAlert["subjectType"], string> = { website: "Website", server: "Server", database_server: "Database server" };

function Level({ alert }: { alert: UsageAlert }) {
  const critical = alert.level === "critical";
  return (
    <span className={`alert-level ${alert.level}`}>
      {critical ? <OctagonAlert size={16} aria-hidden="true" /> : <AlertTriangle size={16} aria-hidden="true" />}
      {critical ? "Critical" : "Warning"}
    </span>
  );
}

function Subject({ alert }: { alert: UsageAlert }) {
  const admin = useStore().session.user.role === "administrator";
  const name = alert.subjectName ?? "Removed";
  if (!alert.subjectName) return <span className="muted">{name}</span>;
  const href = alert.subjectType === "website" ? `/websites/${alert.subjectId}/usage`
    : alert.subjectType === "server" ? (admin ? `/servers/${alert.subjectId}` : null)
      : "/databases";
  return (
    <>
      {href ? <Link to={href}>{name}</Link> : name}
      <span className="cell-sub">{SUBJECT_LABEL[alert.subjectType]}</span>
    </>
  );
}

function AlertTable({ alerts, closed }: { alerts: UsageAlert[]; closed: boolean }) {
  return (
    <TableWrap>
      <table>
        <thead>
          <tr>
            <th scope="col">Level</th>
            <th scope="col">Resource</th>
            <th scope="col">For</th>
            <th scope="col">Use</th>
            <th scope="col">{closed ? "Open from" : "Since"}</th>
          </tr>
        </thead>
        <tbody>
          {alerts.map((a) => (
            <tr key={a.id}>
              <td className="nowrap"><Level alert={a} /></td>
              <td>
                {RESOURCE_LABEL[a.resource]}
                {!closed && a.subjectType !== "server" && <span className="cell-sub">{ALERT_ADVICE[a.resource]}</span>}
              </td>
              <td><Subject alert={a} /></td>
              <td className="nowrap">{a.usedText} of {a.limitText}<span className="cell-sub">{Math.round(a.percent)}% of the limit</span></td>
              <td className="nowrap">
                {closed ? <>{longDate(a.openedAt)}<span className="cell-sub">to {longDate(a.resolvedAt!)}</span></> : relativeTime(a.openedAt)}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </TableWrap>
  );
}

function ThresholdsCard() {
  const settings = useStore().alertSettings;
  const [warning, setWarning] = useState(String(settings.warningPercent));
  const [critical, setCritical] = useState(String(settings.criticalPercent));
  const save = useAction(setAlertSettings);
  return (
    <Card title="Thresholds">
      <form
        className="card-body"
        onSubmit={(e) => {
          e.preventDefault();
          void save.run({ warningPercent: Number(warning), criticalPercent: Number(critical) });
        }}
      >
        <p className="muted" style={{ marginBottom: 14 }}>
          The thresholds are percents of each limit, for all websites, database servers and servers. An alert closes when the use goes 5 points below the warning threshold.
        </p>
        <div className="form-row">
          <Field label="Warning at (%)" error={save.error?.fields.warningPercent}>
            {(p) => <input {...p} className="input" type="number" min={1} max={99} value={warning} onChange={(e) => setWarning(e.target.value)} style={{ width: 110 }} />}
          </Field>
          <Field label="Critical at (%)" error={save.error?.fields.criticalPercent}>
            {(p) => <input {...p} className="input" type="number" min={2} max={100} value={critical} onChange={(e) => setCritical(e.target.value)} style={{ width: 110 }} />}
          </Field>
        </div>
        <FormError error={save.error} shown={["warningPercent", "criticalPercent"]} />
        <div className="btn-group" style={{ marginTop: 14 }}>
          <button type="submit" className="btn btn-primary" disabled={save.busy}>{save.busy ? "Saving…" : "Save thresholds"}</button>
        </div>
      </form>
    </Card>
  );
}

/** The usage alerts: the resources of the websites (and, for an administrator, the servers) that are near their limits. */
export function AlertsPage() {
  const state = useStore();
  const role = state.session.user.role;
  const [tab, setTab] = useState<"open" | "resolved">("open");
  const closed = useLoad(() => (tab === "resolved" ? listUsageAlerts("resolved") : Promise.resolve([])), [tab]);
  const open = state.usageAlerts;
  const { warningPercent, criticalPercent } = state.alertSettings;
  const shown = tab === "open" ? open : closed.data ?? [];

  return (
    <>
      <Topbar />
      <main id="main" className="content content-wide">
        <PageHeader
          title="Usage alerts"
          description={`An alert opens when a website${role === "administrator" ? ", a database server or a server" : " or a database server"} uses ${warningPercent}% of a limit, and becomes critical at ${criticalPercent}%. CPU and memory use the average of the last 30 minutes.`}
        />
        <Tabs label="Alerts" current={tab} onSelect={(t) => setTab(t as "open" | "resolved")} items={[{ id: "open", label: `Open (${open.length})` }, { id: "resolved", label: "Closed" }]} />
        <div className="stack">
          <Card title={tab === "open" ? "Open alerts" : "Closed alerts"}>
            {tab === "resolved" && <div className="card-body"><FormError error={closed.error} /></div>}
            {shown.length ? <AlertTable alerts={shown} closed={tab === "resolved"} /> : (
              <div className="card-body">
                <Empty title={tab === "open" ? "No open alerts" : closed.loading ? "Loading…" : "No closed alerts"}>
                  <p>{tab === "open" ? "Each website is well below its limits." : "The panel keeps closed alerts for 90 days."}</p>
                </Empty>
              </div>
            )}
          </Card>
          {role === "administrator" && <ThresholdsCard />}
          <p className="muted small">
            The panel does not stop a website because of an alert. When the panel can send email, the users who keep the usage email on also get an email when an alert opens or becomes critical.
          </p>
        </div>
      </main>
    </>
  );
}
