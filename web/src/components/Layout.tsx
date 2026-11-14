import {
  Activity,
  Bell,
  Boxes,
  ChevronsUpDown,
  CircleUser,
  Database,
  Gauge,
  Globe,
  History,
  Layers,
  ListChecks,
  LogOut,
  Menu,
  Search,
  Server,
  Settings2,
  Tags,
  Users,
} from "lucide-react";
import { createContext, useContext, useEffect, useRef, useState, type ReactNode } from "react";
import { Link, NavLink, Outlet, useLocation, useNavigate } from "react-router-dom";
import { useStore } from "../api/hooks";
import { markNotificationsRead, setContext, signOut } from "../api/store";
import type { AccountContext, Tone } from "../api/types";
import { relativeTime } from "../lib/format";
import { jobActive } from "../lib/labels";
import { HOME, NAVIGATION, ROLE_LABEL, type NavEntry, type NavSection } from "../lib/roles";
import { CommandPalette } from "./CommandPalette";
import { Status } from "./ui";

interface LayoutState {
  openSearch: (placeholder?: string) => void;
  toggleNav: () => void;
}

const LayoutContext = createContext<LayoutState>({ openSearch: () => {}, toggleNav: () => {} });

const DEFAULT_PLACEHOLDER = "Search websites, domains and jobs";

export function Logo() {
  return (
    <svg width="28" height="28" viewBox="0 0 32 32" aria-hidden="true">
      <path d="M8 19a5 5 0 0 1 1.2-9.8A6.5 6.5 0 0 1 21.6 9.6 4.8 4.8 0 0 1 24 19Z" fill="#fff" />
      <path d="M5 21h22v6H5z" fill="#5b8def" />
      <path d="M8 23h3v2H8zm5 0h3v2h-3zm5 0h3v2h-3z" fill="#0f1b33" />
    </svg>
  );
}

