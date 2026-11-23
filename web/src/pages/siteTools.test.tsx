// @vitest-environment jsdom
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { afterEach, describe, expect, it } from "vitest";
import { App } from "../App";
import type { CronJob, Deploy, WebsiteCommand } from "../api/types";
import { toBase64 } from "../features/FileManager";
import { apiJob, reset, signedIn, website, type PanelApi } from "../test/panelApi";

afterEach(() => {
  cleanup();
  reset();
});

/** Signs in as an owner and opens a tab of a website of the sample data. */
async function open(name: string, tab: string): Promise<{ api: PanelApi; id: string }> {
  const api = await signedIn("owner");
  const id = website(name).id;
  render(<MemoryRouter initialEntries={[`/websites/${id}/${tab}`]}><App /></MemoryRouter>);
  return { api, id };
}

/** Waits for the top folder, and opens the folder "public". */
async function openPublic() {
  await screen.findAllByText("composer.json");
  fireEvent.click(screen.getByRole("button", { name: /^Folder\s*public$/ }));
  await screen.findAllByText("index.php");
}

describe("the file manager", () => {
  it("shows the files of the website and opens a folder", async () => {
    await open("shop.acme.example.com", "files");
    await openPublic();
    expect(screen.getByRole("button", { name: "public" }).getAttribute("aria-current")).toBe("location");
  });

  it("opens a text file in the editor and saves it", async () => {
    const { api, id } = await open("shop.acme.example.com", "files");
    await openPublic();
    fireEvent.click(screen.getByRole("button", { name: /^Edit\s*index\.php$/ }));
    const editor = await screen.findByLabelText("Content of index.php") as HTMLTextAreaElement;
    expect(editor.value).toContain("<?php");
    api.on(`PUT /websites/${id}/files/content`, (body) => ({ file: { name: "index.php", path: (body as { path: string }).path, type: "file", sizeBytes: 5, modifiedAt: "2026-11-14T10:00:00Z", mode: "0644" } }));
    fireEvent.change(editor, { target: { value: "<?php" } });
    fireEvent.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(screen.queryByLabelText("Content of index.php")).toBeNull());
    expect(api.changes.at(-1)).toEqual({ method: "PUT", path: `/websites/${id}/files/content`, body: { path: "/public/index.php", content: btoa("<?php"), encoding: "base64" } });
  });

  it("makes a folder in the open folder and shows the error of the API", async () => {
    const { api, id } = await open("shop.acme.example.com", "files");
    await openPublic();
    fireEvent.click(screen.getByRole("button", { name: "New folder" }));
    api.on(`POST /websites/${id}/files/directories`, () => [409, { message: "A file or a directory with this name exists." }]);
    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "css" } });
    fireEvent.click(screen.getByRole("button", { name: "Create" }));
    expect(await screen.findByText("A file or a directory with this name exists.")).toBeTruthy();
    expect(api.changes.at(-1)?.body).toEqual({ path: "/public/css" });
  });

  it("removes a file after a confirmation", async () => {
    const { api, id } = await open("shop.acme.example.com", "files");
    await openPublic();
    fireEvent.click(screen.getByRole("button", { name: /^Remove\s*index\.php$/ }));
    api.on(`DELETE /websites/${id}/files?path=%2Fpublic%2Findex.php`, () => [204, null]);
    fireEvent.click(within(screen.getByRole("dialog")).getByRole("button", { name: "Remove" }));
    await waitFor(() => expect(api.changes.at(-1)?.method).toBe("DELETE"));
  });

  it("encodes the bytes of an upload", () => {
    expect(toBase64(new Uint8Array([0, 255, 16, 32]))).toBe("AP8QIA==");
    expect(atob(toBase64(new Uint8Array(70000).fill(65))).length).toBe(70000);
  });

  it("has a page with the files of each website", async () => {
    await signedIn("owner");
    render(<MemoryRouter initialEntries={["/files"]}><App /></MemoryRouter>);
    expect(screen.getByRole("heading", { level: 1 }).textContent).toBe("Files");
    expect((screen.getByLabelText("Website") as HTMLSelectElement).options.length).toBeGreaterThan(1);
    expect((await screen.findAllByText("composer.json")).length).toBeGreaterThan(0);
  });
});

