// @vitest-environment jsdom
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { afterEach, describe, expect, it } from "vitest";
import { getState } from "../api/store";
import type { Role } from "../api/types";
import { apiJob, reset, signedIn, website, type PanelApi } from "../test/panelApi";
import { DatabaseServersPanel } from "./DatabaseServersPanel";
import { DatabasesPanel } from "./DatabasesPanel";

afterEach(() => {
  cleanup();
  reset();
});

// The owner of the sample data is a user of Acme Ltd. Acme has five database servers on web-1, one of them of the pack engine MySQL.
const open = async (role: Role): Promise<PanelApi> => {
  const api = await signedIn(role);
  render(<MemoryRouter><DatabaseServersPanel /></MemoryRouter>);
  return api;
};
const acme = () => getState().customers.find((c) => c.name === "Acme Ltd")!;
const server = (name: string) => getState().databaseServers.find((s) => s.name === name && s.customerId === acme().id)!;
const row = (name: string) => screen.getAllByRole("row").find((r) => r.querySelector("strong")?.textContent === name && (r.textContent?.includes("Acme Ltd") || getState().session.user.role === "owner"))!;
const actions = (r: HTMLElement) => {
  fireEvent.click(within(r).getByRole("button", { name: /^Actions for/ }));
  return within(r).getAllByRole("menuitem");
};
const answer = (name: string, over: Record<string, unknown> = {}) => ({ ...server(name), usage: server(name).usage ?? null, ...over });

