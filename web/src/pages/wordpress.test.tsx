// @vitest-environment jsdom
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { afterEach, describe, expect, it } from "vitest";
import { App } from "../App";
import { wpCliArguments } from "../api/store";
import type { WordPressState } from "../api/types";
import { apiJob, reset, signedIn, website, type PanelApi } from "../test/panelApi";

afterEach(() => {
  cleanup();
  reset();
});

/** Signs in as the owner of Acme and opens the WordPress tab of a website of the sample data. */
async function open(name: string): Promise<{ api: PanelApi; id: string }> {
  const api = await signedIn("owner");
  const id = website(name).id;
  render(<MemoryRouter initialEntries={[`/websites/${id}/wordpress`]}><App /></MemoryRouter>);
  return { api, id };
}

const job = (action: string) => ({ job: apiJob({ id: `job-${action}`, action, title: action, websiteId: "x" }) });

describe("the WordPress tab", () => {
  it("shows the versions, the updates, the last update and the restore point", async () => {
    await open("acme.example.com");
    expect(await screen.findByText("WordPress 6.8.3")).toBeTruthy();
    expect(screen.getByText("4 updates")).toBeTruthy();
    expect(screen.getByRole("link", { name: /Open WordPress admin/ }).getAttribute("href")).toBe("https://acme.example.com/wp-admin/");
    expect(screen.getByText("Yoast SEO")).toBeTruthy();
    expect(screen.getByText(/The automatic update of .* succeeded: WordPress, akismet\. WordPress 6\.8\.2 to 6\.8\.3\./)).toBeTruthy();
    expect(screen.getByText(/before an update of WordPress 6\.8\.2/)).toBeTruthy();
    // The output of the last WP-CLI command, and the history with no passwords.
    expect(screen.getByLabelText("Output of the last command").textContent).toBe("Success: Updated user 2.");
    expect(screen.getByText("wp user update 2 --user_pass=***", { selector: "td code" })).toBeTruthy();
  });

  it("updates all, or only the selected plugins and themes", async () => {
    const { api, id } = await open("acme.example.com");
    await screen.findByText("WordPress 6.8.3");
    api.on(`POST /websites/${id}/wordpress/updates`, () => [202, job("UpdateWordPress")]);

    fireEvent.click(screen.getByRole("button", { name: "Update all" }));
    await waitFor(() => expect(api.changes.at(-1)).toEqual({ method: "POST", path: `/websites/${id}/wordpress/updates`, body: { all: true } }));

    fireEvent.click(screen.getByRole("checkbox", { name: "Select the update of Wordfence Security" }));
    fireEvent.click(screen.getByRole("checkbox", { name: "Select the update of Astra" }));
    fireEvent.click(screen.getByRole("button", { name: "Update selected" }));
    await waitFor(() => expect(api.changes.at(-1)?.body).toEqual({ plugins: ["wordfence"], themes: ["astra"] }));

    fireEvent.click(screen.getByRole("button", { name: /^Update\s*WordPress to 6\.9$/ }));
    await waitFor(() => expect(api.changes.at(-1)?.body).toEqual({ core: "latest" }));
  });

  it("rolls back after a confirmation that names the database", async () => {
    const { api, id } = await open("acme.example.com");
    await screen.findByText("WordPress 6.8.3");
    api.on(`POST /websites/${id}/wordpress/restore`, () => [202, job("RestoreWordPress")]);
    fireEvent.click(screen.getByRole("button", { name: "Roll back" }));
    const dialog = screen.getByRole("dialog");
    expect(within(dialog).getByText("The database also goes back.")).toBeTruthy();
    fireEvent.click(within(dialog).getByRole("button", { name: "Roll back" }));
    await waitFor(() => expect(api.changes.at(-1)).toEqual({ method: "POST", path: `/websites/${id}/wordpress/restore`, body: undefined }));
  });

  it("shows an update that the agent rolled back", async () => {
    const api = await signedIn("reseller");
    const id = website("cornerbakery.example.org").id;
    render(<MemoryRouter initialEntries={[`/websites/${id}/wordpress`]}><App /></MemoryRouter>);
    const alert = await screen.findByText(/The update of .* failed: elementor\./);
    expect(alert.closest("[role=alert]")?.textContent).toContain("The agent put back the restore point");
    // Only the sign-in: the tab reads and changes nothing.
    expect(api.changes.map((c) => c.path)).toEqual(["/auth/login"]);
  });

  it("changes the automatic updates", async () => {
    const { api, id } = await open("acme.example.com");
    await screen.findByText("WordPress 6.8.3");
    api.on(`PATCH /websites/${id}/wordpress`, (body) => ({ wordpress: { autoUpdates: (body as { autoUpdates: object }).autoUpdates } }));
    fireEvent.click(screen.getByRole("switch", { name: "Update the themes automatically" }));
    await waitFor(() => expect(api.changes.at(-1)).toEqual({ method: "PATCH", path: `/websites/${id}/wordpress`, body: { autoUpdates: { themes: true } } }));
    fireEvent.change(screen.getByLabelText("WordPress"), { target: { value: "off" } });
    await waitFor(() => expect(api.changes.at(-1)?.body).toEqual({ autoUpdates: { core: "off" } }));
  });

  it("runs a WP-CLI command with each argument apart, and shows the error of the API", async () => {
    const { api, id } = await open("acme.example.com");
    await screen.findByText("WordPress 6.8.3");
    api.on(`POST /websites/${id}/wordpress/cli`, () => [202, {
      command: { id: "c1", command: "wp option update blogname 'Acme Ltd'", status: "running", exitCode: null, output: null, ranAt: null, createdAt: null, user: "Olu", jobId: "job-RunWpCli" },
      ...job("RunWpCli"),
    }]);
    fireEvent.change(screen.getByLabelText("Command"), { target: { value: `wp option update blogname "Acme Ltd"` } });
    fireEvent.click(screen.getByRole("button", { name: "Run" }));
    await waitFor(() => expect(api.changes.at(-1)).toEqual({ method: "POST", path: `/websites/${id}/wordpress/cli`, body: { args: ["option", "update", "blogname", "Acme Ltd"] } }));

    api.on(`POST /websites/${id}/wordpress/cli`, () => [422, { message: "x", errors: { args: ["The panel cannot run wp shell: it needs a terminal."] } }]);
    fireEvent.change(screen.getByLabelText("Command"), { target: { value: "shell" } });
    fireEvent.click(screen.getByRole("button", { name: "Run" }));
    expect(await screen.findByText("The panel cannot run wp shell: it needs a terminal.")).toBeTruthy();
  });

  it("installs WordPress on a website with no WordPress and shows the password one time", async () => {
    const { api, id } = await open("api.acme.example.com");
    expect(await screen.findByRole("heading", { name: "Install WordPress" })).toBeTruthy();
    api.on(`POST /websites/${id}/wordpress`, () => [202, {
      wordpress: { status: "installing" }, admin: { user: "olu", password: "Kx7-secret-password", loginUrl: "https://api.acme.example.com/wp-login.php" }, ...job("InstallWordPress"),
    }]);
    api.on(`GET /websites/${id}/wordpress`, (): WordPressState => ({ wordpress: { status: "installing" } as never, commands: [], busy: "job-InstallWordPress" }));
    fireEvent.change(screen.getByLabelText("Site title"), { target: { value: " Acme API " } });
    fireEvent.change(screen.getByLabelText("Administrator user name"), { target: { value: "olu" } });
    fireEvent.change(screen.getByLabelText("Administrator email"), { target: { value: "olu@acme.example.com" } });
    fireEvent.click(screen.getByRole("button", { name: "Install WordPress" }));
    const dialog = await screen.findByRole("dialog");
    expect(within(dialog).getByLabelText("Administrator password").textContent).toBe("Kx7-secret-password");
    expect(api.changes.at(-1)?.body).toEqual({ title: "Acme API", adminUser: "olu", adminEmail: "olu@acme.example.com", locale: "en_GB" });
    fireEvent.click(within(dialog).getByRole("button", { name: "Done" }));
    expect(await screen.findByText(/The panel installs WordPress\. This takes about one minute\./)).toBeTruthy();
  });

  it("has no WordPress tab for a website with no PHP", async () => {
    await signedIn("owner");
    const site = website("storefront.acme.example.com");
    render(<MemoryRouter initialEntries={[`/websites/${site.id}`]}><App /></MemoryRouter>);
    await screen.findByRole("link", { name: "Runtime" });
    expect(screen.queryByRole("link", { name: "WordPress" })).toBeNull();
  });
});

