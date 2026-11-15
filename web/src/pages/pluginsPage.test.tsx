import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";
import { App } from "../App";
import { getState } from "../api/store";
import type { Role } from "../api/types";
import { reset, signedIn, type PanelApi } from "../test/panelApi";

afterEach(() => {
  cleanup();
  reset();
  vi.restoreAllMocks();
});

async function open(role: Role, path: string): Promise<PanelApi> {
  const api = await signedIn(role);
  render(<MemoryRouter initialEntries={[path]}><App /></MemoryRouter>);
  return api;
}

describe("the Plugins page", () => {
  it("shows each plugin with its signature, its scopes and its deliveries", async () => {
    await open("administrator", "/plugins");
    expect(screen.getByRole("heading", { level: 1 }).textContent).toBe("Plugins");
    const main = screen.getByRole("main");
    await waitFor(() => expect(main.textContent).toContain("Uptime monitor 1.4.0"));
    expect(main.textContent).toContain("Signed by Example Plugins Ltd");
    expect(main.textContent).toContain("See all websites, their domains, files and logs.");
    expect(main.textContent).toContain("Chat alerts 0.9.2");
    // "Chat alerts" has a delivery that waits and one that failed.
    expect(main.textContent).toContain("0 delivered, 1 waiting, 1 failed");
    expect(within(main).getByText("Trusted keys")).toBeTruthy();
  });

  it("checks a manifest and shows what it asks for before the install", async () => {
    const api = await open("administrator", "/plugins");
    const manifest = { schema: 1, slug: "backup-report", name: "Backup report", version: "1.0.0", publisher: "Example Plugins Ltd", description: "", scopes: ["backups:read"], events: ["backup.started"], webhook: "/hooks", pages: [], actions: [{ id: "report", title: "Send report", target: "website", path: "/report", roles: ["owner"], confirm: "" }], hooks: [{ event: "website.removing", path: "/before", onError: "refuse" }], commands: [] };
    api.on("POST /plugins/preview", () => ({ manifest, signedBy: null, installed: null, developerMode: false }));
    await waitFor(() => screen.getByText("Uptime monitor 1.4.0"));
    fireEvent.click(screen.getByRole("button", { name: "Install plugin" }));
    const dialog = screen.getByRole("dialog");
    fireEvent.change(within(dialog).getByLabelText("Manifest (plugin.json)"), { target: { value: JSON.stringify(manifest) } });
    fireEvent.click(within(dialog).getByRole("button", { name: "Check the manifest" }));
    await waitFor(() => expect(dialog.textContent).toContain("Backup report 1.0.0"));
    expect(dialog.textContent).toContain("See the backups.");
    expect(dialog.textContent).toContain("No trusted signature. The panel refuses it.");
    expect(dialog.textContent).toContain("A backup to /hooks");
    expect(dialog.textContent).toContain("Send report on websites (Website owner)");
    expect(dialog.textContent).toContain("Delete a website (refused when the plugin does not answer)");
    expect(api.changes.find((c) => c.path === "/plugins/preview")?.body).toEqual({ manifest: JSON.stringify(manifest), signature: null });
  });
});

describe("a plugin action", () => {
  it("is a button on the website, and shows the answer of the plugin", async () => {
    const { pluginActions, websites } = (await signedIn("owner"), getState());
    const action = pluginActions.find((a) => a.target === "website")!;
    const site = websites[0];
    reset();
    const api = await open("owner", `/websites/${site.id}`);
    api.on(`POST /plugin-actions/${action.pluginId}/${action.id}`, () => ({ message: "The website answered in 182 ms.", url: null }));
    // The button also names the plugin, for a screen reader.
    fireEvent.click(await screen.findByRole("button", { name: new RegExp(`^${action.title}.*${action.pluginName}`) }));
    const dialog = screen.getByRole("dialog");
    expect(dialog.textContent).toContain("It gets access with your rights and the scopes of the plugin, for 15 minutes.");
    fireEvent.click(within(dialog).getByRole("button", { name: action.title }));
    await waitFor(() => expect(dialog.textContent).toContain("The website answered in 182 ms."));
    expect(api.changes.find((c) => c.path.startsWith("/plugin-actions/"))?.body).toEqual({ targetId: site.id });
  });
});

describe("a plugin page", () => {
  it("is in the menu of each role that the plugin gives it to", async () => {
    await open("owner", "/websites");
    const nav = screen.getByRole("navigation", { name: "Main" });
    const link = within(nav).getByRole("link", { name: "Uptime" });
    expect(link.getAttribute("href")).toMatch(/^\/plugin\/[a-z0-9]+\/status$/);
  });

  it("opens in a sandboxed frame, and gives the page token only to the origin of the plugin", async () => {
    const page = (await signedIn("owner"), getState().pluginPages[0]);
    reset();
    const api = await open("owner", `/plugin/${page.pluginId}/${page.id}`);
    api.on(`POST /plugin-pages/${page.pluginId}/${page.id}/session`, () => [201, { token: "9|page", expiresAt: "2026-11-15T03:00:00Z", scopes: ["websites:read"], page, user: { id: 3, name: "Olivia Owner", role: "owner", customerId: "c1" } }]);
    const frame = screen.getByTitle(`${page.title} (${page.pluginName})`) as HTMLIFrameElement;
    expect(frame.getAttribute("src")).toBe(page.url);
    expect(frame.getAttribute("sandbox")).not.toContain("allow-top-navigation");
    const post = vi.spyOn(frame.contentWindow!, "postMessage");

    // A message from a different origin gets nothing.
    window.dispatchEvent(new MessageEvent("message", { data: { type: "cloudfactory:ready" }, origin: "https://evil.example", source: frame.contentWindow }));
    await new Promise((r) => setTimeout(r, 20));
    expect(api.changes.some((c) => c.path.endsWith("/session"))).toBe(false);

    window.dispatchEvent(new MessageEvent("message", { data: { type: "cloudfactory:ready" }, origin: page.origin, source: frame.contentWindow }));
    await waitFor(() => expect(post).toHaveBeenCalled());
    const [message, origin] = post.mock.calls[0] as unknown as [{ type: string; token: string }, string];
    expect(origin).toBe(page.origin);
    expect(message.type).toBe("cloudfactory:session");
    expect(message.token).toBe("9|page");
  });
});
