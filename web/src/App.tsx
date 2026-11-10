import { Navigate, Route, Routes, useLocation } from "react-router-dom";
import { useStore } from "./api/hooks";
import { features } from "./api/mode";
import { Layout } from "./components/Layout";
import { AppsPage } from "./pages/AppsPage";
import { BackupsPage } from "./pages/BackupsPage";
import { CustomersPage } from "./pages/CustomersPage";
import { DatabasesPage } from "./pages/DatabasesPage";
import { DnsPage } from "./pages/DnsPage";
import { EmailPage } from "./pages/EmailPage";
import { FilesPage } from "./pages/FilesPage";
import { JobsPage } from "./pages/JobsPage";
import { LoginPage } from "./pages/LoginPage";
import { NewWebsitePage } from "./pages/NewWebsitePage";
import { ServersPage } from "./pages/ServersPage";
import { SetPasswordPage } from "./pages/SetPasswordPage";
import { SettingsPage } from "./pages/SettingsPage";
import { UpdatesPage } from "./pages/UpdatesPage";
import { WebsiteDetailPage } from "./pages/WebsiteDetailPage";
import { WebsitesPage } from "./pages/WebsitesPage";

function RequireSignIn({ children }: { children: React.ReactNode }) {
  const state = useStore();
  const location = useLocation();
  if (!state.session.signedIn) return <Navigate to="/login" state={{ from: location }} replace />;
  if (state.session.loading) return <main id="main" className="content"><p className="muted" role="status">Loading…</p></main>;
  // Audit finding B5: a temporary password gives access only to the form for a new password.
  if (state.session.mustChangePassword) return <SetPasswordPage />;
  return children;
}

function RequireAdmin({ children }: { children: React.ReactNode }) {
  const state = useStore();
  if (state.session.user.role !== "administrator") return <Navigate to="/websites" replace />;
  return children;
}

export function App() {
  return (
    <Routes>
      <Route path="/login" element={<LoginPage />} />
      <Route element={<RequireSignIn><Layout /></RequireSignIn>}>
        <Route index element={<Navigate to="/websites" replace />} />
        <Route path="websites" element={<WebsitesPage />} />
        <Route path="websites/new" element={<NewWebsitePage />} />
        <Route path="websites/:id" element={<WebsiteDetailPage />} />
        <Route path="websites/:id/:tab" element={<WebsiteDetailPage />} />
        {features.dns && <Route path="dns" element={<DnsPage />} />}
        {features.dns && <Route path="dns/:zone" element={<DnsPage />} />}
        {features.email && <Route path="email" element={<EmailPage />} />}
        <Route path="databases" element={<DatabasesPage />} />
        {features.files && <Route path="files" element={<FilesPage />} />}
        <Route path="backups" element={<BackupsPage />} />
        <Route path="apps" element={<AppsPage />} />
        <Route path="jobs" element={<JobsPage />} />
        <Route path="updates" element={<RequireAdmin><UpdatesPage /></RequireAdmin>} />
        <Route path="servers" element={<RequireAdmin><ServersPage /></RequireAdmin>} />
        <Route path="servers/:id" element={<RequireAdmin><ServersPage /></RequireAdmin>} />
        <Route path="customers" element={<CustomersPage />} />
        <Route path="settings" element={<SettingsPage />} />
        <Route path="*" element={<Navigate to="/websites" replace />} />
      </Route>
    </Routes>
  );
}
