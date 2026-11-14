import { Topbar } from "../components/Layout";
import { PageHeader } from "../components/ui";
import { DatabaseServersPanel } from "../features/DatabaseServersPanel";
import { DatabasesPanel } from "../features/DatabasesPanel";

export function DatabasesPage() {
  return (
    <>
      <Topbar />
      <main id="main" className="content content-wide">
        <PageHeader title="Databases" description="MariaDB, PostgreSQL and Valkey. Each customer has its own database servers, and each database of a website is in one of them. By default, only the website containers of the customer can connect." />
        <div className="stack">
          <DatabaseServersPanel />
          <DatabasesPanel />
        </div>
      </main>
    </>
  );
}
