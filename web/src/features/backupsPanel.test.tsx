// @vitest-environment jsdom
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { afterEach, describe, expect, it } from "vitest";
import { getState } from "../api/store";
import { apiJob, reset, signedIn, website } from "../test/panelApi";
import { BackupsPanel } from "./BackupsPanel";

afterEach(() => {
  cleanup();
  reset();
});

// This website of the sample data is on web-2. One of its databases is in a database server on
// web-1, so each backup has a second snapshot with only that database.
describe("the backup of a website with a database on a different server", () => {
  const open = async () => {
    const api = await signedIn("administrator");
    const site = website("storefront.acme.example.com");
    render(<MemoryRouter><BackupsPanel websiteId={site.id} /></MemoryRouter>);
    return { api, site };
  };

  it("shows the snapshot with only the databases, and the server that has it", async () => {
    await open();
    const rows = screen.getAllByRole("row").map((r) => r.textContent ?? "");
    expect(rows.some((r) => r.includes("1 database on the server web-1"))).toBe(true);
    expect(rows.some((r) => r.endsWith("FilesRestore…"))).toBe(true);
  });

  it("restores only a database from that snapshot", async () => {
    const { api, site } = await open();
    const part = getState().snapshots.find((s) => s.websiteId === site.id && s.part === "databases")!;
    api.on(`POST /websites/${site.id}/snapshots/${part.id}/restore`, () => [202, { job: apiJob({ action: "Restore" }) }]);
    const row = screen.getAllByRole("row").find((r) => r.textContent?.includes("1 database on the server web-1"))!;
    fireEvent.click(within(row).getByRole("button", { name: "Restore…" }));
    const dialog = screen.getByRole("dialog");
    // The snapshot has no files, so the dialog offers only the database.
    expect(within(dialog).getAllByRole("radio").map((r) => r.parentElement?.textContent)).toEqual(["One database1 in this snapshot."]);
    fireEvent.change(within(dialog).getByLabelText("Database"), { target: { value: part.databases[0] } });
    fireEvent.click(within(dialog).getByRole("button", { name: "Restore" }));
    await waitFor(() => expect(api.changes.at(-1)).toMatchObject({ method: "POST", path: `/websites/${site.id}/snapshots/${part.id}/restore`, body: { scope: "database", item: part.databases[0] } }));
  });
});
