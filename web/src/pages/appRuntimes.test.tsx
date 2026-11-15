// @vitest-environment jsdom
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { afterEach, describe, expect, it } from "vitest";
import { App } from "../App";
import { apiJob, reset, signedIn, website } from "../test/panelApi";

afterEach(() => {
  cleanup();
  reset();
});

describe("Node.js and Python websites", () => {
  it("fills in the start command and the install command of the application", async () => {
    await signedIn("owner");
    render(<MemoryRouter initialEntries={["/websites/new"]}><App /></MemoryRouter>);
    fireEvent.change(await screen.findByLabelText("Application"), { target: { value: "Next.js" } });
    expect(screen.getByRole("radio", { name: /Node\.js 26/ })).toHaveProperty("checked", true);
    expect((screen.getByLabelText("Start command") as HTMLInputElement).value).toBe("npm start");
    expect((screen.getByLabelText("Install command") as HTMLInputElement).value).toBe("npm install && npm run build");

    fireEvent.change(screen.getByLabelText("Application"), { target: { value: "FastAPI" } });
    expect(screen.getByRole("radio", { name: /Python 3\.14/ })).toHaveProperty("checked", true);
    expect((screen.getByLabelText("Start command") as HTMLInputElement).value).toBe("uvicorn main:app --host 0.0.0.0 --port $PORT");
    expect((screen.getByLabelText("Install command") as HTMLInputElement).value).toContain("requirements.txt");

    fireEvent.change(screen.getByLabelText("Application"), { target: { value: "Laravel" } });
    expect(screen.queryByLabelText("Install command")).toBeNull();
  });

  it("sends the commands with a new Node.js website", async () => {
    const api = await signedIn("owner");
    api.on("POST /websites", () => [202, { website: { id: "w-new" }, job: apiJob() }]);
    render(<MemoryRouter initialEntries={["/websites/new"]}><App /></MemoryRouter>);
    fireEvent.change(await screen.findByLabelText("Primary domain"), { target: { value: "shop.example.com" } });
    fireEvent.change(screen.getByLabelText("Application"), { target: { value: "Express" } });
    fireEvent.change(screen.getByLabelText("Start command"), { target: { value: "node server.js" } });
    fireEvent.click(screen.getByRole("button", { name: "Create website" }));
    await waitFor(() => expect(api.changes.find((c) => c.path === "/websites")).toBeTruthy());
    expect(api.changes.find((c) => c.path === "/websites")!.body).toMatchObject({ application: "Express", command: "node server.js", installCommand: "npm install --omit=dev" });
  });

  it("shows the version, the commands and the names of the variables, and never the values", async () => {
    const api = await signedIn("owner");
    const site = website("dashboard.acme.example.com");
    api.on("PATCH /websites/*/environment", () => [202, { website: { ...apiWebsite(site.id), environment: ["DATABASE_URL", "SESSION_SECRET"] }, job: apiJob({ state: "running" }) }]);
    render(<MemoryRouter initialEntries={[`/websites/${site.id}/runtime`]}><App /></MemoryRouter>);
    expect(await screen.findByText("Node.js version")).toBeTruthy();
    expect((screen.getByLabelText("Command that starts the app") as HTMLInputElement).value).toBe("npm start");
    expect((screen.getByLabelText("Install command") as HTMLInputElement).value).toBe("npm ci --omit=dev");
    expect(screen.getByText("Application directory")).toBeTruthy();
    expect(screen.queryByText("Extensions and php.ini")).toBeNull();
    const list = screen.getByRole("list", { name: "Variables" });
    expect(within(list).getAllByRole("listitem").map((li) => li.querySelector("code")?.textContent)).toEqual(["DATABASE_URL", "NODE_ENV", "SESSION_SECRET"]);

    fireEvent.click(screen.getByRole("button", { name: "Remove NODE_ENV" }));
    await waitFor(() => expect(api.changes.some((c) => c.method === "PATCH")).toBe(true));
    expect(api.changes.find((c) => c.method === "PATCH")!.body).toEqual({ variables: { NODE_ENV: null } });
  });

  it("runs the install command", async () => {
    const api = await signedIn("owner");
    const site = website("dashboard.acme.example.com");
    api.on("POST /websites/*/runtime/install", () => [202, { job: apiJob({ title: "Install dependencies", state: "running", websiteId: site.id }) }]);
    render(<MemoryRouter initialEntries={[`/websites/${site.id}/runtime`]}><App /></MemoryRouter>);
    fireEvent.click(await screen.findByRole("button", { name: "Install dependencies" }));
    await waitFor(() => expect(api.changes.some((c) => c.path.endsWith("/runtime/install"))).toBe(true));
  });
});

/** The website as the API gives it, from the fixture. */
function apiWebsite(id: string) {
  return { id, name: "dashboard.acme.example.com", application: "Next.js", customerId: "c", serverId: "s", planId: "p", unixUser: "u", status: "running", runtime: { imageId: "i", family: "node", image: "x", phpVersion: null, version: "24", workerMode: false, extensions: [], phpIni: {}, previousImageId: null }, limits: {}, usage: null, createdAt: "2026-11-14T10:00:00+00:00" };
}
