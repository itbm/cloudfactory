// @vitest-environment jsdom
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { getState, resetState, timing } from "../api/store";
import { DatabasesPanel } from "./DatabasesPanel";

beforeEach(() => {
  resetState();
  timing.stepMs = 0;
});
afterEach(cleanup);

const panel = () => render(<MemoryRouter><DatabasesPanel websiteId="web_shop" /></MemoryRouter>);
const row = (name: string, table: number) => within(screen.getAllByRole("table")[table]).getAllByRole("row").find((r) => r.textContent?.startsWith(name))!;

describe("Valkey in the databases panel", () => {
  it("shows the host, the database number and the keys of a Valkey database", () => {
    panel();
    const cache = row("shop_7f2a_cache", 0);
    expect(cache.textContent).toContain("Valkey");
    expect(cache.textContent).toContain("valkey.internal:6379");
    expect(cache.textContent).toContain("Database number 1");
    expect(cache.textContent).toContain("18,412 keys");
    expect(row("shop_7f2a_wp", 0).textContent).toContain("mariadb.internal:3306");
    expect(screen.getByText(/To connect to a Valkey database/)).toBeTruthy();
  });

  it("tells that the number comes later while the job runs", () => {
    getState().databases.find((d) => d.id === "db_6")!.number = undefined;
    getState().databases.find((d) => d.id === "db_6")!.keys = undefined;
    panel();
    expect(row("shop_7f2a_cache", 0).textContent).toContain("The number comes when the job is complete");
    expect(row("shop_7f2a_cache", 0).textContent).toContain("No report");
  });

  it("lets the user of a Valkey database only get a new password", () => {
    panel();
    const valkey = row("shop_7f2a_cache", 1);
    expect(within(valkey).getAllByRole("button").map((b) => b.textContent)).toEqual(["New password"]);
    expect(within(valkey).queryByRole("combobox")).toBeNull();
    expect(valkey.textContent).toContain("Website container only");
    const maria = row("shop_7f2a_wp", 1);
    expect(within(maria).getAllByRole("button").map((b) => b.textContent)).toEqual(["Permissions", "New password", "Remove"]);
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
    fireEvent.click(within(dialog).getByRole("button", { name: "Create database" }));

    await waitFor(() => expect(screen.getByRole("dialog").textContent).toContain("The password of shop_7f2a_sessions"));
    expect(screen.getByRole("dialog").textContent).toContain("the list of databases shows the database number");
    await waitFor(() => expect(row("shop_7f2a_sessions", 0).textContent).toContain("Database number 2"));
    expect(row("shop_7f2a_sessions", 1).textContent).toContain("Valkey");
  });

  it("has no Valkey for a new database user", () => {
    panel();
    fireEvent.click(screen.getByRole("button", { name: "New user" }));
    expect(within(screen.getByRole("dialog")).getAllByRole("radio").map((r) => r.closest("label")!.textContent)).toEqual(["MariaDBMariaDB 12.3", "PostgreSQLPostgreSQL 18"]);
  });

  it("does not offer Valkey when the agent of the server cannot operate it", () => {
    const shop = getState().websites.find((w) => w.id === "web_shop")!;
    getState().servers.find((s) => s.id === shop.serverId)!.databaseEngines = ["mariadb", "postgresql"];
    panel();
    fireEvent.click(screen.getByRole("button", { name: "New database" }));
    const dialog = screen.getByRole("dialog");
    expect((within(dialog).getByRole("radio", { name: /Valkey/ }) as HTMLInputElement).disabled).toBe(true);
    expect(dialog.textContent).toContain("cannot operate Valkey. Update the agent of the server.");
  });
});
