// @vitest-environment jsdom
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { afterEach, describe, expect, it } from "vitest";
import { App } from "../App";
import { apiJob, refresh, reset, signedIn, website } from "../test/panelApi";

afterEach(() => {
  cleanup();
  reset();
});

/** The sample data has the signed pack "golang" with the images Go 1.27 and Go 1.26. */
interface ApiRuntimeImage { id: string; name: string; family: string; pack: { slug: string } | null }

describe("the runtime of a pack", () => {
  it("shows the runtime as one choice with its versions and its usual commands", async () => {
    const api = await signedIn("owner");
    let go127 = "";
    api.change<{ data: ApiRuntimeImage[] }>("/runtime-images", (answer) => { go127 = answer.data.find((i) => i.name === "Go 1.27")!.id; });
    api.on("POST /websites", () => [202, { website: { id: "w-new" }, job: apiJob() }]);
    render(<MemoryRouter initialEntries={["/websites/new"]}><App /></MemoryRouter>);
    fireEvent.change(await screen.findByLabelText("Primary domain"), { target: { value: "api.example.com" } });
    // The preset of a Python application does not go to a Go website.
    fireEvent.change(screen.getByLabelText("Application"), { target: { value: "Flask" } });
    fireEvent.click(screen.getByRole("radio", { name: "Go" }));
    expect(screen.getByRole("radio", { name: "Go 1.27" })).toHaveProperty("checked", true);
    expect(screen.getByRole("radio", { name: "Go 1.26" })).toHaveProperty("checked", false);
    expect((screen.getByLabelText("Start command") as HTMLInputElement).value).toBe("./server");
    expect((screen.getByLabelText("Install command") as HTMLInputElement).value).toBe("go build -o server .");

    fireEvent.click(screen.getByRole("button", { name: "Create website" }));
    await waitFor(() => expect(api.changes.find((c) => c.path === "/websites")).toBeTruthy());
    expect(api.changes.find((c) => c.path === "/websites")!.body).toMatchObject({ imageId: go127, command: "./server", installCommand: "go build -o server ." });
  });

  it("gives a website of the runtime its start command, install command and application directory", async () => {
    const api = await signedIn("owner");
    let go127 = "";
    api.change<{ data: ApiRuntimeImage[] }>("/runtime-images", (answer) => { go127 = answer.data.find((i) => i.name === "Go 1.27")!.id; });
    api.change<{ data: { name: string; runtime: Record<string, unknown>; command: string | null; installCommand: string | null }[] }>("/websites?perPage=100&page=1", (answer) => {
      const w = answer.data.find((x) => x.name === "dashboard.acme.example.com")!;
      Object.assign(w.runtime, { imageId: go127, family: "golang", version: "1.27" });
      Object.assign(w, { command: "./server", installCommand: "go build -o server ." });
    });
    await refresh();
    const site = website("dashboard.acme.example.com");
    expect(site.runtime.kind).toBe("app");
    render(<MemoryRouter initialEntries={[`/websites/${site.id}/runtime`]}><App /></MemoryRouter>);
    expect(await screen.findByText("Go version")).toBeTruthy();
    const versions = screen.getByRole("group", { name: "Runtime image" });
    expect(within(versions).getAllByRole("radio").map((r) => r.closest("label")?.querySelector("strong")?.textContent)).toEqual(["Go 1.26", "Go 1.27"]);
    expect((screen.getByLabelText("Command that starts the app") as HTMLInputElement).value).toBe("./server");
    expect((screen.getByLabelText("Install command") as HTMLInputElement).value).toBe("go build -o server .");
    expect(screen.getByText("Application directory")).toBeTruthy();
    expect(screen.queryByText("package.json")).toBeNull();
    expect(screen.queryByText("Extensions and php.ini")).toBeNull();
  });

  it("shows a tile for the runtime on the Apps page, and the own images only in the table", async () => {
    await signedIn("administrator");
    render(<MemoryRouter initialEntries={["/apps"]}><App /></MemoryRouter>);
    const tile = (await screen.findByRole("heading", { name: "Go" })).closest("section")!;
    expect(tile.textContent).toContain("Go 1.26, 1.27");
    expect(within(tile).getByRole("link", { name: "New Go website" })).toBeTruthy();
    expect(within(screen.getByRole("main")).getByRole("link", { name: "Packs" })).toBeTruthy();
    const table = screen.getByRole("table");
    expect(within(table).queryByText("Go 1.27")).toBeNull();
  });

  it("sends an own image as before, with no runtime field", async () => {
    const api = await signedIn("administrator");
    api.on("POST /images", () => [201, {}]);
    render(<MemoryRouter initialEntries={["/apps"]}><App /></MemoryRouter>);
    fireEvent.click(await screen.findByRole("button", { name: "Add your own image" }));
    const dialog = screen.getByRole("dialog");
    expect(within(dialog).queryByText("Runtime from an agent manifest")).toBeNull();
    fireEvent.change(within(dialog).getByLabelText("Name"), { target: { value: "App" } });
    fireEvent.change(within(dialog).getByLabelText("Image reference"), { target: { value: "registry.example.com/app" } });
    fireEvent.change(within(dialog).getByLabelText("HTTP port"), { target: { value: "9000" } });
    fireEvent.click(within(dialog).getByRole("button", { name: "Add image" }));
    await waitFor(() => expect(api.changes.find((c) => c.path === "/images")).toBeTruthy());
    expect(api.changes.find((c) => c.path === "/images")!.body).toEqual({ name: "App", reference: "registry.example.com/app", tag: "latest", policy: "manual", containerPort: 9000, command: null });
  });
});

