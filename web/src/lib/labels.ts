import type { Domain, HostUpdatePolicy, JobState, RuntimeKind, ServerRole, ServerState, UpdatePolicy, WebsiteStatus } from "../api/types";
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

/** Short certificate text for the website list. */
export function certificateText(d: Pick<Domain, "certificate" | "dnsPointsHere">): string {
  const c = d.certificate;
  switch (c.state) {
    case "valid":
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

export function certificateUntil(d: Pick<Domain, "certificate" | "dnsPointsHere">, websiteFailed = false): string {
  const c = d.certificate;
  // The panel could not make the website, so the edge proxy does not know the domain. Before,
  // the text was "certificate waits for DNS", which sent the user to the DNS.
  if (websiteFailed && c.state === "pending") return "no certificate yet";
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
