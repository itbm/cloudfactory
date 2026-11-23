// @vitest-environment jsdom
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { afterEach, describe, expect, it } from "vitest";
import { App } from "../App";
import type { BackupPolicy } from "../api/types";
import { reset, signedIn } from "../test/panelApi";
import { storagePath } from "./BackupsPage";

afterEach(() => {
  cleanup();
  reset();
});

const card = () => screen.getByRole("heading", { name: "Storage key of each server" }).closest("section")!;
const row = (server: string) => within(card()).getByText(server).closest("tr")!;

/** Second audit, finding F08: each server can have its own key for the object storage. */
describe("the storage key of each server", () => {
  it("gives the bucket and the path that the key of a server must have access to", () => {
    expect(storagePath("s3:s3.amazonaws.com/bucket/path/servers/ID")).toBe("bucket/path/servers/ID");
    expect(storagePath("s3:https://minio.example.com:9000/bucket/servers/ID")).toBe("bucket/servers/ID");
    expect(storagePath("b2:bucket:servers/ID")).toBe("bucket:servers/ID");
    expect(storagePath("azure:container:/servers/ID")).toBe("container:/servers/ID");
  });

  it("shows the servers that use the shared key", async () => {
    await signedIn("administrator");
    render(<MemoryRouter initialEntries={["/backups"]}><App /></MemoryRouter>);
    // In the sample data, web-1 has its own key and the other servers use the shared key. The
    // mail server has a repository for the backups of the mailboxes.
    expect(row("web-1").textContent).toContain("Own key");
    expect(row("web-2").textContent).toContain("Shared key");
    expect(row("mail-1").textContent).toContain("Shared key");
    expect(card().textContent).toMatch(/3 servers use the shared key\. A person who has root access to one of these servers can delete or change the backups of all servers/);
    expect(row("web-1").textContent).toMatch(/cloudfactory-backups\/servers\/[0-9a-z]{26}/);
    expect(within(row("web-1")).getByRole("button", { name: /^Remove key\s*of web-1$/ })).toBeTruthy();
    expect(within(row("web-2")).queryByRole("button", { name: /Remove key/ })).toBeNull();
  });

  it("sends the key of one server to the API, and shows no warning when each server has a key", async () => {
    const api = await signedIn("administrator");
    render(<MemoryRouter initialEntries={["/backups"]}><App /></MemoryRouter>);
    api.on("PUT /backup-policy/servers/*/credentials", (_body, path) => {
      api.change<BackupPolicy>("/backup-policy", (policy) => {
        for (const s of policy.servers!) if (path.includes(s.serverId) || s.server === "web-3" || s.server === "mail-1") s.credentialNames = ["AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY"];
      });
      return {};
    });
    fireEvent.click(within(row("web-2")).getByRole("button", { name: /^Set key\s*of web-2$/ }));
    const dialog = screen.getByRole("dialog", { name: "Storage key of web-2" });
    // The secret access key is missing: the form sends nothing.
    fireEvent.change(within(dialog).getByLabelText("Access key ID"), { target: { value: "AKIAWEB2" } });
    fireEvent.click(within(dialog).getByRole("button", { name: "Save key" }));
    await waitFor(() => expect(dialog.textContent).toContain("Type the secret access key."));
    expect(api.changes.filter((c) => c.method === "PUT")).toEqual([]);

    fireEvent.change(within(dialog).getByLabelText("Secret access key"), { target: { value: "secret-2" } });
    fireEvent.click(within(dialog).getByRole("button", { name: "Save key" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    const sent = api.changes.find((c) => c.method === "PUT")!;
    expect(sent.path).toMatch(/^\/backup-policy\/servers\/[0-9a-z]{26}\/credentials$/);
    expect(sent.body).toEqual({ credentials: { AWS_ACCESS_KEY_ID: "AKIAWEB2", AWS_SECRET_ACCESS_KEY: "secret-2" } });
    await waitFor(() => expect(row("web-2").textContent).toContain("Own key"));
    expect(card().textContent).not.toContain("shared key.");
  });

  it("removes the key of a server after a confirmation", async () => {
    const api = await signedIn("administrator");
    render(<MemoryRouter initialEntries={["/backups"]}><App /></MemoryRouter>);
    api.on("DELETE /backup-policy/servers/*/credentials", () => {
      api.change<BackupPolicy>("/backup-policy", (policy) => { for (const s of policy.servers!) s.credentialNames = []; });
      return {};
    });
    fireEvent.click(within(row("web-1")).getByRole("button", { name: /^Remove key\s*of web-1$/ }));
    const dialog = screen.getByRole("dialog", { name: "Remove the key of web-1?" });
    expect(dialog.textContent).toContain("then uses the shared key");
    fireEvent.click(within(dialog).getByRole("button", { name: "Remove key" }));
    await waitFor(() => expect(row("web-1").textContent).toContain("Shared key"));
    expect(card().textContent).toContain("All servers use the shared key.");
  });

  it("does not show the servers to a reseller or an owner", async () => {
    for (const role of ["reseller", "owner"] as const) {
      await signedIn(role);
      render(<MemoryRouter initialEntries={["/backups"]}><App /></MemoryRouter>);
      expect(screen.getByRole("heading", { level: 1 }).textContent).toBe("Backups");
      expect(screen.queryByRole("heading", { name: "Storage key of each server" })).toBeNull();
      cleanup();
      reset();
    }
  });
});
