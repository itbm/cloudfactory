// @vitest-environment jsdom
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { getState, resetState, setContext, timing } from "../api/store";
import { DatabaseServersPanel } from "./DatabaseServersPanel";
import { DatabasesPanel } from "./DatabasesPanel";

beforeEach(() => {
  resetState();
  timing.stepMs = 0;
});
afterEach(cleanup);

const panel = () => render(<MemoryRouter><DatabaseServersPanel /></MemoryRouter>);
const row = (name: string) => screen.getAllByRole("row").find((r) => r.querySelector("strong")?.textContent === name)!;
const server = (name: string) => getState().databaseServers.find((s) => s.name === name)!;

describe("the database servers panel", () => {
  it("shows the plan, the limits, the use, the databases and the host of each database server", () => {
    panel();
    const maria = row("mariadb-1");
    expect(maria.textContent).toContain("MariaDB");
    expect(maria.textContent).toContain("Business");
    expect(maria.textContent).toContain("2 vCPU");
    expect(maria.textContent).toContain("612 MB of 1 GB memory");
    expect(maria.textContent).toContain("2.4 GB of 10 GB disk");
    expect(maria.textContent).toContain("3 of 50");
    expect(maria.textContent).toContain("mariadb.internal:3306");
    expect(maria.textContent).toContain("Running");
    // The second MariaDB server of the customer has the next port.
    expect(row("mariadb-reports").textContent).toContain("mariadb.internal:3307");
    expect(row("valkey-1").textContent).toContain("valkey.internal:6379");
  });

  it("shows only the limits for a database server that does not run, and the error of a failed one", () => {
    Object.assign(server("mariadb-reports"), { status: "failed", usage: undefined });
    getState().jobs.unshift({ id: "job_f", title: "Create database server mariadb-reports", state: "failed", steps: ["Start database server"], step: 0, error: "The server could not pull the image.", createdAt: new Date().toISOString(), databaseServerId: "dbs_4" });
    panel();
    const failed = row("mariadb-reports");
    expect(failed.textContent).toContain("512 MB memory");
    expect(failed.textContent).not.toContain(" of 512 MB");
    expect(failed.textContent).toContain("Failed");
    expect(failed.textContent).toContain("The server could not pull the image.");
    expect(within(failed).getAllByRole("button").map((b) => b.textContent)).toEqual(["Try again", "Change", "Remove"]);
    expect(within(row("mariadb-1")).getAllByRole("button").map((b) => b.textContent)).toEqual(["Change", "Remove"]);
  });

  it("makes a database server with the plan and the name that the user selects", async () => {
    panel();
    fireEvent.click(screen.getByRole("button", { name: "New database server" }));
    const dialog = screen.getByRole("dialog");
    expect(dialog.textContent).toContain("Acme Agency has 4 of 6 database servers.");
    // With no name, the panel gives the next free name of the engine.
    expect(dialog.textContent).toContain("the panel gives the name mariadb-2");
    fireEvent.click(within(dialog).getByRole("radio", { name: /PostgreSQL/ }));
    expect(dialog.textContent).toContain("the panel gives the name postgresql-2");
    fireEvent.change(within(dialog).getByLabelText("Name"), { target: { value: "reports" } });
    fireEvent.change(within(dialog).getByLabelText("Plan"), { target: { value: "plan_standard" } });
    fireEvent.click(within(dialog).getByRole("button", { name: "Create database server" }));

    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    await waitFor(() => expect(row("reports").textContent).toContain("Running"));
    expect(row("reports").textContent).toContain("postgres.internal:5433");
    expect(row("reports").textContent).toContain("Standard");
    expect(row("reports").textContent).toContain("0 of 10");
    expect(server("reports")).toMatchObject({ engine: "postgresql", planId: "plan_standard", limits: { memoryBytes: 512 * 1024 ** 2 } });
  });

  it("lets an administrator set limits that are different from the plan", async () => {
    panel();
    fireEvent.click(screen.getByRole("button", { name: "New database server" }));
    const dialog = screen.getByRole("dialog");
    expect(within(dialog).queryByLabelText("Memory (MB)")).toBeNull();
    fireEvent.click(within(dialog).getByRole("checkbox", { name: /Set limits/ }));
    // The fields start with the limits of the plan.
    expect((within(dialog).getByLabelText("Memory (MB)") as HTMLInputElement).value).toBe("1024");
    fireEvent.change(within(dialog).getByLabelText("Memory (MB)"), { target: { value: "100" } });
    fireEvent.click(within(dialog).getByRole("button", { name: "Create database server" }));
    await waitFor(() => expect(screen.getByRole("dialog").textContent).toContain("A database server needs 512 MB of memory or more."));
    fireEvent.change(within(dialog).getByLabelText("Memory (MB)"), { target: { value: "3072" } });
    fireEvent.click(within(dialog).getByRole("button", { name: "Create database server" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    expect(server("mariadb-2").limits).toEqual({ vcpus: 2, memoryBytes: 3 * 1024 ** 3, diskBytes: 10 * 1024 ** 3 });
  });

  it("tells when the plan of the customer allows no more database servers", async () => {
    getState().plans.find((p) => p.id === "plan_business")!.databaseServers = 4;
    panel();
    fireEvent.click(screen.getByRole("button", { name: "New database server" }));
    const dialog = screen.getByRole("dialog");
    expect(dialog.textContent).toContain("Acme Agency has 4 of 4 database servers. The plan of the customer allows no more.");
    fireEvent.click(within(dialog).getByRole("button", { name: "Create database server" }));
    await waitFor(() => expect(screen.getByRole("dialog").textContent).toContain("The Business plan allows 4 database servers."));
  });

  it("changes the plan of a database server, and tells that the database server restarts", async () => {
    timing.stepMs = 300;
    panel();
    fireEvent.click(within(row("mariadb-reports")).getByRole("button", { name: "Change" }));
    const dialog = screen.getByRole("dialog");
    expect(dialog.textContent).not.toContain("The panel restarts the database server");
    fireEvent.change(within(dialog).getByLabelText("Plan"), { target: { value: "plan_business" } });
    // A new plan gives its limits.
    expect((within(dialog).getByLabelText("Disk (GB)") as HTMLInputElement).value).toBe("10");
    expect(dialog.textContent).toContain("The panel restarts the database server to apply the new limits.");
    fireEvent.click(within(dialog).getByRole("button", { name: "Save changes" }));

    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    expect(server("mariadb-reports")).toMatchObject({ planId: "plan_business", limits: { vcpus: 2, memoryBytes: 1024 ** 3, diskBytes: 10 * 1024 ** 3 } });
    expect(row("mariadb-reports").textContent).toContain("Start database server…");
    await waitFor(() => expect(row("mariadb-reports").textContent).not.toContain("Start database server…"));
    expect(row("mariadb-reports").textContent).toContain("0 of 50");
  });

  it("removes only a database server with no databases", async () => {
    timing.stepMs = 150;
    panel();
    fireEvent.click(within(row("mariadb-1")).getByRole("button", { name: "Remove" }));
    expect(screen.getByRole("dialog").textContent).toContain("This database server has 3 databases. Remove the databases and the database users first.");
    expect(within(screen.getByRole("dialog")).queryByRole("button", { name: "Remove database server" })).toBeNull();
    fireEvent.click(within(screen.getByRole("dialog")).getAllByRole("button", { name: "Close" })[1]);

    fireEvent.click(within(row("mariadb-reports")).getByRole("button", { name: "Remove" }));
    expect(screen.getByRole("dialog").textContent).toContain("The panel stops the container and removes its data.");
    fireEvent.click(within(screen.getByRole("dialog")).getByRole("button", { name: "Remove database server" }));
    await waitFor(() => expect(row("mariadb-reports").textContent).toContain("Removing"));
    // The panel makes no change to a database server that it removes.
    expect(within(row("mariadb-reports")).queryAllByRole("button")).toEqual([]);
    await waitFor(() => expect(row("mariadb-reports")).toBeUndefined());
  });

  it("gives a website owner the plan of the customer, with no limits to set", () => {
    // The bakery is a customer of the reseller Acme. Its user is a website owner.
    Object.assign(getState().session.user, { role: "owner" });
    setContext("cus_bakery");
    panel();
    expect(screen.getByText(/Hill Street Bakery has 0 of 2 database servers/)).toBeTruthy();
    expect(screen.getByText(/No database servers. The panel makes the first database server of an engine when you make a database./)).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "New database server" }));
    const dialog = screen.getByRole("dialog");
    expect(within(dialog).queryByLabelText("Plan")).toBeNull();
    expect(within(dialog).queryByLabelText("Customer")).toBeNull();
    expect(within(dialog).queryByRole("checkbox")).toBeNull();
    expect(dialog.textContent).toContain("Plan: Starter. 1 vCPU · 512 MB memory · 2 GB disk · 2 databases.");
  });
});

