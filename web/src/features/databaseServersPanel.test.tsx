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

// The owner of the sample data is a user of Acme Ltd. Acme has four database servers on web-1.
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
    expect(screen.getByText(/Acme Ltd has 4 of 6 database servers/)).toBeTruthy();
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
    expect(row("valkey-1").textContent).toContain("valkey.internal:6379");
    // An owner cannot read the plans of other customers, so the panel has no name for this plan.
    expect(row("mariadb-reports").textContent).toContain("Other plan");
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
    expect(dialog.textContent).toContain("Acme Ltd has 4 of 6 database servers.");
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
    expect([...select.options].map((o) => o.textContent)).toEqual(["Automatic: the first database server with space", "mariadb-1 · port 3306 · 2 of 20 databases", "mariadb-reports · port 3307 · 0 of 5 databases"]);
    fireEvent.change(select, { target: { value: server("mariadb-reports").id } });
    fireEvent.change(within(dialog).getByLabelText("Name"), { target: { value: "stats" } });
    fireEvent.click(within(dialog).getByRole("button", { name: "Create database" }));
    await waitFor(() => expect(api.changes.at(-1)).toMatchObject({ method: "POST", body: { engine: "mariadb", name: "stats", databaseServerId: server("mariadb-reports").id } }));
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

  it("has no database server for a website with the shared database containers of an earlier release", async () => {
    await databases();
    shop().sharedDatabases = true;
    fireEvent.click(screen.getByRole("button", { name: "New database" }));
    expect(within(screen.getByRole("dialog")).queryByLabelText("Database server")).toBeNull();
  });
});
