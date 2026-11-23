import type { Website } from "../../api/types";
import { Card } from "../../components/ui";
import { LogSearch } from "../../features/LogSearch";

/**
 * The logs of a website: the requests to its domains (the access log of the edge proxy), and
 * the output and the errors of its containers, in one search.
 */
export function LogsTab({ website }: { website: Website }) {
  return (
    <Card title="Logs">
      <div className="card-body">
        <LogSearch target={{ websiteId: website.id }} name={website.name} />
        <p className="muted small" style={{ marginTop: 12 }}>
          Requests come from the edge proxy, which answers for all the domains of the website. The server keeps them for 30 days. Output and errors are the last 10 MB that the containers of the website wrote. The edge proxy does not keep cookies or the Authorization header. Times are in your time zone. Click a request to see its details.
        </p>
      </div>
    </Card>
  );
}
