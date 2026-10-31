import { Link } from "react-router-dom";
import { useStore } from "../api/hooks";
import { Topbar } from "../components/Layout";
import { Card, JobState, Live, Meter, PageHeader } from "../components/ui";
import { longDate, relativeTime, shortAgo } from "../lib/format";

/** All jobs with live progress, and the activity feed (spec 6.3, UI rule: live job progress). */
export function JobsPage() {
  const state = useStore();
  const name = (id?: string) => state.websites.find((w) => w.id === id)?.name ?? state.servers.find((s) => s.id === id)?.name;
  return (
    <>
      <Topbar />
      <main id="main" className="content content-wide">
        <PageHeader title="Jobs and activity" description="Each change is a job. The agent on the correct server runs each step and reports the result." />
        <div className="detail-grid">
          <Card title="Jobs" actions={<Live />}>
            <div className="table-wrap">
              <table>
                <thead><tr><th scope="col">Job</th><th scope="col">For</th><th scope="col">Progress</th><th scope="col">Started</th></tr></thead>
                <tbody>
                  {state.jobs.map((j) => (
                    <tr key={j.id}>
                      <td>{j.title}{j.ref && <> <code>{j.ref}</code></>}{j.error && <span className="cell-sub text-danger">{j.error}</span>}</td>
                      <td>{j.websiteId ? <Link to={`/websites/${j.websiteId}`}>{name(j.websiteId)}</Link> : j.serverId ? <Link to={`/servers/${j.serverId}`}>{name(j.serverId)}</Link> : "Panel"}</td>
                      <td style={{ minWidth: 180 }}>
                        {j.state === "running" ? (
                          <>
                            <Meter value={j.step / j.steps.length} label={`${j.title}: step ${j.step + 1} of ${j.steps.length}`} />
                            <span className="cell-sub">{j.steps[Math.min(j.step, j.steps.length - 1)]} · step {j.step + 1} of {j.steps.length}</span>
                          </>
                        ) : <JobState job={j} />}
                      </td>
                      <td className="nowrap">{shortAgo(j.createdAt)}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
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