describe("the database server of a new database", () => {
  const databases = () => render(<MemoryRouter><DatabasesPanel websiteId="web_shop" /></MemoryRouter>);
  const dbRow = (name: string) => within(screen.getAllByRole("table")[0]).getAllByRole("row").find((r) => r.textContent?.startsWith(name))!;

  it("lets the user select the database server, and shows the host of the database", async () => {
    databases();
    fireEvent.click(screen.getByRole("button", { name: "New database" }));
    const dialog = screen.getByRole("dialog");
    const select = within(dialog).getByLabelText("Database server") as HTMLSelectElement;
    expect([...select.options].map((o) => o.textContent)).toEqual(["Automatic: the first database server with space", "mariadb-1 · port 3306 · 3 of 50 databases", "mariadb-reports · port 3307 · 0 of 10 databases"]);
    fireEvent.change(select, { target: { value: "dbs_4" } });
    fireEvent.change(within(dialog).getByLabelText("Name"), { target: { value: "stats" } });
    fireEvent.click(within(dialog).getByRole("button", { name: "Create database" }));

    await waitFor(() => expect(dbRow("shop_7f2a_stats")).toBeTruthy());
    expect(dbRow("shop_7f2a_stats").textContent).toContain("mariadb.internal:3307");
    expect(dbRow("shop_7f2a_stats").textContent).toContain("mariadb-reports");
    expect(dbRow("shop_7f2a_wp").textContent).toContain("mariadb-1");
  });

  it("shows only the database servers of the engine, and does not offer a full one", () => {
    getState().databaseServers.find((s) => s.id === "dbs_3")!.maxDatabases = 1;
    databases();
    fireEvent.click(screen.getByRole("button", { name: "New database" }));
    const dialog = screen.getByRole("dialog");
    fireEvent.click(within(dialog).getByRole("radio", { name: /Valkey/ }));
    const select = within(dialog).getByLabelText("Database server") as HTMLSelectElement;
    expect([...select.options].map((o) => [o.textContent, o.disabled])).toEqual([["Automatic: the first database server with space", false], ["valkey-1 · port 6379 · 1 of 1 databases", true]]);
  });

  it("tells that the panel makes the first database server of an engine", () => {
    getState().databaseServers = getState().databaseServers.filter((s) => s.engine !== "postgresql");
    databases();
    fireEvent.click(screen.getByRole("button", { name: "New database" }));
    const dialog = screen.getByRole("dialog");
    fireEvent.click(within(dialog).getByRole("radio", { name: /PostgreSQL/ }));
    expect(dialog.textContent).toContain("The customer has no PostgreSQL database server on the server of this website. The panel makes one with the plan of the customer.");
    expect([...(within(dialog).getByLabelText("Database server") as HTMLSelectElement).options].map((o) => o.textContent)).toEqual(["New PostgreSQL database server"]);
  });

  it("has no database server for a website with the shared database containers of an earlier release", () => {
    getState().websites.find((w) => w.id === "web_shop")!.sharedDatabases = true;
    databases();
    fireEvent.click(screen.getByRole("button", { name: "New database" }));
    expect(within(screen.getByRole("dialog")).queryByLabelText("Database server")).toBeNull();
  });

  it("gives a new user the databases of one database server", async () => {
    getState().databases.push({ id: "db_9", name: "shop_7f2a_stats", engine: "mariadb", websiteId: "web_shop", sizeBytes: 0, serverId: "srv_web01", databaseServerId: "dbs_4", host: "mariadb.internal", port: 3307 });
    databases();
    fireEvent.click(screen.getByRole("button", { name: "New user" }));
    const dialog = screen.getByRole("dialog");
    const select = within(dialog).getByLabelText("Database server") as HTMLSelectElement;
    expect([...select.options].map((o) => o.textContent)).toEqual(["mariadb-1 · port 3306", "mariadb-reports · port 3307"]);
    expect(within(dialog).getByLabelText("shop_7f2a_wp")).toBeTruthy();
    expect(within(dialog).queryByLabelText("shop_7f2a_stats")).toBeNull();
    fireEvent.change(select, { target: { value: "dbs_4" } });
    expect(within(dialog).queryByLabelText("shop_7f2a_wp")).toBeNull();
    fireEvent.change(within(dialog).getByLabelText("shop_7f2a_stats"), { target: { value: "readonly" } });
    fireEvent.change(within(dialog).getByLabelText("Name"), { target: { value: "reader" } });
    fireEvent.click(within(dialog).getByRole("button", { name: "Create user" }));
    await waitFor(() => expect(getState().databaseUsers.find((u) => u.name === "shop_7f2a_reader")).toMatchObject({ databaseServerId: "dbs_4", grants: [{ database: "shop_7f2a_stats", privileges: "readonly" }] }));
    // PostgreSQL has one database server, so the dialog has no select.
    cleanup();
    databases();
    fireEvent.click(screen.getByRole("button", { name: "New user" }));
    fireEvent.click(within(screen.getByRole("dialog")).getByRole("radio", { name: /PostgreSQL/ }));
    expect(within(screen.getByRole("dialog")).queryByLabelText("Database server")).toBeNull();
  });
});