describe("the logs of a website", () => {
  it("shows the log lines and filters them", async () => {
    await open("shop.acme.example.com", "logs");
    const view = await screen.findByLabelText("Log lines");
    await waitFor(() => expect(view.querySelectorAll("div").length).toBeGreaterThan(5));
    fireEvent.change(screen.getByLabelText("Filter"), { target: { value: "no line has this text" } });
    expect(view.textContent).toBe("No lines match the filter.");
  });
});

describe("the cron jobs of a website", () => {
  it("shows the cron jobs and adds one", async () => {
    const { api, id } = await open("shop.acme.example.com", "cron");
    expect((await screen.findAllByText("wp cron event run --due-now")).length).toBeGreaterThan(0);
    const made: CronJob = { id: "c-new", schedule: "0 3 * * *", command: "php backup.php", enabled: true, lastRunAt: null, lastResult: null, lastExitCode: null, lastOutput: null };
    api.on(`POST /websites/${id}/cron-jobs`, () => [202, { cronJob: made, job: apiJob({ websiteId: id, action: "SetCronJobs" }) }]);
    api.on("GET /jobs/job-1", () => apiJob({ websiteId: id, action: "SetCronJobs" }));
    fireEvent.change(screen.getByLabelText("Schedule"), { target: { value: "0  3 * * *" } });
    fireEvent.change(screen.getByLabelText("Command"), { target: { value: " php backup.php " } });
    fireEvent.click(screen.getByRole("button", { name: "Add" }));
    await waitFor(() => expect(api.changes.at(-1)?.body).toEqual({ schedule: "0 3 * * *", command: "php backup.php" }));
  });

  it("shows the error of the API next to the schedule", async () => {
    const { api, id } = await open("shop.acme.example.com", "cron");
    await screen.findAllByText("wp cron event run --due-now");
    api.on(`POST /websites/${id}/cron-jobs`, () => [422, { message: "x", errors: { schedule: ["The schedule must have 5 fields."] } }]);
    fireEvent.change(screen.getByLabelText("Command"), { target: { value: "true" } });
    fireEvent.click(screen.getByRole("button", { name: "Add" }));
    expect(await screen.findByText("The schedule must have 5 fields.")).toBeTruthy();
  });
});

describe("run a command and the command log", () => {
  const entry = (over: Partial<WebsiteCommand> = {}): WebsiteCommand => ({
    id: "c-new", websiteId: "", source: "run", sessionId: null, command: "php artisan migrate --force", state: "done", exitCode: 0, timedOut: false, timeoutSeconds: 600,
    actor: "Olu Owner", ip: "203.0.113.14", jobId: "job-1", createdAt: "2026-11-14T10:00:00+00:00", startedAt: "2026-11-14T10:00:00+00:00", finishedAt: "2026-11-14T10:00:03+00:00", ...over,
  });

  it("shows the command log, with a role name for an administrator", async () => {
    await open("api.acme.example.com", "commands");
    expect(await screen.findByText("php artisan queue:restart")).toBeTruthy();
    const row = screen.getByText("du -sh storage/logs").closest("tr")!;
    expect(within(row).getByText("Administrator")).toBeTruthy();
    expect(screen.getByText("Exit code 1")).toBeTruthy();
    expect(document.body.textContent).not.toMatch(/podman|runuser|\.sock/);
  });

  it("runs a command and shows its output", async () => {
    const { api, id } = await open("api.acme.example.com", "commands");
    await screen.findByText("php artisan queue:restart");
    api.on(`POST /websites/${id}/commands`, () => [202, { command: entry({ websiteId: id, state: "running", exitCode: null }), job: apiJob({ websiteId: id, action: "RunCommand" }) }]);
    api.on("GET /jobs/job-1", () => ({ data: apiJob({ websiteId: id, action: "RunCommand" }) }));
    api.on(`GET /websites/${id}/commands/c-new`, () => ({ command: entry({ websiteId: id, output: "INFO  Running migrations." }) }));
    fireEvent.change(screen.getByLabelText("Command"), { target: { value: " php artisan migrate --force " } });
    fireEvent.change(screen.getByLabelText("Time limit"), { target: { value: "1800" } });
    fireEvent.click(screen.getByRole("button", { name: "Run" }));
    expect((await screen.findByLabelText("Output of the command")).textContent).toBe("INFO  Running migrations.");
    expect(screen.getAllByText("Exit code 0").length).toBeGreaterThan(0);
    expect(api.changes.at(-1)).toEqual({ method: "POST", path: `/websites/${id}/commands`, body: { command: "php artisan migrate --force", timeoutSeconds: 1800 } });
  });

  it("shows the output of an entry and uses its command again", async () => {
    const { api, id } = await open("api.acme.example.com", "commands");
    await screen.findByText("php artisan queue:restart");
    api.on(`GET /websites/${id}/commands/*`, () => ({ command: entry({ websiteId: id, command: "php artisan orders:export --since=yesterday", exitCode: 1, output: "PHP Fatal error" }) }));
    fireEvent.click(screen.getByRole("button", { name: /^Show output\s*of php artisan orders:export/ }));
    const dialog = await screen.findByRole("dialog");
    expect((await within(dialog).findByLabelText("Output")).textContent).toBe("PHP Fatal error");
    fireEvent.click(within(dialog).getByRole("button", { name: "Use this command again" }));
    expect((screen.getByLabelText("Command") as HTMLInputElement).value).toBe("php artisan orders:export --since=yesterday");
  });

  it("does not run a command on a stopped website", async () => {
    await open("old.acme.example.com", "commands");
    fireEvent.change(screen.getByLabelText("Command"), { target: { value: "php -v" } });
    expect((screen.getByRole("button", { name: "Run" }) as HTMLButtonElement).disabled).toBe(true);
    expect(screen.getByText("The website does not run. Start it before you run a command.")).toBeTruthy();
  });
});