/** The sample data also has the pack "php-legacy-apache": PHP 8.3 and 8.2 for Apache + PHP-FPM. */
describe("a PHP pack", () => {
  it("adds its versions to the PHP runtime, with no start command", async () => {
    const api = await signedIn("owner");
    let php82 = "";
    api.change<{ data: ApiRuntimeImage[] }>("/runtime-images", (answer) => { php82 = answer.data.find((i) => i.name === "Apache + PHP-FPM · PHP 8.2")!.id; });
    api.on("POST /websites", () => [202, { website: { id: "w-new" }, job: apiJob() }]);
    render(<MemoryRouter initialEntries={["/websites/new"]}><App /></MemoryRouter>);
    fireEvent.change(await screen.findByLabelText("Primary domain"), { target: { value: "old-shop.example.com" } });
    fireEvent.click(screen.getByRole("radio", { name: "Apache + PHP-FPM" }));
    const versions = screen.getByRole("group", { name: "Version" });
    expect(within(versions).getAllByRole("radio").map((r) => r.closest("label")?.textContent)).toEqual(["PHP 8.2", "PHP 8.3", "PHP 8.4", "PHP 8.5"]);
    expect(screen.queryByRole("radio", { name: "Older PHP for Apache + PHP-FPM" })).toBeNull();
    fireEvent.click(screen.getByRole("radio", { name: "Apache + PHP-FPM · PHP 8.2" }));
    expect(screen.getByText(/comes from the pack Older PHP for Apache \+ PHP-FPM/)).toBeTruthy();
    expect(screen.queryByLabelText("Start command")).toBeNull();

    fireEvent.click(screen.getByRole("button", { name: "Create website" }));
    await waitFor(() => expect(api.changes.find((c) => c.path === "/websites")).toBeTruthy());
    const body = api.changes.find((c) => c.path === "/websites")!.body as Record<string, unknown>;
    expect(body).toMatchObject({ imageId: php82, name: "old-shop.example.com" });
    // A PHP website has no start command and no install command.
    expect(body.command).toBeUndefined();
    expect(body.installCommand).toBeUndefined();
  });

  it("shows a tile on the Apps page with the PHP runtime and its versions", async () => {
    await signedIn("administrator");
    render(<MemoryRouter initialEntries={["/apps"]}><App /></MemoryRouter>);
    const tile = (await screen.findByRole("heading", { name: "Older PHP for Apache + PHP-FPM" })).closest("section")!;
    expect(tile.textContent).toContain("Apache + PHP-FPM · PHP 8.2, 8.3");
    expect(within(tile).getByRole("link", { name: "New website" })).toBeTruthy();
  });

  it("shows the PHP runtime and the versions on the Packs page, not commands", async () => {
    await signedIn("administrator");
    render(<MemoryRouter initialEntries={["/packs"]}><App /></MemoryRouter>);
    const card = (await screen.findByRole("heading", { name: "Older PHP for Apache + PHP-FPM 0.1.9" })).closest(".card") as HTMLElement;
    expect(within(card).getByText("Signed by the Cloudfactory release key")).toBeTruthy();
    expect(card.textContent).toContain("cf-php/1: adds PHP 8.3 and 8.2");
    expect(within(card).queryByText("Commands")).toBeNull();
    expect(within(card).getByText("ghcr.io/itbm/apache-php8.2:0.1.9")).toBeTruthy();
  });

  it("shows what a PHP pack adds before the install", async () => {
    const api = await signedIn("administrator");
    api.on("POST /packs/preview", () => ({
      manifest: {
        contract: "cf-pack/1", kind: "runtime", slug: "php-legacy-frankenphp", name: "Older PHP for FrankenPHP", version: "0.2.0", publisher: "Cloudfactory", description: "",
        runtime: { contract: "cf-php/1", type: "", defaultCommand: "", defaultInstall: "", family: "frankenphp" },
        images: ["8.3", "8.2"].map((v) => ({ version: v, ref: `ghcr.io/itbm/frankenphp-php${v}:0.2.0`, repository: `ghcr.io/itbm/frankenphp-php${v}`, tag: "0.2.0", digest: `sha256:${"b".repeat(64)}`, supportEndsAt: null })),
      },
      signedBy: { name: "Cloudfactory release key", fingerprint: null, release: true }, installed: null, developerMode: false,
    }));
    render(<MemoryRouter initialEntries={["/packs"]}><App /></MemoryRouter>);
    fireEvent.click(await screen.findByRole("button", { name: "Install pack" }));
    const dialog = screen.getByRole("dialog");
    fireEvent.change(within(dialog).getByLabelText("Manifest (pack.json)"), { target: { value: "{}" } });
    fireEvent.click(within(dialog).getByRole("button", { name: "Check the manifest" }));
    expect(await within(dialog).findByText("Older PHP for FrankenPHP 0.2.0")).toBeTruthy();
    expect(dialog.textContent).toContain("The pack adds PHP 8.3 and 8.2 to FrankenPHP.");
    expect(dialog.textContent).not.toContain("start command");
    expect(within(dialog).getByText("PHP 8.2")).toBeTruthy();
  });
});

