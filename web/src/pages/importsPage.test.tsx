// @vitest-environment jsdom
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { afterEach, describe, expect, it } from "vitest";
import { App } from "../App";
import { CHUNK_BYTES, uploadArchive } from "../api/imports";
import { NAVIGATION, canOpen } from "../lib/roles";
import { reset, signedIn } from "../test/panelApi";
import administrator from "../test/fixtures/administrator.json";

afterEach(() => {
  cleanup();
  reset();
});

/** The sample data has a cPanel import that is ready, and a DirectAdmin import that is complete. */
interface ApiImport { id: string; status: string; fileName: string }
const imports = (administrator as unknown as Record<string, { body: { data: ApiImport[] } }>)["GET /imports"].body.data;
const ready = imports.find((i) => i.status === "ready")!;
const done = imports.find((i) => i.status === "done")!;

describe("the imports", () => {
  it("are only for an administrator", () => {
    expect(canOpen("administrator", "/imports")).toBe(true);
    expect(canOpen("reseller", "/imports")).toBe(false);
    expect(canOpen("owner", `/imports/${ready.id}`)).toBe(false);
    expect(NAVIGATION.administrator.flatMap((s) => s.items).some((i) => i.to === "/imports")).toBe(true);
  });

  it("lists each import with its status", async () => {
    await signedIn("administrator");
    render(<MemoryRouter initialEntries={["/imports"]}><App /></MemoryRouter>);
    const table = await screen.findByRole("table");
    const rows = within(table).getAllByRole("row").slice(1).map((r) => r.textContent);
    expect(rows.find((r) => r?.includes("cpmove-bakery.tar.gz"))).toContain("Ready to import");
    expect(rows.find((r) => r?.includes("user.admin.harbour.tar.zst"))).toContain("Complete with problems");
  });

  it("shows the plan, and starts the import of the selected parts", async () => {
    const api = await signedIn("administrator");
    api.on("POST /imports/*/start", () => [202, { ...(administrator as never as Record<string, { body: object }>)[`GET /imports/${ready.id}`].body, status: "running", plan: null, items: [{ id: "i1", kind: "website", name: "bakeryclub.example.org", state: "running", error: null, detail: null, jobId: "j1" }], progress: { total: 1, done: 0, failed: 0 } }]);
    render(<MemoryRouter initialEntries={[`/imports/${ready.id}`]}><App /></MemoryRouter>);

    // The website with a name that the panel has already cannot be selected.
    const conflict = await screen.findByText("A website with this name exists.");
    expect((within(conflict.closest("tr")!).getByRole("checkbox") as HTMLInputElement).disabled).toBe(true);
    // PHP 7.4 has no image: the plan selects the first newer version, PHP 8.2 from the pack.
    expect((screen.getByLabelText("Image for orders.bakeryclub.example.org") as HTMLSelectElement).selectedOptions[0].textContent).toBe("Apache + PHP-FPM · PHP 8.2");
    expect(screen.getByText("1 mailbox gets a new password")).toBeTruthy();
    expect(screen.getByText("The customer has this zone. The import adds the records that it does not have.")).toBeTruthy();
    expect(screen.getByText("The panel has no FTP accounts. Use SFTP with an SSH key in place of FTP.")).toBeTruthy();

    // The administrator leaves out the second website and the DNS zone of the customer.
    fireEvent.click(within(screen.getByText("orders.bakeryclub.example.org").closest("tr")!).getByRole("checkbox"));
    const zones = screen.getByRole("heading", { name: "DNS zones" }).closest("section")!;
    fireEvent.click(within(within(zones).getByText("cornerbakery.example.org").closest("tr")!).getByRole("checkbox"));
    const image = screen.getByLabelText("Image for bakeryclub.example.org") as HTMLSelectElement;
    const other = image.options[1].value;
    fireEvent.change(image, { target: { value: other } });
    expect(screen.getByText(/The import makes 1 website, 1 mail domain, 1 DNS zone for Corner Bakery on web-2/)).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "Start import" }));

    await screen.findByText("Running");
    const body = api.changes.find((c) => c.path.endsWith("/start"))!.body as { websites: { name: string; imageId: string }[]; mailDomains: string[]; dnsZones: string[]; rewriteWordPress: boolean };
    expect(body.websites.map((w) => w.name)).toEqual(["bakeryclub.example.org"]);
    expect(body.websites[0].imageId).toBe(other);
    expect(body.mailDomains).toEqual(["bakeryclub.example.org"]);
    expect(body.dnsZones).toEqual(["bakeryclub.example.org"]);
    expect(body.rewriteWordPress).toBe(true);
  });

  it("shows the parts of a complete import, with the failed part", async () => {
    await signedIn("administrator");
    render(<MemoryRouter initialEntries={[`/imports/${done.id}`]}><App /></MemoryRouter>);
    const failed = await screen.findByText("events.harbourbooks.example.net");
    expect(failed.closest("tr")!.textContent).toContain("Failed");
    expect(failed.closest("tr")!.textContent).toContain("A website with this name exists.");
    expect(screen.getByText("18204 files copied, database settings changed in wp-config.php")).toBeTruthy();
    expect(screen.getByText("8 of 8 ended")).toBeTruthy();
  });

  it("uploads a file in parts, and continues from the offset of the panel", async () => {
    const api = await signedIn("administrator");
    const file = new Blob([new Uint8Array(CHUNK_BYTES * 2 + 10)]);
    const offsets: number[] = [];
    let first = true;
    api.on("PUT /imports/imp-1/archive?offset=*", (_b, path) => {
      const offset = Number(new URL(path, "http://x").searchParams.get("offset"));
      offsets.push(offset);
      // The panel has the first part already, for example after a connection error.
      if (first) { first = false; return [409, { message: "Send the part from that offset.", receivedBytes: CHUNK_BYTES }]; }
      return { receivedBytes: Math.min(offset + CHUNK_BYTES, file.size) };
    });
    const seen: number[] = [];
    await uploadArchive("imp-1", file, 0, (n) => seen.push(n));
    expect(offsets).toEqual([0, CHUNK_BYTES, CHUNK_BYTES * 2]);
    expect(seen.at(-1)).toBe(file.size);
  });

  it("starts an import from a URL", async () => {
    const api = await signedIn("administrator");
    api.on("POST /imports", () => [201, { import: { id: "imp-2" }, job: null }]);
    api.on("GET /imports/imp-2", () => ({ ...(administrator as never as Record<string, { body: object }>)[`GET /imports/${ready.id}`].body, id: "imp-2", status: "fetching", plan: null }));
    render(<MemoryRouter initialEntries={["/imports"]}><App /></MemoryRouter>);
    fireEvent.click(await screen.findByRole("button", { name: "Import account" }));
    const dialog = screen.getByRole("dialog");
    fireEvent.click(within(dialog).getByLabelText(/Download from a URL/));
    fireEvent.change(within(dialog).getByLabelText("URL"), { target: { value: "https://files.example/cpmove-bakery.tar.gz" } });
    fireEvent.click(within(dialog).getByRole("button", { name: "Read the archive" }));
    expect(await screen.findByText(/downloads the archive and reads it/)).toBeTruthy();
    await waitFor(() => expect(api.changes.find((c) => c.path === "/imports")).toBeTruthy());
    expect(api.changes.find((c) => c.path === "/imports")!.body).toMatchObject({ url: "https://files.example/cpmove-bakery.tar.gz" });
  });
});