describe("the deploys of a website", () => {
  it("shows the repository, the deploy key and the releases", async () => {
    await open("api.acme.example.com", "deploys");
    expect(await screen.findByText("git@github.com:acme-example/api.git")).toBeTruthy();
    expect(screen.getByLabelText("Deploy key").textContent).toMatch(/^ssh-ed25519 /);
    const releases = within(screen.getByRole("heading", { name: "Releases" }).closest("section")!);
    expect(releases.getByText("Live")).toBeTruthy();
    expect(releases.getByText("Failed")).toBeTruthy();
    expect(releases.getAllByRole("button", { name: /^Go back/ }).length).toBeGreaterThan(0);
  });

  it("starts a deploy", async () => {
    const { api, id } = await open("api.acme.example.com", "deploys");
    const job = apiJob({ websiteId: id, action: "Deploy", title: "Deploy" });
    api.on(`POST /websites/${id}/deploy/releases`, () => [202, { release: {}, job }]);
    api.on("GET /jobs/job-1", () => job);
    await screen.findByText("git@github.com:acme-example/api.git");
    fireEvent.click(screen.getByRole("button", { name: "Deploy now" }));
    await waitFor(() => expect(api.changes.at(-1)?.path).toBe(`/websites/${id}/deploy/releases`));
  });

  it("shows the migration command, and a release that went back after the health check", async () => {
    await open("api.acme.example.com", "deploys");
    expect(await screen.findByText("php artisan migrate --force")).toBeTruthy();
    expect(screen.getByText("Went back")).toBeTruthy();
    expect(screen.getByText("the health check failed after the switch: 9 of 30 checks failed.")).toBeTruthy();
    expect(screen.getByLabelText("Webhook secret").textContent).not.toBe("");
    expect(screen.queryByRole("alert")).toBeNull();
  });

  it("shows an alert when the live release failed its health check after a migration", async () => {
    const api = await signedIn("owner");
    const id = website("api.acme.example.com").id;
    const healthError = "the health check failed after the switch: 9 of 30 checks failed.";
    api.change<Deploy>(`/websites/${id}/deploy`, (d) => { d.releases.find((r) => r.live)!.healthError = healthError; });
    render(<MemoryRouter initialEntries={[`/websites/${id}/deploys`]}><App /></MemoryRouter>);
    const alert = await screen.findByRole("alert");
    expect(alert.textContent).toContain("The live release failed its health check after a migration.");
    expect(within(alert).getByText(healthError)).toBeTruthy();
    expect(screen.getByText("Check failed")).toBeTruthy();
  });

  it("changes the trigger to a schedule with a migration command", async () => {
    const { api, id } = await open("api.acme.example.com", "deploys");
    let deploy = {} as Deploy;
    api.change<Deploy>(`/websites/${id}/deploy`, (d) => { deploy = structuredClone(d); });
    api.on(`PUT /websites/${id}/deploy`, () => ({ ...deploy, source: { ...deploy.source!, trigger: "schedule", checkMinutes: 15, migrationCommand: null, webhookUrl: null } }));
    await screen.findByText("git@github.com:acme-example/api.git");
    // The card of the previews also has a "Change" button, when its settings load first.
    const repository = within(screen.getByRole("heading", { name: "Git repository" }).closest("section")!);
    fireEvent.click(repository.getByRole("button", { name: "Change" }));
    fireEvent.click(screen.getByRole("radio", { name: /^Schedule/ }));
    fireEvent.change(screen.getByLabelText("Check the branch every"), { target: { value: "15" } });
    fireEvent.change(screen.getByLabelText("Migration command"), { target: { value: "  " } });
    fireEvent.click(screen.getByRole("button", { name: "Save" }));
    expect(await screen.findByText(/The panel checks the branch every 15 minutes/)).toBeTruthy();
    expect(api.changes.at(-1)?.body).toMatchObject({ trigger: "schedule", checkMinutes: 15, migrationCommand: null });
    expect(screen.queryByLabelText("Webhook secret")).toBeNull();
  });

  it("makes a new webhook address and secret", async () => {
    const { api, id } = await open("api.acme.example.com", "deploys");
    let deploy = {} as Deploy;
    api.change<Deploy>(`/websites/${id}/deploy`, (d) => { deploy = structuredClone(d); });
    api.on(`POST /websites/${id}/deploy/webhook`, () => ({ ...deploy, source: { ...deploy.source!, webhookUrl: "https://panel/api/v1/hooks/deploy/new", webhookSecret: "new-secret" } }));
    await screen.findByText("git@github.com:acme-example/api.git");
    fireEvent.click(screen.getByRole("button", { name: "New address and secret" }));
    fireEvent.click(await screen.findByRole("button", { name: "Make new address" }));
    await waitFor(() => expect(screen.getByLabelText("Webhook secret").textContent).toBe("new-secret"));
    expect(api.changes.at(-1)?.path).toBe(`/websites/${id}/deploy/webhook`);
  });

  it("connects a repository to a website that has none", async () => {
    const { api, id } = await open("shop.acme.example.com", "deploys");
    const answer: Deploy = {
      source: {
        repository: "https://example.com/shop.git", branch: "main", webDirectory: ".", trigger: "manual", checkMinutes: 5, checkedAt: null, checkError: null,
        buildCommands: ["composer install"], migrationCommand: null, deployKey: "ssh-ed25519 AAAA key", webhookUrl: "https://panel/hook", webhookSecret: "s3cret", deployQueued: false, secrets: [],
      },
      releases: [],
    };
    api.on(`PUT /websites/${id}/deploy`, () => answer);
    fireEvent.change(await screen.findByLabelText("Repository"), { target: { value: " https://example.com/shop.git " } });
    fireEvent.change(screen.getByLabelText("Web directory"), { target: { value: "" } });
    fireEvent.change(screen.getByLabelText("Build commands"), { target: { value: "composer install\n\n" } });
    fireEvent.click(screen.getByRole("button", { name: "Connect repository" }));
    expect(await screen.findByLabelText("Deploy key")).toBeTruthy();
    expect(api.changes.at(-1)?.body).toEqual({ repository: "https://example.com/shop.git", branch: "main", webDirectory: ".", trigger: "manual", checkMinutes: 5, buildCommands: ["composer install"], migrationCommand: null });
  });
});

