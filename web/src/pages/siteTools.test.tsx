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

/** Opens the menu of a row of the file list, and selects an item. */
function menu(name: string, item: string) {
  fireEvent.click(screen.getByRole("button", { name: `More actions for ${name}` }));
  fireEvent.click(screen.getByRole("menuitem", { name: item }));
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
    menu("index.php", "Remove");
    api.on(`DELETE /websites/${id}/files?path=%2Fpublic%2Findex.php`, () => [204, null]);
    fireEvent.click(within(screen.getByRole("dialog")).getByRole("button", { name: "Remove" }));
    await waitFor(() => expect(api.changes.at(-1)?.method).toBe("DELETE"));
  });

  it("shows the permissions and changes them with the check boxes", async () => {
    const { api, id } = await open("shop.acme.example.com", "files");
    await openPublic();
    expect(screen.getAllByText("0640").length).toBe(2);
    menu("index.php", "Permissions");
    const dialog = within(screen.getByRole("dialog"));
    const octal = dialog.getByLabelText("Octal value (file)") as HTMLInputElement;
    expect(octal.value).toBe("0640");
    fireEvent.click(dialog.getByLabelText("Others: read (file)"));
    expect(octal.value).toBe("0644");
    expect(dialog.getByText("rw-r--r--")).toBeTruthy();
    fireEvent.change(octal, { target: { value: "64" } });
    expect(dialog.getByText("Enter 3 octal digits, for example 644 or 0755.")).toBeTruthy();
    expect((dialog.getByRole("button", { name: "Change" }) as HTMLButtonElement).disabled).toBe(true);
    fireEvent.change(octal, { target: { value: "600" } });
    api.on(`PUT /websites/${id}/files/permissions`, () => ({ file: { name: "index.php", path: "/public/index.php", type: "file", sizeBytes: 103, modifiedAt: "2026-11-14T10:00:00Z", mode: "0600" }, changed: 1 }));
    fireEvent.click(dialog.getByRole("button", { name: "Change" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    expect(api.changes.at(-1)).toEqual({ method: "PUT", path: `/websites/${id}/files/permissions`, body: { path: "/public/index.php", mode: "600" } });
  });

  it("changes the permissions of a folder and of all files in it", async () => {
    const { api, id } = await open("shop.acme.example.com", "files");
    await openPublic();
    menu("css", "Permissions");
    const dialog = within(screen.getByRole("dialog"));
    // A folder must keep Read and Open for the owner.
    fireEvent.click(dialog.getByLabelText("Owner: open (folders)"));
    expect(dialog.getByText("The owner must keep Read and Open for a folder.")).toBeTruthy();
    fireEvent.click(dialog.getByLabelText("Owner: open (folders)"));
    fireEvent.click(dialog.getByLabelText("Also change all folders and files in this folder"));
    fireEvent.click(dialog.getByLabelText("Others: open (folders)"));
    fireEvent.click(dialog.getByLabelText("Others: read (folders)"));
    fireEvent.change(dialog.getByLabelText("Octal value (files)"), { target: { value: "0644" } });
    api.on(`PUT /websites/${id}/files/permissions`, () => ({ file: { name: "css", path: "/public/css", type: "directory", sizeBytes: 0, modifiedAt: "2026-11-14T10:00:00Z", mode: "0755" }, changed: 7 }));
    fireEvent.click(dialog.getByRole("button", { name: "Change" }));
    expect(await screen.findByText("Changed the permissions of 7 files and folders in /public/css.")).toBeTruthy();
    expect(api.changes.at(-1)?.body).toEqual({ path: "/public/css", mode: "0755", recursive: true, fileMode: "0644" });
  });

  it("makes an archive of the selected files and shows the summary of the job", async () => {
    const { api, id } = await open("shop.acme.example.com", "files");
    await openPublic();
    fireEvent.click(screen.getByLabelText("Select index.php"));
    fireEvent.click(screen.getByLabelText("Select robots.txt"));
    expect(screen.getByText("2 items selected")).toBeTruthy();
    fireEvent.click(within(screen.getByRole("region", { name: "Selected files" })).getByRole("button", { name: "Make archive" }));
    const dialog = within(screen.getByRole("dialog"));
    fireEvent.change(dialog.getByLabelText("Name of the archive"), { target: { value: "pages" } });
    fireEvent.change(dialog.getByLabelText("Format"), { target: { value: ".tar.gz" } });
    expect(dialog.getByText("/public/pages.tar.gz")).toBeTruthy();
    const job = apiJob({ websiteId: id, action: "MakeArchive", title: "Make archive", ref: "/public/pages.tar.gz", steps: ["Make the archive"], step: 1, result: { files: 2, directories: 0, links: 0, sizeBytes: 2048, kept: 0, skipped: 0, skippedNames: [] } });
    api.on(`POST /websites/${id}/files/archives`, () => [202, { job }]);
    fireEvent.click(dialog.getByRole("button", { name: "Make archive" }));
    expect(await screen.findByText(/^The archive pages\.tar\.gz is ready: 2 files and 0 folders/)).toBeTruthy();
    expect(api.changes.find((c) => c.path.endsWith("/files/archives"))?.body).toEqual({ paths: ["/public/index.php", "/public/robots.txt"], path: "/public/pages.tar.gz" });
    expect(screen.queryByText("2 items selected")).toBeNull();
  });

  it("extracts an archive and shows the entries that the server skipped", async () => {
    const { api, id } = await open("shop.acme.example.com", "files");
    await screen.findAllByText("site-2026-10.zip");
    expect(screen.queryByRole("button", { name: /^Edit\s*site-2026-10\.zip$/ })).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: /^Extract\s*site-2026-10\.zip$/ }));
    const dialog = within(screen.getByRole("dialog"));
    expect((dialog.getByLabelText("Extract to the folder") as HTMLInputElement).value).toBe("/");
    fireEvent.change(dialog.getByLabelText("Extract to the folder"), { target: { value: "restore" } });
    fireEvent.click(dialog.getByLabelText("Replace files that exist"));
    expect(dialog.getByText(/You cannot undo this/)).toBeTruthy();
    const job = apiJob({ websiteId: id, action: "ExtractArchive", title: "Extract archive", ref: "/site-2026-10.zip", result: { files: 4, directories: 2, links: 0, sizeBytes: 900, kept: 0, skipped: 1, skippedNames: ["../evil.php"] } });
    api.on(`POST /websites/${id}/files/extract`, () => [202, { job }]);
    fireEvent.click(dialog.getByRole("button", { name: "Extract" }));
    expect(await screen.findByText(/^Extracted site-2026-10\.zip to \/restore: 4 files and 2 folders\. The server skipped 1 entry/)).toBeTruthy();
    expect(screen.getByText("../evil.php")).toBeTruthy();
    expect(api.changes.find((c) => c.path.endsWith("/files/extract"))?.body).toEqual({ path: "/site-2026-10.zip", to: "/restore", overwrite: true });
  });

  it("shows the error of a failed archive job", async () => {
    const { api, id } = await open("shop.acme.example.com", "files");
    await screen.findAllByText("site-2026-10.zip");
    fireEvent.click(screen.getByRole("button", { name: /^Extract\s*site-2026-10\.zip$/ }));
    const job = apiJob({ websiteId: id, action: "ExtractArchive", title: "Extract archive", ref: "/site-2026-10.zip", state: "failed", error: "The archive is damaged or is not a supported archive." });
    api.on(`POST /websites/${id}/files/extract`, () => [202, { job }]);
    fireEvent.click(within(screen.getByRole("dialog")).getByRole("button", { name: "Extract" }));
    expect(await screen.findByText("Extract archive /site-2026-10.zip failed. The archive is damaged or is not a supported archive.")).toBeTruthy();
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
