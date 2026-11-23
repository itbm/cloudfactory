// @vitest-environment jsdom
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";
import { getState, signIn } from "../api/store";
import type { AccountExport } from "../api/types";
import { EMAIL, panelApi, reset } from "../test/panelApi";
import owner from "../test/fixtures/owner.json";
import reseller from "../test/fixtures/reseller.json";
import { AccountPage } from "../pages/AccountPage";
import { CustomersPage } from "../pages/CustomersPage";

afterEach(() => {
  cleanup();
  reset();
  vi.restoreAllMocks();
});

const exportsCard = () => screen.getByRole("heading", { name: "Account export" }).closest(".card") as HTMLElement;

// The tests give the exports themselves, so that they do not depend on the exports of the
// fixtures. The IDs of the customers come from the fixtures.
const customerId = (fixture: unknown, name: string) =>
  (fixture as Record<string, { body: { data: { id: string; name: string }[] } }>)["GET /customers"].body.data.find((c) => c.name === name)!.id;
const acme = customerId(owner, "Acme Ltd");
const books = customerId(reseller, "Harbour Books");

const anExport = (over: Partial<AccountExport>): AccountExport => ({
  id: "E1", customerId: acme, customerName: "Acme Ltd", state: "ready", files: true, databases: true,
  parts: { complete: 12, total: 12 }, receivedBytes: 6318, sizeBytes: 61952, warnings: [], error: null, createdBy: "Olu Owner",
  createdAt: "2026-11-20T16:40:00+00:00", finishedAt: "2026-11-20T16:43:00+00:00", expiresAt: "2026-11-27T16:43:00+00:00", ...over,
});

const OWNER_EXPORTS = [
  anExport({ id: "E1" }),
  anExport({ id: "E2", state: "failed", sizeBytes: null, expiresAt: null, createdAt: "2026-11-17T09:00:00+00:00", error: "The server could not make the dump of shopacme: mariadb-dump: Lost connection to server." }),
  // The export of a different account. The page of the owner does not show it.
  anExport({ id: "E3", customerId: "other", customerName: "Other Ltd", createdBy: "Somebody Else" }),
];

/** Signs in as the sample user of the role. The API gives these exports. */
async function signedInWithExports(role: "owner" | "reseller", exports: AccountExport[]) {
  const api = panelApi(role);
  api.on("GET /exports", () => ({ data: exports }));
  await signIn(EMAIL[role], "password");
  return api;
}

describe("the account export of an owner", () => {
  it("lists the exports of the account with their state", async () => {
    await signedInWithExports("owner", OWNER_EXPORTS);
    render(<MemoryRouter><AccountPage /></MemoryRouter>);
    const rows = within(exportsCard()).getAllByRole("row").slice(1).map((r) => r.textContent ?? "");
    expect(rows).toHaveLength(2);
    expect(rows[0]).toContain("Ready");
    expect(rows[0]).toContain("Olu Owner");
    expect(rows[0]).toContain("Settings, files and databases");
    expect(rows[1]).toContain("Failed");
    expect(rows[1]).toContain("mariadb-dump");
    // The export of a different account is not on the page.
    expect(rows.join(" ")).not.toContain("Somebody Else");
  });

  it("starts an export without the files", async () => {
    const api = await signedInWithExports("owner", OWNER_EXPORTS);
    api.on("POST /exports", () => [202, { ...getState().exports[0], id: "E9", state: "running", sizeBytes: null }]);
    render(<MemoryRouter><AccountPage /></MemoryRouter>);
    fireEvent.click(within(exportsCard()).getByRole("button", { name: "Export account" }));
    const dialog = screen.getByRole("dialog");
    fireEvent.click(within(dialog).getByLabelText(/The files of the websites/));
    fireEvent.click(within(dialog).getByRole("button", { name: "Start export" }));
    // The owner exports its own account: the request names no customer.
    await waitFor(() => expect(api.changes.at(-1)).toEqual({ method: "POST", path: "/exports", body: { files: false, databases: true } }));
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
  });

  it("downloads the archive with a link from the API", async () => {
    const api = await signedInWithExports("owner", OWNER_EXPORTS);
    const ready = getState().exports.find((e) => e.state === "ready")!;
    api.on(`POST /exports/${ready.id}/download-link`, () => [200, { url: `/api/v1/export-downloads/${ready.id}?expires=1&signature=abc`, fileName: "acme-ltd-export.tar", sizeBytes: ready.sizeBytes, expiresAt: "2026-11-14T10:10:00Z" }]);
    const clicked: { href: string; download: string }[] = [];
    vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(function (this: HTMLAnchorElement) {
      clicked.push({ href: this.getAttribute("href") ?? "", download: this.download });
    });
    render(<MemoryRouter><AccountPage /></MemoryRouter>);
    fireEvent.click(within(exportsCard()).getByRole("button", { name: "Download" }));
    await waitFor(() => expect(clicked).toEqual([{ href: `/api/v1/export-downloads/${ready.id}?expires=1&signature=abc`, download: "acme-ltd-export.tar" }]));
  });

  it("removes an export after a confirmation", async () => {
    const api = await signedInWithExports("owner", OWNER_EXPORTS);
    const failed = getState().exports.find((e) => e.state === "failed")!;
    api.on(`DELETE /exports/${failed.id}`, () => [204, null]);
    render(<MemoryRouter><AccountPage /></MemoryRouter>);
    const row = within(exportsCard()).getAllByRole("row").find((r) => r.textContent?.includes("Failed"))!;
    fireEvent.click(within(row).getByRole("button", { name: "Remove" }));
    fireEvent.click(within(screen.getByRole("dialog")).getByRole("button", { name: "Remove" }));
    await waitFor(() => expect(api.changes.at(-1)).toMatchObject({ method: "DELETE", path: `/exports/${failed.id}` }));
  });
});

describe("the account export of a customer of a reseller", () => {
  it("exports the account of the customer from the list of customers", async () => {
    const api = await signedInWithExports("reseller", [anExport({ id: "E4", customerId: books, customerName: "Harbour Books", createdBy: "Sam Reseller" })]);
    api.on("POST /exports", () => [202, { ...getState().exports[0], state: "running" }]);
    render(<MemoryRouter initialEntries={["/customers"]}><CustomersPage /></MemoryRouter>);
    const row = screen.getAllByRole("row").find((r) => r.textContent?.includes("Harbour Books"))!;
    fireEvent.click(within(row).getByRole("button", { name: "Actions for Harbour Books" }));
    fireEvent.click(screen.getByRole("menuitem", { name: "Export account…" }));
    const dialog = screen.getByRole("dialog", { name: "Account export: Harbour Books" });
    expect(within(dialog).getAllByRole("row").slice(1).map((r) => r.textContent ?? "")[0]).toContain("Sam Reseller");
    fireEvent.click(within(dialog).getByRole("button", { name: "Export account" }));
    fireEvent.click(within(screen.getByRole("dialog", { name: "Export Harbour Books" })).getByRole("button", { name: "Start export" }));
    await waitFor(() => expect(api.changes.at(-1)).toEqual({ method: "POST", path: "/exports", body: { customerId: books, files: true, databases: true } }));
  });
});
