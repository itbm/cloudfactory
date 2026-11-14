import { Link } from "react-router-dom";
import { useStore } from "../api/hooks";
import { Topbar } from "../components/Layout";
import { Card, JobError, JobState, Live, Meter, PageHeader, TableWrap } from "../components/ui";
import { resolvedFailures } from "../lib/jobFailure";
import { longDate, relativeTime, shortAgo } from "../lib/format";

/** All jobs with live progress, and the activity feed (spec 6.3, UI rule: live job progress). */
export function JobsPage() {
  const state = useStore();
  const role = state.session.user.role;
  const resolved = resolvedFailures(state.jobs);
  const name = (id?: string) => state.websites.find((w) => w.id === id)?.name ?? state.servers.find((s) => s.id === id)?.name;
  return (
    <>
      <Topbar />
      <main id="main" className="content content-wide">
        <PageHeader
          title={role === "owner" ? "Activity" : "Jobs and activity"}
          description={role === "administrator"
            ? "Each change is a job. The agent on the correct server runs each step and reports the result."
            : "Each change is a job. This page shows the progress and the result of each job. If a job fails, give its reference to your provider."}
        />
        <div className="detail-grid">
          <Card title="Jobs" actions={<Live />}>
            <TableWrap>
              <table>
                <thead><tr><th scope="col">Job</th><th scope="col">For</th><th scope="col">Progress</th><th scope="col">Started</th></tr></thead>
                <tbody>
                  {state.jobs.map((j) => (
                    <tr key={j.id}>
                      <td>{j.title}{j.ref && <> <code>{j.ref}</code></>}<JobError job={j} resolved={resolved.has(j.id)} /></td>
                      <td>{j.websiteId ? <Link to={`/websites/${j.websiteId}`}>{name(j.websiteId)}</Link> : j.serverId && role === "administrator" ? <Link to={`/servers/${j.serverId}`}>{name(j.serverId)}</Link> : "Panel"}</td>
                      <td style={{ minWidth: 180 }}>
                        {j.state === "running" ? (
                          <>
                            <Meter value={j.step / j.steps.length} label={`${j.title}: step ${j.step + 1} of ${j.steps.length}`} />
                            <span className="cell-sub">{j.steps[Math.min(j.step, j.steps.length - 1)]} · step {j.step + 1} of {j.steps.length}</span>
                          </>
                        ) : <JobState job={j} resolved={resolved.has(j.id)} />}
                      </td>
                      <td className="nowrap">{shortAgo(j.createdAt)}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </TableWrap>
          </Card>
          <Card title="Activity">
            <div className="card-body">
              <ul className="list">
                {state.activity.map((a) => (
                  <li key={a.id}>
                    <span>{a.text}{a.ref && <> <code>{a.ref}</code></>}</span>
                    <time className="when" dateTime={a.at} title={longDate(a.at)}>{relativeTime(a.at)}</time>
                  </li>
                ))}
              </ul>
            </div>
          </Card>
        </div>
      </main>
    </>
  );
}
