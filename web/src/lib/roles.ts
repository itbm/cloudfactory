// The panel has one console for each role: an administrator console, a reseller console and
// an owner console. Each console has its own menu, its own first page, and only its own pages.

import type { Role } from "../api/types";

/** The first page of each console. */
export const HOME: Record<Role, string> = {
  administrator: "/overview",
  reseller: "/customers",
  owner: "/websites",
};

export const ROLE_LABEL: Record<Role, string> = {
  administrator: "Administrator",
  reseller: "Reseller",
  owner: "Website owner",
};

export interface NavEntry {
  to: string;
  label: string;
  /** The name of the icon (components/Layout.tsx). */
  icon: "overview" | "setup" | "servers" | "network" | "updates" | "customers" | "plans" | "websites" | "files" | "databases" | "email" | "dns" | "networks" | "backups" | "apps" | "jobs" | "alerts" | "account" | "brand" | "settings" | "plugins";
}

export interface NavSection {
  /** No title: the links come first, with no heading. */
  title?: string;
  items: NavEntry[];
}

const hosting = (websites: string, jobs: string): NavEntry[] => [
  { to: "/websites", label: websites, icon: "websites" },
  { to: "/files", label: "Files", icon: "files" },
  { to: "/databases", label: "Databases", icon: "databases" },
  { to: "/email", label: "Email", icon: "email" },
  { to: "/dns", label: "DNS", icon: "dns" },
  { to: "/networks", label: "Private networks", icon: "networks" },
  { to: "/backups", label: "Backups", icon: "backups" },
  { to: "/apps", label: "Apps", icon: "apps" },
  { to: "/jobs", label: jobs, icon: "jobs" },
  { to: "/alerts", label: "Usage alerts", icon: "alerts" },
];

const account: NavSection = {
  title: "Account",
  items: [
    { to: "/account", label: "My account", icon: "account" },
    { to: "/settings", label: "Settings", icon: "settings" },
  ],
};

/** The menu of each console. An owner has no link to a page of an administrator or a reseller. */
export const NAVIGATION: Record<Role, NavSection[]> = {
  administrator: [
    { items: [{ to: "/overview", label: "Overview", icon: "overview" }, { to: "/setup", label: "Finish setup", icon: "setup" }] },
    { title: "Platform", items: [{ to: "/servers", label: "Servers", icon: "servers" }, { to: "/network", label: "Network", icon: "network" }, { to: "/updates", label: "Images and updates", icon: "updates" }] },
    { title: "Customers", items: [{ to: "/customers", label: "Customers", icon: "customers" }, { to: "/plans", label: "Plans", icon: "plans" }] },
    { title: "Hosting", items: hosting("Websites", "Jobs and activity") },
    { title: "Panel", items: [{ to: "/plugins", label: "Plugins", icon: "plugins" }, { to: "/settings", label: "Settings", icon: "settings" }] },
  ],
  reseller: [
    { title: "Customers", items: [{ to: "/customers", label: "Customers", icon: "customers" }, { to: "/plans", label: "Plans", icon: "plans" }, { to: "/brand", label: "Brand", icon: "brand" }] },
    { title: "Hosting", items: hosting("Websites", "Jobs and activity") },
    account,
  ],
  owner: [
    { title: "Hosting", items: hosting("My websites", "Activity") },
    account,
  ],
};

/** The pages that only some roles can open. A page that is not in the list is for all roles. */
const ONLY: [prefix: string, roles: Role[]][] = [
  ["/overview", ["administrator"]],
  ["/setup", ["administrator"]],
  ["/servers", ["administrator"]],
  ["/network", ["administrator"]],
  ["/updates", ["administrator"]],
  ["/customers", ["administrator", "reseller"]],
  ["/plans", ["administrator", "reseller"]],
  ["/account", ["reseller", "owner"]],
  ["/plugins", ["administrator"]],
  // White label: a reseller sets its brand. The administrator sets the default brand in Settings.
  ["/brand", ["reseller"]],
];

/** The address of a plugin page in the web panel. Each role can open it, when the plugin gives it the page. */
export const pluginPagePath = (pluginId: string, pageId: string) => `/plugin/${pluginId}/${pageId}`;

/** True when a user with the role can open the page. */
export function canOpen(role: Role, path: string): boolean {
  // Adversarial review, finding W1-03: the router ignores the case of the letters and reads
  // "%73" as "s". The check uses the same form of the path.
  let clean = path;
  try {
    clean = decodeURIComponent(path);
  } catch {
    // The path stays as it is.
  }
  clean = clean.toLowerCase().replace(/\/+$/, "") || "/";
  const rule = ONLY.find(([prefix]) => clean === prefix || clean.startsWith(`${prefix}/`));
  return !rule || rule[1].includes(role);
}