describe("a staging copy of a WordPress site", () => {
  it("says that the copy uses its database copy and gets its own address", async () => {
    const api = await signedIn("owner");
    const id = website("acme.example.com").id;
    render(<MemoryRouter initialEntries={[`/websites/${id}`]}><App /></MemoryRouter>);
    let made: Record<string, unknown> = {};
    api.change<{ data: Record<string, unknown>[] }>("/websites?perPage=100&page=1", (body) => { made = { ...structuredClone(body.data.find((w) => w.name === "acme.example.com")!), id: "copy", name: "staging.acme.example.com", status: "starting", stagingOf: id }; });
    api.on(`POST /websites/${id}/staging`, () => [202, {
      website: made, job: apiJob({ id: "job-copy", action: "CreateWebsite", websiteId: "copy" }),
      databaseCredentials: [{ engine: "mariadb", user: "staging_ab12_app", password: "Copy-secret-1234", databases: ["staging_ab12_wp"], host: "mariadb.internal", port: 3306 }],
      wordpress: { database: "staging_ab12_wp", user: "staging_ab12_app" },
    }]);
    fireEvent.click(await screen.findByRole("button", { name: "More actions for acme.example.com" }));
    fireEvent.click(screen.getByRole("menuitem", { name: "Make staging copy" }));
    const dialog = await screen.findByRole("dialog");
    expect(within(dialog).getByRole("status").textContent).toBe("WordPress in the copy uses the database copy staging_ab12_wp with the user staging_ab12_app: the panel writes them to wp-config.php. When the copy runs, the panel changes the site address to https://staging.acme.example.com and asks search engines not to index the copy.");
    expect(within(dialog).queryByText(/Put these users in the configuration/)).toBeNull();
    expect(api.changes.at(-1)).toEqual({ method: "POST", path: `/websites/${id}/staging`, body: undefined });
  });

  it("shows the progress of the copy, and an error of the copy", async () => {
    const api = await signedIn("owner");
    const id = website("acme.example.com").id;
    api.change<WordPressState>(`/websites/${id}/wordpress`, (body) => { body.wordpress!.status = "copying"; body.busy = "job-stage"; });
    render(<MemoryRouter initialEntries={[`/websites/${id}/wordpress`]}><App /></MemoryRouter>);
    expect(await screen.findByText(/The panel makes the WordPress copy\. It connects WordPress to the database copy/)).toBeTruthy();
    // One progress message: no second banner for the job.
    expect(screen.queryByText(/WordPress has a job on this website now/)).toBeNull();
    cleanup();

    api.change<WordPressState>(`/websites/${id}/wordpress`, (body) => { body.wordpress!.status = "ready"; body.busy = null; body.wordpress!.error = "The panel did not change the site address of the copy: Replace the site address: wp search-replace: Error: no connection"; });
    render(<MemoryRouter initialEntries={[`/websites/${id}/wordpress`]}><App /></MemoryRouter>);
    expect((await screen.findByText(/The panel did not change the site address of the copy/)).closest("[role=alert]")).toBeTruthy();
  });
});

