import { Navigate, Outlet, Route, Routes, useLocation } from "react-router-dom";
import { useStore } from "./api/hooks";
import { retryLoad, signOut } from "./api/store";
import { Layout } from "./components/Layout";
import { HOME, canOpen } from "./lib/roles";
import { AccountPage } from "./pages/AccountPage";
import { AppsPage } from "./pages/AppsPage";
import { BackupsPage } from "./pages/BackupsPage";
import { CustomersPage } from "./pages/CustomersPage";
import { DatabasesPage } from "./pages/DatabasesPage";
import { JobsPage } from "./pages/JobsPage";
import { LoginPage } from "./pages/LoginPage";
import { NewWebsitePage } from "./pages/NewWebsitePage";
import { OverviewPage } from "./pages/OverviewPage";
import { PlansPage } from "./pages/PlansPage";
import { ServersPage } from "./pages/ServersPage";
import { SetPasswordPage } from "./pages/SetPasswordPage";
import { SettingsPage } from "./pages/SettingsPage";
import { SetupPage } from "./pages/SetupPage";
import { UpdatesPage } from "./pages/UpdatesPage";
import { WebsiteDetailPage } from "./pages/WebsiteDetailPage";
import { WebsitesPage } from "./pages/WebsitesPage";

function RequireSignIn({ children }: { children: React.ReactNode }) {
  const state = useStore();
  const location = useLocation();
  if (!state.session.signedIn) return <Navigate to="/login" state={{ from: location }} replace />;
  if (state.session.loading) return <main id="main" className="content"><p className="muted" role="status">Loading…</p></main>;
  // The first load failed, so the panel has no data and does not know the user. Before, the
  // panel showed empty pages with the menu of a website owner.
  if (state.session.loadError && !state.session.user.id) {
    return (
      <main id="main" className="content" style={{ maxWidth: 640 }}>
        <div className="alert alert-danger alert-stack" role="alert">
          <p><strong>The panel could not load its data.</strong> {state.session.loadError}</p>
          <p>The panel API can be busy or stopped. Try again in a moment. If the error stays, look at the service <code>cf-panel-api</code> on the panel server.</p>
          <div className="btn-group">
            <button type="button" className="btn btn-primary" onClick={() => void retryLoad()}>Try again</button>
            <button type="button" className="btn" onClick={() => signOut()}>Sign out</button>
          </div>
        </div>
      </main>
    );
  }
  // Audit finding B5: a temporary password gives access only to the form for a new password.
  if (state.session.mustChangePassword) return <SetPasswordPage />;
  return children;
}

/** Each console has only its own pages. A different page sends the user to the first page of the console. */
function RoleGuard() {
  const role = useStore().session.user.role;
  const { pathname } = useLocation();
  if (!canOpen(role, pathname)) return <Navigate to={HOME[role]} replace />;
  return <Outlet />;
}

function Home() {
  return <Navigate to={HOME[useStore().session.user.role]} replace />;
}

export function App() {
  return (
    <Routes>
      <Route path="/login" element={<LoginPage />} />
      <Route element={<RequireSignIn><Layout /></RequireSignIn>}>
        <Route element={<RoleGuard />}>
          <Route index element={<Home />} />
          <Route path="overview" element={<OverviewPage />} />
          <Route path="setup" element={<SetupPage />} />
          <Route path="servers" element={<ServersPage />} />
          <Route path="servers/:id" element={<ServersPage />} />
          <Route path="updates" element={<UpdatesPage />} />
          <Route path="customers" element={<CustomersPage />} />
          <Route path="customers/:tab" element={<CustomersPage />} />
          <Route path="plans" element={<PlansPage />} />
          <Route path="websites" element={<WebsitesPage />} />
          <Route path="websites/new" element={<NewWebsitePage />} />
          <Route path="websites/:id" element={<WebsiteDetailPage />} />
          <Route path="websites/:id/:tab" element={<WebsiteDetailPage />} />
          <Route path="databases" element={<DatabasesPage />} />
          <Route path="backups" element={<BackupsPage />} />
          <Route path="apps" element={<AppsPage />} />
          <Route path="jobs" element={<JobsPage />} />
          <Route path="account" element={<AccountPage />} />
          <Route path="settings" element={<SettingsPage />} />
          <Route path="settings/:tab" element={<SettingsPage />} />
          <Route path="*" element={<Home />} />
        </Route>
      </Route>
    </Routes>
  );
}