describe("the Packs page", () => {
  it("shows each pack with its signature, its images and its state on each web server", async () => {
    await signedIn("administrator");
    render(<MemoryRouter initialEntries={["/packs"]}><App /></MemoryRouter>);
    const card = (await screen.findByRole("heading", { name: "Go 1.2.0" })).closest(".card") as HTMLElement;
    expect(within(card).getByText("Signed by Example Packs Ltd")).toBeTruthy();
    expect(within(card).getByText("ghcr.io/example-packs/cf-golang:1.27")).toBeTruthy();
    expect(within(card).getAllByText("Loaded")).toHaveLength(2);
    expect(within(card).getByText("Not loaded")).toBeTruthy();
    expect(screen.getByText("Trusted keys")).toBeTruthy();
    expect(screen.getByText("Developer mode")).toBeTruthy();
  });

  it("checks a manifest, shows what it adds, and installs it with the password", async () => {
    const api = await signedIn("administrator");
    const manifest = '{"contract": "cf-pack/1", "kind": "runtime", "slug": "zig"}\n';
    api.on("POST /packs/preview", () => ({
      manifest: {
        contract: "cf-pack/1", kind: "runtime", slug: "zig", name: "Zig", version: "0.1.0", publisher: "Example Packs Ltd", description: "Zig applications.",
        runtime: { contract: "cf-runtime/1", type: "app", defaultCommand: "./zig-out/bin/app", defaultInstall: "zig build" },
        images: [{ version: "0.15", ref: "ghcr.io/example-packs/cf-zig:0.15", repository: "ghcr.io/example-packs/cf-zig", tag: "0.15", digest: `sha256:${"a".repeat(64)}`, supportEndsAt: null }],
      },
      signedBy: null, installed: null, developerMode: false,
    }));
    api.on("POST /packs", () => [201, {}]);
    render(<MemoryRouter initialEntries={["/packs"]}><App /></MemoryRouter>);
    fireEvent.click(await screen.findByRole("button", { name: "Install pack" }));
    const dialog = screen.getByRole("dialog");
    fireEvent.change(within(dialog).getByLabelText("Manifest (pack.json)"), { target: { value: manifest } });
    fireEvent.change(within(dialog).getByLabelText("Signature"), { target: { value: " c2lnbmF0dXJl \n" } });
    fireEvent.click(within(dialog).getByRole("button", { name: "Check the manifest" }));
    expect(await within(dialog).findByText("Zig 0.1.0")).toBeTruthy();
    // The manifest goes to the panel with no change: the signature covers each byte.
    expect(api.changes.find((c) => c.path === "/packs/preview")!.body).toEqual({ manifest, signature: "c2lnbmF0dXJl" });
    expect(dialog.textContent).toContain("No trusted signature. The panel refuses it.");
    expect(within(dialog).getByText("ghcr.io/example-packs/cf-zig:0.15")).toBeTruthy();
    expect(dialog.textContent).toContain("It cannot run a command on a server");

    fireEvent.change(within(dialog).getByLabelText("Your password"), { target: { value: "password" } });
    fireEvent.click(within(dialog).getByRole("button", { name: "Install pack" }));
    await waitFor(() => expect(api.changes.find((c) => c.method === "POST" && c.path === "/packs")).toBeTruthy());
    expect(api.changes.find((c) => c.method === "POST" && c.path === "/packs")!.body).toMatchObject({ manifest, signature: "c2lnbmF0dXJl", password: "password" });
  });

  it("does not remove a pack that websites use", async () => {
    const api = await signedIn("administrator");
    api.change<{ data: { images: { websites: number }[] }[] }>("/packs", (answer) => { answer.data[0].images[0].websites = 2; });
    render(<MemoryRouter initialEntries={["/packs"]}><App /></MemoryRouter>);
    fireEvent.click(await screen.findByRole("button", { name: "Actions for Go" }));
    fireEvent.click(screen.getByRole("menuitem", { name: "Remove pack" }));
    const dialog = screen.getByRole("dialog");
    expect(dialog.textContent).toContain("2 websites use this runtime.");
    expect(within(dialog).getByRole("button", { name: "Remove pack" })).toHaveProperty("disabled", true);
  });

  it("is only for administrators", async () => {
    await signedIn("owner");
    render(<MemoryRouter initialEntries={["/packs"]}><App /></MemoryRouter>);
    await waitFor(() => expect(screen.queryByRole("heading", { name: "Packs" })).toBeNull());
  });
});
