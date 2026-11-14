import type { DatabaseServerStatus, Domain, HostUpdatePolicy, JobState, RuntimeKind, ServerRole, ServerState, UpdatePolicy, WebsiteStatus } from "../api/types";
import type { Tone } from "../components/ui";
import { daysUntil, longDate, relativeTime } from "./format";

export const WEBSITE_STATUS: Record<WebsiteStatus, { label: string; tone: Tone }> = {
  running: { label: "Running", tone: "success" },
  stopped: { label: "Stopped", tone: "neutral" },
  starting: { label: "Starting", tone: "info" },
  stopping: { label: "Stopping", tone: "info" },
  failed: { label: "Failed", tone: "danger" },
};

export const JOB_STATE: Record<JobState, { label: string; tone: Tone }> = {
  queued: { label: "Queued", tone: "neutral" },
  running: { label: "In progress", tone: "info" },
  succeeded: { label: "Done", tone: "success" },
  failed: { label: "Failed", tone: "danger" },
};

/** A job that waits for the server, or that the server does now. The panel shows the two as work in progress. */
export const jobActive = (job: { state: JobState }): boolean => job.state === "queued" || job.state === "running";

export const SERVER_STATE: Record<ServerState, { label: string; tone: Tone }> = {
  healthy: { label: "Healthy", tone: "success" },
  updating: { label: "Updating", tone: "info" },
  agent_update: { label: "Agent update ready", tone: "warning" },
  rebooting: { label: "Rebooting", tone: "info" },
  joining: { label: "Joining", tone: "info" },
  degraded: { label: "Degraded", tone: "warning" },
  offline: { label: "Offline", tone: "danger" },
};

export const SERVER_ROLE: Record<ServerRole, string> = {
  web: "web",
  database: "database",
  email: "email",
  dns: "DNS",
  backup: "backups",
};

export function roleList(roles: ServerRole[]): string {
  const text = roles.map((r) => SERVER_ROLE[r]).join(", ");
  return text.charAt(0).toUpperCase() + text.slice(1);
}

export const RUNTIME_KIND: Record<RuntimeKind, string> = {
  frankenphp: "FrankenPHP",
  "apache-php-fpm": "Apache + PHP-FPM",
  oci: "OCI image",
  static: "Static files",
};

/**
 * True when a public certificate authority gives certificates for the name. The API and the
 * agent have the same rule (PanelCertificate::publicName, caddy.PublicName). For a different
 * name, for example shop.test, the CA of the panel signs the certificate.
 */
export function publicName(name: string): boolean {
  const n = name.toLowerCase().replace(/\.$/, "");
  if (!n.includes(".") || /^[\d.]+$/.test(n) || n.includes(":")) return false;
  return ![".localhost", ".local", ".internal", ".home.arpa", ".test", ".example", ".invalid"].some((suffix) => n.endsWith(suffix));
}

/** Audit item U12: the text for a valid certificate that the CA of the panel signed. */
export const PANEL_CA_NOTE = "panel CA, not trusted by browsers";

type CertificateOf = Pick<Domain, "certificate" | "dnsPointsHere"> & { name?: string };

/** True when the CA of the panel signed the certificate of the domain. */
const fromPanelCa = (d: CertificateOf) => d.name !== undefined && !publicName(d.name);

/** Short certificate text for the website list. */
export function certificateText(d: CertificateOf): string {
  const c = d.certificate;
  switch (c.state) {
    case "valid":
      if (fromPanelCa(d)) return `Valid (${PANEL_CA_NOTE})`;
      return c.expiresAt ? `Valid · renews in ${daysUntil(c.expiresAt)} days` : "Valid";
    case "renewed":
      return c.renewedAt ? `Renewed ${relativeTime(c.renewedAt)}` : "Renewed";
    case "pending":
      // The DNS is correct: the edge proxy gets the certificate now. This takes some minutes at most.
      return d.dnsPointsHere ? "Getting the certificate" : "Waiting for DNS";
    case "failed":
      return "No certificate: the request failed";
    case "not_needed":
      return "Not needed while stopped";
  }
}

export function certificateUntil(d: CertificateOf, websiteFailed = false): string {
  const c = d.certificate;
  // The panel could not make the website, so the edge proxy does not know the domain. Before,
  // the text was "certificate waits for DNS", which sent the user to the DNS.
  if (websiteFailed && c.state === "pending") return "no certificate yet";
  if ((c.state === "valid" || c.state === "renewed") && fromPanelCa(d)) return `certificate valid (${PANEL_CA_NOTE})`;
  if (c.state === "valid" || c.state === "renewed") return c.expiresAt ? `certificate valid until ${longDate(c.expiresAt)}` : "certificate valid";
  // Before, this text was "certificate waits for DNS" also when the DNS was correct.
  if (c.state === "pending") return d.dnsPointsHere ? "the edge proxy gets the certificate" : "certificate waits for DNS";
  if (c.state === "failed") return "the edge proxy could not get a certificate";
  return "no certificate";
}


export const POLICY_LABEL: Record<UpdatePolicy, string> = {
  automatic: "Automatic",
  security_automatic: "Security fixes: automatic, in the window",
  manual: "Manual",
};

export const HOST_POLICY_LABEL: Record<HostUpdatePolicy, string> = {
  security: "Security updates only",
  all: "All updates",
  manual: "Manual",
};

/** The name of each database engine. */
export const ENGINE_NAME = { mariadb: "MariaDB", postgresql: "PostgreSQL", valkey: "Valkey" } as const;

/** The host name that a website container connects to, for each database engine. */
export const DATABASE_HOST = { mariadb: "mariadb.internal", postgresql: "postgres.internal", valkey: "valkey.internal" } as const;

/** The port of the first database server of an engine. A second database server of the engine gets the next port. */
export const DATABASE_PORT = { mariadb: 3306, postgresql: 5432, valkey: 6379 } as const;

export const DATABASE_SERVER_STATUS: Record<DatabaseServerStatus, { label: string; tone: Tone }> = {
  starting: { label: "Starting", tone: "info" },
  running: { label: "Running", tone: "success" },
  stopped: { label: "Stopped", tone: "neutral" },
  failed: { label: "Failed", tone: "danger" },
  removing: { label: "Removing", tone: "info" },
};
