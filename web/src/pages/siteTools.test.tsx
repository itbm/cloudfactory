// @vitest-environment jsdom
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { afterEach, describe, expect, it } from "vitest";
import { App } from "../App";
import type { CronJob, Deploy } from "../api/types";
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

  it("connects a repository to a website that has none", async () => {
    const { api, id } = await open("shop.acme.example.com", "deploys");
    const answer: Deploy = {
      source: { repository: "https://example.com/shop.git", branch: "main", webDirectory: ".", trigger: "manual", buildCommands: ["composer install"], deployKey: "ssh-ed25519 AAAA key", webhookUrl: "https://panel/hook", secrets: [] },
      releases: [],
    };
    api.on(`PUT /websites/${id}/deploy`, () => answer);
    fireEvent.change(await screen.findByLabelText("Repository"), { target: { value: " https://example.com/shop.git " } });
    fireEvent.change(screen.getByLabelText("Web directory"), { target: { value: "" } });
    fireEvent.change(screen.getByLabelText("Build commands"), { target: { value: "composer install\n\n" } });
    fireEvent.click(screen.getByRole("button", { name: "Connect repository" }));
    expect(await screen.findByLabelText("Deploy key")).toBeTruthy();
    expect(api.changes.at(-1)?.body).toEqual({ repository: "https://example.com/shop.git", branch: "main", webDirectory: ".", trigger: "manual", buildCommands: ["composer install"] });
  });
});