describe("a new WordPress website", () => {
  it("sends the WordPress fields and shows the password of the administrator", async () => {
    const api = await signedIn("owner");
    render(<MemoryRouter initialEntries={["/websites/new"]}><App /></MemoryRouter>);
    fireEvent.change(await screen.findByLabelText("Primary domain"), { target: { value: "blog.acme.example.com" } });
    fireEvent.change(screen.getByLabelText("Site title"), { target: { value: "Acme blog" } });
    fireEvent.change(screen.getByLabelText("Administrator user name"), { target: { value: "olu" } });
    fireEvent.change(screen.getByLabelText("Administrator email"), { target: { value: "olu@acme.example.com" } });
    fireEvent.change(screen.getByLabelText("Language"), { target: { value: "de_DE" } });
    let made: Record<string, unknown> = {};
    api.change<{ data: Record<string, unknown>[] }>("/websites?perPage=100&page=1", (body) => { made = { ...structuredClone(body.data.find((w) => w.name === "acme.example.com")!), id: "new-site", name: "blog.acme.example.com", status: "starting" }; });
    api.on("POST /websites", () => [202, {
      website: made, job: apiJob({ id: "job-new", action: "CreateWebsite", websiteId: "new-site" }),
      wordpress: { admin: { user: "olu", password: "Secret-once-1234", loginUrl: "https://blog.acme.example.com/wp-login.php" } },
    }]);
    fireEvent.click(screen.getByRole("button", { name: "Create website" }));
    const dialog = await screen.findByRole("dialog");
    expect(within(dialog).getByLabelText("Administrator password").textContent).toBe("Secret-once-1234");
    expect(api.changes.at(-1)?.body).toMatchObject({ name: "blog.acme.example.com", application: "WordPress", wordpress: { title: "Acme blog", adminUser: "olu", adminEmail: "olu@acme.example.com", locale: "de_DE" } });
  });

  it("shows the API errors next to the WordPress fields, and sends no WordPress for a different application", async () => {
    const api = await signedIn("owner");
    render(<MemoryRouter initialEntries={["/websites/new"]}><App /></MemoryRouter>);
    fireEvent.change(await screen.findByLabelText("Primary domain"), { target: { value: "blog.acme.example.com" } });
    api.on("POST /websites", () => [422, { message: "x", errors: { "wordpress.adminUser": ["Do not use admin."] } }]);
    fireEvent.click(screen.getByRole("button", { name: "Create website" }));
    await waitFor(() => expect(screen.getByLabelText("Administrator user name").getAttribute("aria-invalid")).toBe("true"));

    fireEvent.change(screen.getByLabelText("Application"), { target: { value: "Laravel" } });
    expect(screen.queryByLabelText("Site title")).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "Create website" }));
    await waitFor(() => expect(api.changes.at(-1)?.body).not.toHaveProperty("wordpress"));
  });
});

describe("WP-CLI arguments", () => {
  it("splits a command line as a shell does, with no shell", () => {
    expect(wpCliArguments("wp plugin list --status=active")).toEqual(["plugin", "list", "--status=active"]);
    expect(wpCliArguments(`post create --post_title="A \\"quoted\\" title" --post_status=draft`)).toEqual(["post", "create", "--post_title=A \"quoted\" title", "--post_status=draft"]);
    expect(wpCliArguments("option update blogname 'It''s'")).toEqual(["option", "update", "blogname", "Its"]);
    expect(wpCliArguments("search-replace a\\ b c ''")).toEqual(["search-replace", "a b", "c", ""]);
    expect(() => wpCliArguments("option get 'blogname")).toThrow("A quote has no end.");
  });
});
