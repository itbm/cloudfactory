// @vitest-environment jsdom
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { getState } from "../api/store";
import { apiJob, reset, signedIn, website, type PanelApi } from "../test/panelApi";
import { DatabasesPanel } from "./DatabasesPanel";

let api: PanelApi;
// The Unix user of shop.acme.example.com in the sample data starts each name.
let prefix: string;
beforeEach(async () => {
  api = await signedIn("administrator");
  prefix = `${shop().unixUser}_`;
});
afterEach(() => {
  cleanup();
  reset();
});

const shop = () => website("shop.acme.example.com");
const panel = () => render(<MemoryRouter><DatabasesPanel websiteId={shop().id} /></MemoryRouter>);
const database = (suffix: string) => getState().databases.find((d) => d.name === prefix + suffix)!;
const row = (name: string, table: number) => within(screen.getAllByRole("table")[table]).getAllByRole("row").find((r) => r.textContent?.startsWith(name))!;

describe("Valkey in the databases panel", () => {
  it("shows the host, the database number and the keys of a Valkey database", () => {
    panel();
    const cache = row(`${prefix}cache`, 0);
    expect(cache.textContent).toContain("Valkey");
    expect(cache.textContent).toContain("valkey.internal:6379");
    expect(cache.textContent).toContain("Database number 1");
    expect(cache.textContent).toContain("18,412 keys");
    expect(row(`${prefix}shop`, 0).textContent).toContain("mariadb.internal:3306");
    expect(screen.getByText(/To connect to a Valkey database/)).toBeTruthy();
  });

  it("tells that the number comes later while the job runs", () => {
    database("cache").number = undefined;
    database("cache").keys = undefined;
    panel();
    expect(row(`${prefix}cache`, 0).textContent).toContain("The number comes when the job is complete");
    expect(row(`${prefix}cache`, 0).textContent).toContain("No report");
  });

  it("lets the user of a Valkey database only get a new password", () => {
    panel();
    const valkey = row(`${prefix}cache`, 1);
    // The actions of a row are in one menu (audit item U7).
    const actions = (r: HTMLElement) => {
      fireEvent.click(within(r).getByRole("button", { name: /^Actions for/ }));
      const items = within(r).getAllByRole("menuitem").map((b) => b.textContent);
      fireEvent.keyDown(document, { key: "Escape" });
      return items;
    };
    expect(actions(valkey)).toEqual(["Make a new password…"]);
    expect(within(valkey).queryByRole("combobox")).toBeNull();
    expect(valkey.textContent).toContain("Website container only");
    const maria = row(`${prefix}shop`, 1);
    expect(actions(maria)).toEqual(["Change permissions…", "Make a new password…", "Remove user…"]);
    expect(within(maria).getByRole("combobox")).toBeTruthy();
  });

  it("makes a Valkey database, with no option to make it with no user", async () => {
    panel();
    fireEvent.click(screen.getByRole("button", { name: "New database" }));
    const dialog = screen.getByRole("dialog");
    expect(within(dialog).getByRole("checkbox")).toBeTruthy();
    fireEvent.click(within(dialog).getByRole("radio", { name: /Valkey/ }));
    expect(within(dialog).queryByRole("checkbox")).toBeNull();
    expect(dialog.textContent).toContain("a user with the same name as the database");
    fireEvent.change(within(dialog).getByLabelText("Name"), { target: { value: "sessions" } });
    // The API makes the database and its user, and the agent gives the database number.
    const path = `/websites/${shop().id}/databases`;
    api.on(`POST ${path}`, (body) => {
      expect(body).toEqual({ engine: "valkey", name: "sessions", createUser: true });
      api.change<{ databases: object[]; users: object[] }>(path, (answer) => {
        answer.databases.push({ id: "db-new", engine: "valkey", name: `${prefix}sessions`, sizeBytes: 0, number: 3, keys: 0 });
        answer.users.push({ id: "user-new", engine: "valkey", name: `${prefix}sessions`, access: "website", grants: [{ databaseId: "db-new", database: `${prefix}sessions`, privileges: "readwrite" }] });
      });
      return [202, { password: "a-new-password", job: apiJob({ action: "CreateDatabase", websiteId: shop().id }) }];
    });
    fireEvent.click(within(dialog).getByRole("button", { name: "Create database" }));

    await waitFor(() => expect(screen.getByRole("dialog").textContent).toContain(`The password of ${prefix}sessions`));
    expect(screen.getByRole("dialog").textContent).toContain("the list of databases shows the database number");
    await waitFor(() => expect(row(`${prefix}sessions`, 0).textContent).toContain("Database number 3"));
    expect(row(`${prefix}sessions`, 1).textContent).toContain("Valkey");
  });

  it("has no Valkey for a new database user", () => {
    panel();
    fireEvent.click(screen.getByRole("button", { name: "New user" }));
    expect(within(screen.getByRole("dialog")).getAllByRole("radio").map((r) => r.closest("label")!.textContent)).toEqual(["MariaDBMariaDB 12.3", "PostgreSQLPostgreSQL 18"]);
  });

  it("does not offer Valkey when the agent of the server cannot operate it", () => {
    getState().servers.find((s) => s.id === shop().serverId)!.databaseEngines = ["mariadb", "postgresql"];
    panel();
    fireEvent.click(screen.getByRole("button", { name: "New database" }));
    const dialog = screen.getByRole("dialog");
    expect((within(dialog).getByRole("radio", { name: /Valkey/ }) as HTMLInputElement).disabled).toBe(true);
    expect(dialog.textContent).toContain("cannot operate Valkey. Update the agent of the server.");
  });
});