function ContextSwitcher() {
  const state = useStore();
  const [open, setOpen] = useState(false);
  const ref = useRef<HTMLDivElement>(null);
  const shown = state.contexts.find((c) => c.id === state.session.contextId) ?? state.contexts[0];

  useEffect(() => {
    if (!open) return;
    const close = (e: MouseEvent) => !ref.current?.contains(e.target as Node) && setOpen(false);
    document.addEventListener("mousedown", close);
    return () => document.removeEventListener("mousedown", close);
  }, [open]);
  if (!shown) return null;

  const summary = (c: AccountContext) => {
    if (c.role === "administrator") return `Administrator · ${state.servers.length} ${state.servers.length === 1 ? "server" : "servers"}`;
    const customer = state.customers.find((x) => x.id === c.id);
    const plan = state.plans.find((p) => p.id === customer?.planId);
    const count = state.websites.filter((w) => w.customerId === c.id).length;
    if (c.role === "reseller") {
      // Batch 3, issue 2: a reseller also manages the websites of its customers.
      const customers = state.customers.filter((x) => x.resellerId === c.id).map((x) => x.id);
      const all = state.websites.filter((w) => w.customerId === c.id || customers.includes(w.customerId)).length;
      return `Reseller · ${all} ${all === 1 ? "website" : "websites"} · ${customers.length} ${customers.length === 1 ? "customer" : "customers"}`;
    }
    return `Website owner · ${count} of ${plan?.websites ?? count} websites`;
  };

  return (
    <div className="context-switcher" ref={ref}>
      <button
        type="button"
        className="context-button"
        aria-haspopup="menu"
        aria-expanded={open}
        onClick={() => setOpen((o) => !o)}
      >
        <span>
          <span className="context-name">{shown.name}</span>
          <span className="context-summary">{summary(shown)}</span>
        </span>
        <ChevronsUpDown size={16} aria-hidden="true" />
      </button>
      {open && (
        <ul className="context-menu" role="menu">
          {state.contexts.map((c) => (
            <li key={c.id} role="none">
              <button
                type="button"
                role="menuitemradio"
                aria-checked={c.id === shown.id}
                onClick={() => {
                  setContext(c.id);
                  setOpen(false);
                }}
              >
                <span className="context-name">{c.name}</span>
                <span className="context-summary">{summary(c)}</span>
              </button>
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}

function Notifications() {
  const state = useStore();
  const [open, setOpen] = useState(false);
  const ref = useRef<HTMLDivElement>(null);
  const unread = state.notifications.filter((n) => !n.read).length;

  useEffect(() => {
    if (!open) return;
    const close = (e: MouseEvent) => !ref.current?.contains(e.target as Node) && setOpen(false);
    const esc = (e: KeyboardEvent) => e.key === "Escape" && setOpen(false);
    document.addEventListener("mousedown", close);
    document.addEventListener("keydown", esc);
    return () => {
      document.removeEventListener("mousedown", close);
      document.removeEventListener("keydown", esc);
    };
  }, [open]);

  return (
    <div className="notifications" ref={ref}>
      <button
        type="button"
        className="btn btn-icon bell"
        aria-label={unread ? `Notifications, ${unread} unread` : "Notifications"}
        aria-expanded={open}
        onClick={() => setOpen((o) => !o)}
      >
        <Bell size={18} aria-hidden="true" />
        {unread > 0 && <span className="dot" aria-hidden="true" />}
      </button>
      {open && (
        <div className="notifications-panel" role="dialog" aria-label="Notifications">
          <header>
            <h2>Notifications</h2>
            <button type="button" className="btn btn-sm" onClick={markNotificationsRead} disabled={!unread}>Mark all as read</button>
          </header>
          <ul>
            {state.notifications.slice(0, 12).map((n) => (
              <li key={n.id} className={n.read ? "" : "unread"}>
                <Link to={n.href ?? "/jobs"} onClick={() => setOpen(false)}>
                  <Status tone={n.tone}>{n.read ? "" : "New · "}{TONE_LABEL[n.tone]}</Status>
                  <span style={{ display: "block", marginTop: 4 }}>{n.text}</span>
                  <time dateTime={n.at}>{relativeTime(n.at)}</time>
                </Link>
              </li>
            ))}
          </ul>
        </div>
      )}
    </div>
  );
}

const TONE_LABEL: Record<Tone, string> = { success: "Done", info: "Information", warning: "Warning", danger: "Problem", neutral: "Note" };

const ICONS: Record<NavEntry["icon"], ReactNode> = {
  overview: <Gauge size={18} />,
  setup: <ListChecks size={18} />,
  servers: <Server size={18} />,
  updates: <Layers size={18} />,
  customers: <Users size={18} />,
  plans: <Tags size={18} />,
  websites: <Globe size={18} />,
  databases: <Database size={18} />,
  backups: <History size={18} />,
  apps: <Boxes size={18} />,
  jobs: <Activity size={18} />,
  account: <CircleUser size={18} />,
  settings: <Settings2 size={18} />,
};

/** A count or a mark at the end of a link, for example the number of open setup items. */
function NavMark({ to }: { to: string }) {
  const state = useStore();
  if (to === "/updates" && (state.rollout?.state === "running" || state.rollout?.state === "paused")) {
    return <span className="count">1<span className="visually-hidden"> in progress</span></span>;
  }
  if (to === "/setup" && state.setup && state.setup.open > 0) {
    return <span className="count">{state.setup.open}<span className="visually-hidden"> open</span></span>;
  }
  // A dot, not a word: with a word, the name of the page does not fit on one line.
  if (to === "/jobs" && state.jobs.some(jobActive)) {
    return <span className="live" title="A job is in progress" style={{ marginLeft: "auto", color: "#9cc0ff" }}><span className="visually-hidden">A job is in progress</span></span>;
  }
  return null;
}

function NavGroup({ section, index }: { section: NavSection; index: number }) {
  const id = `nav-${index}`;
  return (
    <>
      {section.title && <h2 className="nav-section" id={id}>{section.title}</h2>}
      <ul className="nav-list" aria-labelledby={section.title ? id : undefined}>
        {section.items.map((item) => (
          <li key={item.to}>
            <NavLink to={item.to} className={({ isActive }) => `nav-link${isActive ? " active" : ""}`}>
              <span aria-hidden="true">{ICONS[item.icon]}</span>
              {item.label}
              <NavMark to={item.to} />
            </NavLink>
          </li>
        ))}
      </ul>
    </>
  );
}

export function Layout() {
  const [palette, setPalette] = useState<string | null>(null);
  const [navOpen, setNavOpen] = useState(false);
  const location = useLocation();

  useEffect(() => setNavOpen(false), [location.pathname]);

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === "k") {
        e.preventDefault();
        setPalette((p) => (p === null ? DEFAULT_PLACEHOLDER : null));
      }
      if (e.key === "Escape") setNavOpen(false);
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, []);

  const state = useStore();
  const navigate = useNavigate();
  const user = state.session.user;
  const initials = user.name.split(" ").map((p) => p[0]).join("");

  return (
    <LayoutContext.Provider
      value={{ openSearch: (p) => setPalette(p ?? DEFAULT_PLACEHOLDER), toggleNav: () => setNavOpen((o) => !o) }}
    >
      <a className="skip-link" href="#main">Skip to content</a>
      <div className="app">
        <nav className={`sidebar${navOpen ? " open" : ""}`} aria-label="Main">
          <NavLink to={HOME[user.role]} className="brand">
            <Logo />
            <span className="visually-hidden">Cloudfactory home</span>
            <span aria-hidden="true" style={{ color: "#fff" }}>
              cloudfactory<span>.io</span>
            </span>
          </NavLink>
          <ContextSwitcher />
          {/* Each role has its own menu. A setup that is complete needs no link. */}
          {NAVIGATION[user.role]
            .map((section) => ({ ...section, items: section.items.filter((item) => item.to !== "/setup" || (state.setup ? !state.setup.complete : false)) }))
            .map((section, i) => <NavGroup key={section.title ?? i} section={section} index={i} />)}
          <div className="sidebar-user">
            <span className="avatar" aria-hidden="true">{initials}</span>
            <span style={{ flex: 1 }}>
              <span className="name" style={{ display: "block" }}>{user.name}</span>
              <span className="role">{ROLE_LABEL[user.role]}</span>
            </span>
            <button
              type="button"
              className="btn btn-icon btn-ghost"
              style={{ color: "var(--nav-muted)" }}
              aria-label="Sign out"
              onClick={() => {
                signOut();
                navigate("/login");
              }}
            >
              <LogOut size={18} aria-hidden="true" />
            </button>
          </div>
        </nav>
        <div className="main">
          {state.session.loadError && (
            <div className="alert alert-warning load-error" role="status">
              <p><strong>The panel could not load new data.</strong> {state.session.loadError} The pages show the data of the last good load. The panel tries again each 15 seconds.</p>
            </div>
          )}
          <Outlet />
        </div>
      </div>
      <CommandPalette open={palette !== null} placeholder={palette ?? ""} onClose={() => setPalette(null)} />
    </LayoutContext.Provider>
  );
}

const isMac = typeof navigator !== "undefined" && /Mac|iPhone|iPad/.test(navigator.platform);

/** The bar at the top of each page: search, status and page actions. */
export function Topbar({ placeholder = DEFAULT_PLACEHOLDER, children, primary }: {
  placeholder?: string;
  /** Status items before the notifications button. */
  children?: ReactNode;
  /** The main page action, after the notifications button. */
  primary?: ReactNode;
}) {
  const { openSearch, toggleNav } = useContext(LayoutContext);
  return (
    <header className="topbar">
      <button type="button" className="btn btn-icon menu-button" aria-label="Open menu" onClick={toggleNav}>
        <Menu size={20} aria-hidden="true" />
      </button>
      <button type="button" className="search-button" onClick={() => openSearch(placeholder)} aria-label={`${placeholder} (${isMac ? "⌘K" : "Ctrl+K"})`}>
        <Search size={18} aria-hidden="true" />
        <span className="label">{placeholder}</span>
        <kbd aria-hidden="true">{isMac ? "⌘K" : "Ctrl K"}</kbd>
      </button>
      <div className="topbar-actions">
        {children}
        <Notifications />
        {primary}
      </div>
    </header>
  );
}