describe("the previews of pull requests", () => {
  /** The card of the previews on the Deploys tab of the website. */
  const card = async () => within((await screen.findByRole("heading", { name: "Previews of pull requests" })).closest("section")!);

  it("shows the settings, the webhook address and the previews", async () => {
    await open("api.acme.example.com", "deploys");
    const previews = await card();
    expect(await previews.findByRole("link", { name: "pr-42.preview.acme.example.com" })).toBeTruthy();
    expect(previews.getByText("pr-NUMBER.preview.acme.example.com")).toBeTruthy();
    expect(previews.getByText("1 preview at the same time")).toBeTruthy();
    // The website deploys with the webhook, so the card points to the webhook of the repository.
    expect(previews.getByText("Access to the repository")).toBeTruthy();
    expect(previews.getByRole("link", { name: /Add the export of invoices as CSV/ }).getAttribute("href")).toBe("https://github.com/acme-example/api/pull/42");
    expect(previews.getByRole("link", { name: /Visit preview/ }).getAttribute("href")).toBe("https://pr-42.preview.acme.example.com");
    // The panel could not make the preview of #47: the limit is 1.
    expect(previews.getByText(/The limit of the previews of this website is 1/)).toBeTruthy();
    expect(previews.getByText("No website")).toBeTruthy();
  });

  it("shows the webhook address and secret when the website deploys by hand", async () => {
    const api = await signedIn("owner");
    const id = website("api.acme.example.com").id;
    api.change<Deploy>(`/websites/${id}/deploy`, (d) => { d.source!.trigger = "manual"; });
    render(<MemoryRouter initialEntries={[`/websites/${id}/deploys`]}><App /></MemoryRouter>);
    const previews = await card();
    expect((await previews.findByLabelText("Webhook address")).textContent).toMatch(/\/hooks\/deploy\//);
    expect(previews.getByLabelText("Webhook secret").textContent).not.toBe("");
  });

  it("changes the limit of the previews", async () => {
    const { api, id } = await open("api.acme.example.com", "deploys");
    const previews = await card();
    await previews.findByText("1 preview at the same time");
    api.on(`PUT /websites/${id}/previews`, (body) => {
      const b = body as { limit: number };
      return { settings: { enabled: true, domain: "preview.acme.example.com", limit: b.limit, copyEnvironment: false }, suggestedDomain: "preview.api.acme.example.com", maxLimit: 10, previews: [] };
    });
    fireEvent.click(previews.getByRole("button", { name: "Change" }));
    fireEvent.change(previews.getByLabelText("Previews at the same time"), { target: { value: "3" } });
    fireEvent.click(previews.getByRole("button", { name: "Save" }));
    expect(await previews.findByText("3 previews at the same time")).toBeTruthy();
    expect(api.changes.at(-1)?.body).toEqual({ enabled: true, domain: "preview.acme.example.com", limit: 3, copyEnvironment: false });
  });

  it("makes a preview by hand and shows the error of the API", async () => {
    const { api, id } = await open("api.acme.example.com", "deploys");
    const previews = await card();
    fireEvent.click(await previews.findByRole("button", { name: "Make a preview by hand" }));
    api.on(`POST /websites/${id}/previews`, () => [422, { message: "The limit of the previews of this website is 1.", errors: { number: ["The limit of the previews of this website is 1."] } }]);
    fireEvent.change(previews.getByLabelText("Pull request"), { target: { value: "#51" } });
    fireEvent.change(previews.getByLabelText("Branch"), { target: { value: " feature/menu " } });
    fireEvent.click(previews.getByRole("button", { name: "Make preview" }));
    expect(await previews.findByText("The limit of the previews of this website is 1.")).toBeTruthy();
    expect(api.changes.at(-1)).toEqual({ method: "POST", path: `/websites/${id}/previews`, body: { number: 51, branch: "feature/menu" } });
  });

  it("removes a preview after a confirmation", async () => {
    const { api, id } = await open("api.acme.example.com", "deploys");
    const previews = await card();
    fireEvent.click(await previews.findByRole("button", { name: /^Remove\s*the preview of #42$/ }));
    api.on(`DELETE /websites/${id}/previews/42`, () => [202, { job: null }]);
    fireEvent.click(within(screen.getByRole("dialog")).getByRole("button", { name: "Remove" }));
    await waitFor(() => expect(api.changes.at(-1)).toEqual({ method: "DELETE", path: `/websites/${id}/previews/42`, body: undefined }));
  });

  it("shows on a preview website where its deploy settings come from", async () => {
    await open("pr-42.preview.acme.example.com", "deploys");
    expect(await screen.findByText(/This website is the preview of pull request #42/)).toBeTruthy();
    expect(screen.getByText("Preview", { selector: ".tag" })).toBeTruthy();
    expect(screen.getByRole("link", { name: "api.acme.example.com" }).getAttribute("href")).toMatch(/\/deploys$/);
    expect(await screen.findByText("feature/invoice-csv", { selector: "td code" })).toBeTruthy();
    // The preview takes the settings and the secrets from the website of the pull request.
    expect(screen.queryByRole("button", { name: "Change" })).toBeNull();
    expect(screen.queryByRole("heading", { name: "Build secrets" })).toBeNull();
    expect(screen.queryByRole("heading", { name: "Previews of pull requests" })).toBeNull();
    expect(screen.getByRole("button", { name: "Deploy now" })).toBeTruthy();
  });
});
