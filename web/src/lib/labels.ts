import type { Certificate, HostUpdatePolicy, JobState, RuntimeKind, ServerRole, ServerState, UpdatePolicy, WebsiteStatus } from "../api/types";
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
export function certificateText(c: Certificate): string {
  switch (c.state) {
    case "valid":
      return c.expiresAt ? `Valid · renews in ${daysUntil(c.expiresAt)} days` : "Valid";
    case "renewed":
      return c.renewedAt ? `Renewed ${relativeTime(c.renewedAt)}` : "Renewed";
    case "pending":
      return "Waiting for DNS";
    case "failed":
      return "Renewal failed";
    case "not_needed":
      return "Not needed while stopped";
  }
}

export function certificateUntil(c: Certificate): string {
  if (c.state === "valid" || c.state === "renewed") return c.expiresAt ? `certificate valid until ${longDate(c.expiresAt)}` : "certificate valid";
  if (c.state === "pending") return "certificate waits for DNS";
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
