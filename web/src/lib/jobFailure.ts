// Audit item U2: a failed job shows a short text and a reference to a customer. The command
// of the agent, the paths and the socket names are for administrators only.

import type { Job, Role } from "../api/types";

/** The reference of a job. A customer gives it to the provider, and an administrator finds the job with it. */
export function jobReference(job: Pick<Job, "id">): string {
  return `J-${job.id.slice(-8).toUpperCase()}`;
}

/**
 * True when the error is the output of a command on the server: a customer cannot use it, and
 * it shows names of the server. An error that the panel wrote for people, for example "The DNS
 * of example.com does not point to this server", is not technical.
 */
export function technicalError(error: string): boolean {
  return /exit status \d|\b(podman|runuser|systemctl|restic|runsc|caddy|nft|setquota|useradd)\b|XDG_RUNTIME_DIR|(^|[\s:=])\/(run|srv|usr|etc|var|home|tmp|opt)\/|\.sock\b|\.service\b|\s--?[a-z][a-z-]*[= ]/.test(error);
}

export interface JobFailure {
  /** The text for all users. */
  text: string;
  reference: string;
  /** The full error of the server. Only an administrator gets it. */
  details?: string;
}

/** The text of a failed job for a user with the role. */
export function jobFailure(job: Job, role: Role): JobFailure {
  const reference = jobReference(job);
  const error = job.error?.trim() ?? "";
  const admin = role === "administrator";
  if (error && !technicalError(error)) return { text: error, reference };
  const step = job.steps[Math.min(job.step, job.steps.length - 1)];
  const where = step ? `The step "${step}" failed on the server.` : "The job failed on the server.";
  return {
    text: admin ? where : `${where} Try again. If it fails again, give the reference to your provider.`,
    reference,
    details: admin && error ? error : undefined,
  };
}

/** The item of a job: two jobs with the same key do the same work on the same thing. */
const itemKey = (job: Job) => `${job.action ?? job.title}|${job.websiteId ?? ""}|${job.serverId ?? ""}|${job.ref ?? ""}`;

/**
 * The IDs of the failed jobs that a later job for the same item completed. These failures
 * need no attention: the panel shows them as resolved.
 */
export function resolvedFailures(jobs: Job[]): Set<string> {
  const succeeded = new Map<string, string>();
  for (const job of jobs) {
    if (job.state !== "succeeded") continue;
    const key = itemKey(job);
    const at = job.finishedAt ?? job.createdAt;
    if (!succeeded.has(key) || succeeded.get(key)! < at) succeeded.set(key, at);
  }
  const resolved = new Set<string>();
  for (const job of jobs) {
    const later = succeeded.get(itemKey(job));
    if (job.state === "failed" && later !== undefined && later > (job.finishedAt ?? job.createdAt)) resolved.add(job.id);
  }
  return resolved;
}

/** The failed jobs that no later job completed, newest first. */
export function openFailures(jobs: Job[]): Job[] {
  const resolved = resolvedFailures(jobs);
  return jobs.filter((j) => j.state === "failed" && !resolved.has(j.id));
}
