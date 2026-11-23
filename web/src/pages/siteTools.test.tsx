// @vitest-environment jsdom
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { afterEach, describe, expect, it } from "vitest";
import { App } from "../App";
import type { CronJob, Deploy, LogEntry } from "../api/types";
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
  const request: LogEntry = {
    at: "2026-11-14T09:58:52.757Z", source: "access", text: "",
    access: { client: "198.51.100.250", method: "POST", host: "shop.acme.example.com", uri: "/wp-login.php", proto: "HTTP/2.0", status: 404, sizeBytes: 548, durationMs: 1.2, userAgent: "zgrab", referer: "" },
  };
  const line: LogEntry = { at: "2026-11-14T09:58:52.700Z", source: "error", text: "PHP Warning:  Undefined array key", access: null };

  it("shows the requests, the lines and the summary of the search", async () => {
    await open("shop.acme.example.com", "logs");
    const view = await screen.findByLabelText("Log lines");
    await waitFor(() => expect(view.querySelectorAll(".log-row")).toHaveLength(50));
    const summary = screen.getByRole("region", { name: "Summary of the search" });
    expect(summary.textContent).toContain("Requests");
    expect(within(summary).getByRole("img").getAttribute("aria-label")).toMatch(/^Status codes: 2xx \d/);
    expect(screen.getByRole("button", { name: "Load older entries" })).toBeTruthy();
  });

  it("sends the search and the filters to the API, and loads older entries", async () => {
    const { api, id } = await open("shop.acme.example.com", "logs");
    await screen.findByRole("button", { name: "Load older entries" });
    const paths: string[] = [];
    api.on(`GET /websites/${id}/logs?*`, (_body, path) => {
      paths.push(path);
      const first = !path.includes("cursor=");
      return { entries: first ? [request] : [line], next: first ? "page-2" : null, summary: null };
    });
    const base = `/websites/${id}/logs`;

    fireEvent.change(screen.getByLabelText("Search the logs"), { target: { value: " wp-login " } });
    fireEvent.change(screen.getByLabelText("Client"), { target: { value: "198.51.100.0/24" } });
    fireEvent.click(screen.getByRole("button", { name: "Search" }));
    await waitFor(() => expect(paths.at(-1)).toBe(`${base}?q=wp-login&client=198.51.100.0%2F24&limit=50&summary=1`));
    fireEvent.click(screen.getByRole("button", { name: "4xx" }));
    fireEvent.change(screen.getByLabelText("Method"), { target: { value: "POST" } });
    fireEvent.click(screen.getByRole("button", { name: "Output" }));
    await waitFor(() => expect(paths.at(-1)).toBe(`${base}?sources=access%2Cerror&q=wp-login&status=4xx&method=POST&client=198.51.100.0%2F24&limit=50&summary=1`));
    expect(screen.getByText(/filters apply only to requests/)).toBeTruthy();

    const view = screen.getByLabelText("Log lines");
    await waitFor(() => expect(view.textContent).toContain("/wp-login.php"));
    fireEvent.click(screen.getByRole("button", { name: "Load older entries" }));
    await waitFor(() => expect(view.querySelectorAll(".log-row")).toHaveLength(2));
    expect(paths.at(-1)).toBe(`${base}?sources=access%2Cerror&q=wp-login&status=4xx&method=POST&client=198.51.100.0%2F24&cursor=page-2&limit=50`);
    expect(view.querySelector(".log-row.err")?.textContent).toContain("PHP Warning");
    expect(screen.queryByRole("button", { name: "Load older entries" })).toBeNull();

    fireEvent.click(screen.getByRole("button", { name: "Clear filters" }));
    await waitFor(() => expect(paths.at(-1)).toBe(`${base}?limit=50&summary=1`));
  });

  it("filters by a top client of the summary, and shows the error of the API", async () => {
    const { api, id } = await open("shop.acme.example.com", "logs");
    const top = (await screen.findAllByRole("button", { name: /^Show the requests of / }))[0];
    const client = top.getAttribute("aria-label")!.replace("Show the requests of ", "");
    api.on(`GET /websites/${id}/logs?*`, () => [422, { message: "Enter an IP address or a network, for example 203.0.113.7 or 203.0.113.0/24.", errors: { client: ["Enter an IP address or a network."] } }]);
    fireEvent.click(top);
    expect((screen.getByLabelText("Client") as HTMLInputElement).value).toBe(client);
    expect((await screen.findByRole("alert")).textContent).toContain("Enter an IP address");
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

describe("the deploys of a website", () => {
  it("shows the repository, the deploy key and the releases", async () => {
    await open("api.acme.example.com", "deploys");
    expect(await screen.findByText("git@github.com:acme-example/api.git")).toBeTruthy();
    expect(screen.getByLabelText("Deploy key").textContent).toMatch(/^ssh-ed25519 /);
    expect(screen.getByText("Live")).toBeTruthy();
    expect(screen.getByText("Failed")).toBeTruthy();
    expect(screen.getAllByRole("button", { name: /^Go back/ }).length).toBeGreaterThan(0);
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
    fireEvent.click(screen.getByRole("button", { name: "Change" }));
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