describe("the database servers panel", () => {
  it("shows the plan, the limits, the use, the databases and the host of each database server", async () => {
    await open("owner");
    expect(screen.getByText(/Acme Ltd has 5 of 6 database servers/)).toBeTruthy();
    const maria = row("mariadb-1");
    expect(maria.textContent).toContain("MariaDB");
    expect(maria.textContent).toContain("Business");
    expect(maria.textContent).toContain("2 vCPU");
    expect(maria.textContent).toContain("of 1 GB memory");
    expect(maria.textContent).toContain("of 10 GB disk");
    expect(maria.textContent).toContain("2 of 20");
    expect(maria.textContent).toContain("mariadb.internal:3306");
    expect(maria.textContent).toContain("Running");
    // The second MariaDB server of the customer has the next port.
    expect(row("mariadb-reports").textContent).toContain("mariadb.internal:3307");
    // The network name is first: it is the same for each website of the customer, on each
    // server. It has the usual port of the engine, also for a second database server.
    expect(row("mariadb-reports").textContent).toContain("mariadb-reports.internal:3306");
    expect(row("mariadb-reports").textContent).toMatch(/Also mariadb\.internal:3307 on /);
    expect(row("valkey-1").textContent).toContain("valkey.internal:6379");
    // An owner cannot read the plans of other customers, so the panel has no name for this plan.
    expect(row("mariadb-reports").textContent).toContain("Other plan");
  });

  it("shows the version and the network name of a database server of a pack engine, with no move", async () => {
    await open("administrator");
    const mysql = row("mysql-1");
    expect(mysql.textContent).toContain("MySQL 8.4");
    expect(mysql.textContent).toContain("mysql-1.internal:3306");
    // A database server of a pack has only its network name.
    expect(mysql.textContent).not.toContain("Also");
    expect(actions(mysql).map((b) => b.textContent)).toEqual(["Change", "Remove"]);
    expect(actions(row("mariadb-1")).map((b) => b.textContent)).toContain("Move to a different server");
  });

  it("makes a database server of a pack engine with the version that the user selects", async () => {
    const api = await open("administrator");
    api.on("POST /database-servers", () => [202, {
      databaseServer: answer("mysql-1", { id: "new-mysql", name: "mysql-2", engineVersion: "8.0", port: 3306, status: "starting", usage: null, databases: 0 }),
      job: apiJob({ id: "job-mysql", title: "Create database server mysql-2", action: "SetDatabaseServer", databaseServerId: "new-mysql" }),
    }]);
    fireEvent.click(screen.getByRole("button", { name: "New database server" }));
    const dialog = screen.getByRole("dialog");
    fireEvent.change(within(dialog).getByLabelText("Customer"), { target: { value: acme().id } });
    expect(within(dialog).getByRole("radio", { name: /MySQL/ }).closest("label")!.textContent).toContain("From a pack of Example Packs Ltd");
    // A built-in engine has no version to select.
    expect(within(dialog).queryByLabelText("Version")).toBeNull();
    fireEvent.click(within(dialog).getByRole("radio", { name: /MySQL/ }));
    expect(dialog.textContent).toContain("the panel gives the name mysql-2");
    const version = within(dialog).getByLabelText("Version") as HTMLSelectElement;
    expect([...version.options].map((o) => o.textContent)).toEqual(["MySQL 8.4 · support until 30 April 2032", "MySQL 8.0 · support ended 30 April 2026"]);
    fireEvent.change(version, { target: { value: "8.0" } });
    fireEvent.click(within(dialog).getByRole("button", { name: "Create database server" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    expect(api.changes.at(-1)).toMatchObject({ method: "POST", path: "/database-servers", body: { engine: "mysql", customerId: acme().id, version: "8.0" } });
  });

  it("shows only the limits of a failed database server, with the cause for an administrator only", async () => {
    const failed = (role: Role) => async () => {
      const api = await signedIn(role);
      Object.assign(server("mariadb-reports"), { status: "failed", usage: undefined });
      getState().jobs.unshift({ id: "01HJOBFAILED00000000000001", title: "Create database server mariadb-reports", state: "failed", steps: ["Start database server"], step: 0, error: "podman pull: the registry gave status 503", createdAt: new Date().toISOString(), databaseServerId: server("mariadb-reports").id });
      render(<MemoryRouter><DatabaseServersPanel /></MemoryRouter>);
      return api;
    };
    await failed("administrator")();
    expect(row("mariadb-reports").textContent).toContain("512 MB memory");
    expect(row("mariadb-reports").textContent).not.toContain(" of 512 MB");
    expect(row("mariadb-reports").textContent).toContain("podman pull: the registry gave status 503");
    expect(actions(row("mariadb-reports")).map((b) => b.textContent)).toEqual(["Try again", "Change", "Remove"]);
    cleanup();
    reset();
    // Audit item U2: an owner gets a plain text and a reference, not the error of the server.
    await failed("owner")();
    expect(row("mariadb-reports").textContent).toContain("Failed");
    expect(row("mariadb-reports").textContent).toContain("Reference J-");
    expect(row("mariadb-reports").textContent).not.toContain("podman");
  });

  it("makes a database server with the engine, the name and the plan that the user selects", async () => {
    const api = await open("administrator");
    const standard = getState().plans.find((p) => p.name === "Starter")!;
    api.on("POST /database-servers", () => [202, {
      databaseServer: answer("mariadb-1", { id: "new-server", name: "reports", engine: "postgresql", port: 5433, host: "postgres.internal", planId: standard.id, status: "starting", usage: null, databases: 0, maxDatabases: standard.databases, limits: standard.databaseLimits }),
      job: apiJob({ id: "job-dbs", title: "Create database server reports", action: "SetDatabaseServer", databaseServerId: "new-server" }),
    }]);
    fireEvent.click(screen.getByRole("button", { name: "New database server" }));
    const dialog = screen.getByRole("dialog");
    fireEvent.change(within(dialog).getByLabelText("Customer"), { target: { value: acme().id } });
    expect(dialog.textContent).toContain("Acme Ltd has 5 of 6 database servers.");
    // With no name, the panel gives the next free name of the engine.
    expect(dialog.textContent).toContain("the panel gives the name mariadb-2");
    fireEvent.click(within(dialog).getByRole("radio", { name: /PostgreSQL/ }));
    expect(dialog.textContent).toContain("the panel gives the name postgresql-2");
    fireEvent.change(within(dialog).getByLabelText("Name"), { target: { value: "reports" } });
    fireEvent.change(within(dialog).getByLabelText("Plan"), { target: { value: standard.id } });
    fireEvent.click(within(dialog).getByRole("button", { name: "Create database server" }));

    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    expect(api.changes.at(-1)).toEqual({ method: "POST", path: "/database-servers", body: { engine: "postgresql", customerId: acme().id, name: "reports", planId: standard.id } });
  });

  it("lets an administrator set limits that are different from the plan, and shows the error of the API", async () => {
    const api = await open("administrator");
    api.on("POST /database-servers", () => [422, { message: "A database server needs 512 MB of memory or more.", errors: { "limits.memoryBytes": ["A database server needs 512 MB of memory or more."] } }]);
    fireEvent.click(screen.getByRole("button", { name: "New database server" }));
    const dialog = screen.getByRole("dialog");
    fireEvent.change(within(dialog).getByLabelText("Customer"), { target: { value: acme().id } });
    expect(within(dialog).queryByLabelText("Memory (MB)")).toBeNull();
    fireEvent.click(within(dialog).getByRole("checkbox", { name: /Set limits/ }));
    // The fields start with the limits of the plan.
    expect((within(dialog).getByLabelText("Memory (MB)") as HTMLInputElement).value).toBe("1024");
    fireEvent.change(within(dialog).getByLabelText("Memory (MB)"), { target: { value: "100" } });
    fireEvent.click(within(dialog).getByRole("button", { name: "Create database server" }));
    await waitFor(() => expect(screen.getByRole("dialog").textContent).toContain("A database server needs 512 MB of memory or more."));
    expect(api.changes.at(-1)!.body).toMatchObject({ limits: { vcpus: 2, memoryBytes: 100 * 1024 ** 2, diskBytes: 10 * 1024 ** 3 } });
  });

  it("changes the plan of a database server, and tells that the database server restarts", async () => {
    const api = await open("administrator");
    const business = getState().plans.find((p) => p.name === "Business")!;
    const id = server("mariadb-reports").id;
    api.on(`PATCH /database-servers/${id}`, () => [200, { databaseServer: answer("mariadb-reports", { planId: business.id, limits: business.databaseLimits, maxDatabases: business.databases }), job: apiJob({ action: "SetDatabaseServer", databaseServerId: id }) }]);
    fireEvent.click(actions(row("mariadb-reports")).find((b) => b.textContent === "Change")!);
    const dialog = screen.getByRole("dialog");
    expect(dialog.textContent).not.toContain("The panel restarts the database server");
    fireEvent.change(within(dialog).getByLabelText("Plan"), { target: { value: business.id } });
    // A new plan gives its limits.
    expect((within(dialog).getByLabelText("Disk (GB)") as HTMLInputElement).value).toBe("10");
    expect(dialog.textContent).toContain("The panel restarts the database server to apply the new limits.");
    fireEvent.click(within(dialog).getByRole("button", { name: "Save changes" }));

    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    expect(api.changes.at(-1)).toEqual({ method: "PATCH", path: `/database-servers/${id}`, body: { planId: business.id, limits: business.databaseLimits } });
  });

  it("removes only a database server with no databases", async () => {
    const api = await open("owner");
    fireEvent.click(actions(row("mariadb-1")).find((b) => b.textContent === "Remove")!);
    expect(screen.getByRole("dialog").textContent).toContain("This database server has 2 databases. Remove the databases and the database users first.");
    expect(within(screen.getByRole("dialog")).queryByRole("button", { name: "Remove database server" })).toBeNull();
    fireEvent.click(within(screen.getByRole("dialog")).getAllByRole("button", { name: "Close" }).at(-1)!);

    // The second MariaDB server has one database. This test removes a database server with none.
    cleanup();
    server("mariadb-reports").databases = 0;
    render(<MemoryRouter><DatabaseServersPanel /></MemoryRouter>);
    const id = server("mariadb-reports").id;
    api.on(`DELETE /database-servers/${id}`, () => [202, { job: apiJob({ action: "DeleteDatabaseServer", databaseServerId: id }) }]);
    fireEvent.click(actions(row("mariadb-reports")).find((b) => b.textContent === "Remove")!);
    expect(screen.getByRole("dialog").textContent).toContain("The panel stops the container and removes its data.");
    fireEvent.click(within(screen.getByRole("dialog")).getByRole("button", { name: "Remove database server" }));
    await waitFor(() => expect(api.changes.at(-1)).toMatchObject({ method: "DELETE", path: `/database-servers/${id}` }));
  });

  it("gives a website owner the plan of the customer, with no limits to set", async () => {
    await open("owner");
    fireEvent.click(screen.getByRole("button", { name: "New database server" }));
    const dialog = screen.getByRole("dialog");
    expect(within(dialog).queryByLabelText("Plan")).toBeNull();
    expect(within(dialog).queryByLabelText("Customer")).toBeNull();
    expect(within(dialog).queryByRole("checkbox")).toBeNull();
    expect(dialog.textContent).toContain("Plan: Business. 2 vCPU · 1 GB memory · 10 GB disk · 20 databases.");
  });
});

describe("the database server of a new database", () => {
  const shop = () => website("shop.acme.example.com");
  const databases = async () => {
    const api = await signedIn("owner");
    render(<MemoryRouter><DatabasesPanel websiteId={shop().id} /></MemoryRouter>);
    return api;
  };

  it("lets the user select the database server, and sends it to the API", async () => {
    const api = await databases();
    api.on("POST /websites/*/databases", () => [202, { password: "secret-password", job: apiJob({ action: "CreateDatabase" }) }]);
    fireEvent.click(screen.getByRole("button", { name: "New database" }));
    const dialog = screen.getByRole("dialog");
    const select = within(dialog).getByLabelText("Database server") as HTMLSelectElement;
    expect([...select.options].map((o) => o.textContent)).toEqual(["Automatic: the first database server with space", "mariadb-1 · port 3306 · 2 of 20 databases", "mariadb-reports · port 3307 · 1 of 5 databases"]);
    fireEvent.change(select, { target: { value: server("mariadb-reports").id } });
    fireEvent.change(within(dialog).getByLabelText("Name"), { target: { value: "stats" } });
    fireEvent.click(within(dialog).getByRole("button", { name: "Create database" }));
    await waitFor(() => expect(api.changes.at(-1)).toMatchObject({ method: "POST", body: { engine: "mariadb", name: "stats", databaseServerId: server("mariadb-reports").id } }));
  });

  it("offers the database servers on a different server, with the network name for the app", async () => {
    await signedIn("administrator");
    // This website of Acme is on web-2. The database servers of Acme are on web-1.
    const storefront = website("storefront.acme.example.com");
    render(<MemoryRouter><DatabasesPanel websiteId={storefront.id} /></MemoryRouter>);
    // Its database is in a database server on web-1: the app connects to the network name,
    // on the usual port of the engine.
    const database = screen.getAllByRole("row").find((r) => r.textContent?.includes("_orders"))!;
    expect(database.textContent).toContain("mariadb-reports.internal:3306");
    fireEvent.click(screen.getByRole("button", { name: "New database" }));
    const select = within(screen.getByRole("dialog")).getByLabelText("Database server") as HTMLSelectElement;
    expect([...select.options].map((o) => o.textContent)).toEqual(["Automatic: the first database server with space", "mariadb-1 · on the server web-1 · 2 of 20 databases", "mariadb-reports · on the server web-1 · 1 of 5 databases"]);
  });

  it("shows only the database servers of the engine, and does not offer a full one", async () => {
    await databases();
    server("valkey-1").maxDatabases = 2;
    fireEvent.click(screen.getByRole("button", { name: "New database" }));
    const dialog = screen.getByRole("dialog");
    fireEvent.click(within(dialog).getByRole("radio", { name: /Valkey/ }));
    const select = within(dialog).getByLabelText("Database server") as HTMLSelectElement;
    expect([...select.options].map((o) => [o.textContent, o.disabled])).toEqual([["Automatic: the first database server with space", false], ["valkey-1 · port 6379 · 2 of 2 databases", true]]);
  });

  it("offers a database server for each new database", async () => {
    await databases();
    fireEvent.click(screen.getByRole("button", { name: "New database" }));
    expect(within(screen.getByRole("dialog")).getByLabelText("Database server")).toBeTruthy();
  });
  it("moves a database server to a different server for an administrator only", async () => {
    await open("owner");
    expect(actions(row("mariadb-reports")).map((b) => b.textContent)).not.toContain("Move to a different server");
    cleanup();
    reset();

    const api = await open("administrator");
    const id = server("mariadb-reports").id;
    const web2 = getState().servers.find((s) => s.name === "web-2")!;
    api.on(`POST /database-servers/${id}/move`, () => [202, {
      databaseServer: answer("mariadb-reports", { status: "moving", movingToServerId: web2.id }),
      jobs: [apiJob({ action: "ReceiveDatabaseServer", databaseServerId: id }), apiJob({ action: "SendDatabaseServer", databaseServerId: id })],
    }]);
    fireEvent.click(actions(row("mariadb-reports")).find((b) => b.textContent === "Move to a different server")!);
    const dialog = screen.getByRole("dialog");
    // The list has the other servers with the web role. A healthy server is first.
    const options = within(dialog).getAllByRole("option").map((o) => o.textContent);
    expect(options).not.toContain("web-1");
    expect((within(dialog).getByLabelText("New server") as HTMLSelectElement).value).toBe(web2.id);
    // The user reads that an app must use the network name.
    expect(dialog.textContent).toContain("mariadb-reports.internal:3306");
    expect(dialog.textContent).toContain("An app that uses mariadb.internal:3307 loses the connection.");
    fireEvent.click(within(dialog).getByRole("button", { name: "Move database server" }));

    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    expect(api.changes.at(-1)).toEqual({ method: "POST", path: `/database-servers/${id}/move`, body: { serverId: web2.id } });
  });

  it("shows a database server that moves, with no actions", async () => {
    await signedIn("administrator");
    const web2 = getState().servers.find((s) => s.name === "web-2")!;
    Object.assign(server("mariadb-reports"), { status: "moving", movingToServerId: web2.id });
    render(<MemoryRouter><DatabaseServersPanel /></MemoryRouter>);
    expect(row("mariadb-reports").textContent).toContain("Moving");
    expect(row("mariadb-reports").textContent).toContain("To web-2");
    expect(within(row("mariadb-reports")).queryByRole("button", { name: /^Actions for/ })).toBeNull();
  });
});