describe("Open in Adminer", () => {
  /** A window of the panel, as window.open gives it, with a document of its own. */
  const fakeWindow = () => {
    const doc = document.implementation.createHTMLDocument("");
    const submitted: { action: string; fields: Record<string, string> }[] = [];
    doc.createElement = ((create) => (tag: string) => {
      const el = create(tag);
      if (tag === "form") (el as HTMLFormElement).submit = () => submitted.push({ action: (el as HTMLFormElement).action, fields: Object.fromEntries([...el.querySelectorAll("input")].map((i) => [i.name, i.value])) });
      return el;
    })(doc.createElement.bind(doc));
    return { win: { document: doc, opener: {} as unknown, close: vi.fn() }, submitted };
  };

  it("signs in to Adminer with a temporary user, and puts no password on the screen", async () => {
    const { win, submitted } = fakeWindow();
    vi.spyOn(window, "open").mockReturnValue(win as unknown as Window);
    const db = database("shop");
    let checks = 0;
    const login = { id: "L1", url: "https://adminer.panel.example/01abc/", databaseName: db.name, username: `${prefix}adminer_k3x9q2mf`, expiresAt: "2026-11-15T03:00:00Z" };
    api.on(`POST /websites/${db.websiteId}/databases/${db.id}/adminer`, () => [202, {
      login: { ...login, status: "starting" },
      form: { driver: "server", server: "mariadb.internal:3306", username: login.username, password: "Secret12345678", db: db.name },
      jobs: [apiJob({ title: "Make Adminer user", action: "SetDatabaseUser" })],
    }]);
    api.on("GET /adminer-logins/L1", () => ({ login: { ...login, status: checks++ ? "ready" : "starting" } }));
    panel();
    fireEvent.click(within(row(`${prefix}shop`, 0)).getByRole("button", { name: "Open in Adminer" }));
    // A Valkey database has no Adminer.
    expect(within(row(`${prefix}cache`, 0)).queryByRole("button", { name: "Open in Adminer" })).toBeNull();
    // Adminer cannot change the page of the panel.
    expect(win.opener).toBeNull();
    await waitFor(() => expect(submitted).toHaveLength(1), { timeout: 5000 });
    expect(submitted[0]).toEqual({
      action: login.url,
      fields: { "auth[driver]": "server", "auth[server]": "mariadb.internal:3306", "auth[username]": login.username, "auth[password]": "Secret12345678", "auth[db]": db.name },
    });
    expect(document.body.textContent).not.toContain("Secret12345678");
    expect(api.changes.at(-1)).toMatchObject({ method: "POST", body: { privileges: "readwrite" } });
  });

  it("closes the window and tells why when Adminer cannot open the database", async () => {
    const { win } = fakeWindow();
    vi.spyOn(window, "open").mockReturnValue(win as unknown as Window);
    const db = database("shop");
    api.on(`POST /websites/${db.websiteId}/databases/${db.id}/adminer`, () => [422, { message: "The database server shop-db does not operate now.", errors: { database: ["The database server shop-db does not operate now."] } }]);
    panel();
    fireEvent.click(within(row(`${prefix}shop`, 0)).getByRole("button", { name: "Open in Adminer" }));
    await screen.findByText("Adminer did not open");
    expect(win.close).toHaveBeenCalled();
  });
});
