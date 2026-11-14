import { Topbar } from "../components/Layout";
import { PageHeader } from "../components/ui";
import { DatabasesPanel } from "../features/DatabasesPanel";

export function DatabasesPage() {
  return (
    <>
      <Topbar />
      <main id="main" className="content">
        <PageHeader title="Databases" description="MariaDB, PostgreSQL and Valkey. Each customer database is a separate database on the shared server. By default, only the website container can connect." />
        <DatabasesPanel />
      </main>
    </>
  );
}
