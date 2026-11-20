// @vitest-environment jsdom
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { afterEach, describe, expect, it } from "vitest";
import { App } from "../App";
import { apiJob, refresh, reset, signedIn, website, type PanelApi } from "../test/panelApi";

afterEach(() => {
  cleanup();
  reset();
});

/** Two images of the runtime "golang" from a manifest of the agent, as the API gives them. */
const go = (id: string, version: string) => ({
  id, name: `Go ${version}`, family: "golang", tag: `${version}.0`, phpVersion: null, version, custom: false, supportEndsAt: null,
  defaults: { command: "./server", install: "go build -o server ." }, appRuntime: true,
});

async function withGo(api: PanelApi) {
  api.change<{ data: object[] }>("/runtime-images", (answer) => { answer.data.push(go("go-124", "1.24"), go("go-125", "1.25")); });
  await refresh();
}

describe("runtimes from the manifests of the agent", () => {
  it("shows the runtime as one choice with its versions and its usual commands", async () => {
    const api = await signedIn("owner");
    await withGo(api);
    api.on("POST /websites", () => [202, { website: { id: "w-new" }, job: apiJob() }]);
    render(<MemoryRouter initialEntries={["/websites/new"]}><App /></MemoryRouter>);
    fireEvent.change(await screen.findByLabelText("Primary domain"), { target: { value: "api.example.com" } });
    // The preset of a Python application does not go to a Go website.
    fireEvent.change(screen.getByLabelText("Application"), { target: { value: "Flask" } });
    fireEvent.click(screen.getByRole("radio", { name: "Go" }));
    expect(screen.getByRole("radio", { name: "Go 1.25" })).toHaveProperty("checked", true);
    expect(screen.getByRole("radio", { name: "Go 1.24" })).toHaveProperty("checked", false);
    expect((screen.getByLabelText("Start command") as HTMLInputElement).value).toBe("./server");
    expect((screen.getByLabelText("Install command") as HTMLInputElement).value).toBe("go build -o server .");

    fireEvent.click(screen.getByRole("button", { name: "Create website" }));
    await waitFor(() => expect(api.changes.find((c) => c.path === "/websites")).toBeTruthy());
    expect(api.changes.find((c) => c.path === "/websites")!.body).toMatchObject({ imageId: "go-125", command: "./server", installCommand: "go build -o server ." });
  });

  it("gives a website of the runtime its start command, install command and application directory", async () => {
    const api = await signedIn("owner");
    api.change<{ data: { name: string; runtime: Record<string, unknown>; command: string | null; installCommand: string | null }[] }>("/websites?perPage=100&page=1", (answer) => {
      const w = answer.data.find((x) => x.name === "dashboard.acme.example.com")!;
      Object.assign(w.runtime, { imageId: "go-125", family: "golang", version: "1.25" });
      Object.assign(w, { command: "./server", installCommand: "go build -o server ." });
    });
    await withGo(api);
    const site = website("dashboard.acme.example.com");
    expect(site.runtime.kind).toBe("app");
    render(<MemoryRouter initialEntries={[`/websites/${site.id}/runtime`]}><App /></MemoryRouter>);
    expect(await screen.findByText("Go version")).toBeTruthy();
    const versions = screen.getByRole("group", { name: "Runtime image" });
    expect(within(versions).getAllByRole("radio").map((r) => r.closest("label")?.querySelector("strong")?.textContent)).toEqual(["Go 1.24", "Go 1.25"]);
    expect((screen.getByLabelText("Command that starts the app") as HTMLInputElement).value).toBe("./server");
    expect((screen.getByLabelText("Install command") as HTMLInputElement).value).toBe("go build -o server .");
    expect(screen.getByText("Application directory")).toBeTruthy();
    expect(screen.queryByText("package.json")).toBeNull();
    expect(screen.queryByText("Extensions and php.ini")).toBeNull();
  });

  it("lets an administrator add an image of a runtime", async () => {
    const api = await signedIn("administrator");
    api.on("POST /images", () => [201, {}]);
    render(<MemoryRouter initialEntries={["/apps"]}><App /></MemoryRouter>);
    fireEvent.click(await screen.findByRole("button", { name: "Add your own image" }));
    const dialog = screen.getByRole("dialog");
    fireEvent.click(within(dialog).getByRole("radio", { name: "Runtime from an agent manifest" }));
    expect(within(dialog).queryByLabelText("HTTP port")).toBeNull();
    fireEvent.change(within(dialog).getByLabelText("Name"), { target: { value: "Go 1.25" } });
    fireEvent.change(within(dialog).getByLabelText("Image reference"), { target: { value: "ghcr.io/example/golang" } });
    fireEvent.change(within(dialog).getByLabelText("Tag"), { target: { value: "1.25.0" } });
    fireEvent.change(within(dialog).getByLabelText("Runtime name"), { target: { value: "golang" } });
    fireEvent.change(within(dialog).getByLabelText("Usual start command"), { target: { value: "./server" } });
    fireEvent.click(within(dialog).getByRole("button", { name: "Add image" }));
    await waitFor(() => expect(api.changes.find((c) => c.path === "/images")).toBeTruthy());
    expect(api.changes.find((c) => c.path === "/images")!.body).toEqual({
      name: "Go 1.25", reference: "ghcr.io/example/golang", tag: "1.25.0", policy: "manual", runtime: "golang", defaultCommand: "./server", defaultInstall: null,
    });
  });

  it("sends an own image as before", async () => {
    const api = await signedIn("administrator");
    api.on("POST /images", () => [201, {}]);
    render(<MemoryRouter initialEntries={["/apps"]}><App /></MemoryRouter>);
    fireEvent.click(await screen.findByRole("button", { name: "Add your own image" }));
    const dialog = screen.getByRole("dialog");
    fireEvent.change(within(dialog).getByLabelText("Name"), { target: { value: "App" } });
    fireEvent.change(within(dialog).getByLabelText("Image reference"), { target: { value: "registry.example.com/app" } });
    fireEvent.change(within(dialog).getByLabelText("HTTP port"), { target: { value: "9000" } });
    fireEvent.click(within(dialog).getByRole("button", { name: "Add image" }));
    await waitFor(() => expect(api.changes.find((c) => c.path === "/images")).toBeTruthy());
    expect(api.changes.find((c) => c.path === "/images")!.body).toEqual({ name: "App", reference: "registry.example.com/app", tag: "latest", policy: "manual", containerPort: 9000, command: null });
  });
});
