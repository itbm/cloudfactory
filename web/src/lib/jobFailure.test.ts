import { describe, expect, it } from "vitest";
import type { Job } from "../api/types";
import { jobFailure, jobReference, openFailures, resolvedFailures, technicalError } from "./jobFailure";

const job = (over: Partial<Job>): Job => ({
  id: "01m7d7zxavzr32nrvw5zbyg4pe", title: "Create database", action: "CreateDatabase", state: "failed",
  steps: ["Create database", "Create user"], step: 0, createdAt: "2026-11-14T09:00:00Z", finishedAt: "2026-11-14T09:01:00Z", websiteId: "w1", ref: "shop",
  ...over,
});

const COMMAND = "runuser -u cf-db -- env XDG_RUNTIME_DIR=/run/user/994 podman exec cf-valkey valkey-cli -s /run/valkey/valkey.sock PING: exit status 1";

describe("the cause of a failed job (audit item U2)", () => {
  it("knows the output of a command from a text for people", () => {
    expect(technicalError(COMMAND)).toBe(true);
    expect(technicalError("systemctl start cf-site-a.service: exit status 1")).toBe(true);
    expect(technicalError("The DNS of shop.example.com does not point to this server.")).toBe(false);
    expect(technicalError("The image has no valid signature.")).toBe(false);
  });

  it("gives a customer a short text and a reference, and no command", () => {
    for (const role of ["owner", "reseller"] as const) {
      const failure = jobFailure(job({ error: COMMAND }), role);
      expect(failure.text).toBe('The step "Create database" failed on the server. Try again. If it fails again, give the reference to your provider.');
      expect(failure.reference).toBe("J-5ZBYG4PE");
      expect(failure.details).toBeUndefined();
      expect(JSON.stringify(failure)).not.toContain("podman");
    }
  });

  it("gives an administrator the error of the server as details", () => {
    const failure = jobFailure(job({ error: COMMAND }), "administrator");
    expect(failure.text).toBe('The step "Create database" failed on the server.');
    expect(failure.details).toBe(COMMAND);
    expect(failure.reference).toBe(jobReference(job({})));
  });

  it("shows a text for people to all roles", () => {
    const error = "The DNS of shop.example.com does not point to this server.";
    expect(jobFailure(job({ error }), "owner")).toEqual({ text: error, reference: "J-5ZBYG4PE" });
  });

  it("marks a failure as resolved when a later job for the same item is successful", () => {
    const failed = job({ id: "a" });
    const later = job({ id: "b", state: "succeeded", createdAt: "2026-11-14T09:05:00Z", finishedAt: "2026-11-14T09:06:00Z" });
    const earlier = job({ id: "c", state: "succeeded", createdAt: "2026-11-14T08:00:00Z", finishedAt: "2026-11-14T08:01:00Z" });
    const other = job({ id: "d", state: "succeeded", websiteId: "w2", createdAt: "2026-11-14T09:05:00Z", finishedAt: "2026-11-14T09:06:00Z" });

    expect([...resolvedFailures([later, failed])]).toEqual(["a"]);
    // A job before the failure, or for a different website, does not resolve it.
    expect(resolvedFailures([failed, earlier, other]).size).toBe(0);
    expect(openFailures([later, failed, job({ id: "e", action: "Backup" })]).map((j) => j.id)).toEqual(["e"]);
  });
});
