// @vitest-environment jsdom
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";
import { App } from "../App";
import type { TerminalSession } from "../api/types";
import { parseRecording, socketUrl } from "../features/WebTerminal";
import { apiJob, reset, signedIn, website, type PanelApi } from "../test/panelApi";

// jsdom has no canvas, so the tests use a fake terminal. It keeps what the panel writes.
const terms = vi.hoisted(() => [] as { written: string[]; type: (keys: string) => void; options: Record<string, unknown> }[]);
vi.mock("@xterm/xterm", () => ({
  Terminal: class {
    cols = 80;
    rows = 24;
    written: string[] = [];
    options: Record<string, unknown>;
    private data: ((d: string) => void)[] = [];
    constructor(options: Record<string, unknown>) {
      this.options = { ...options };
      terms.push(this);
    }
    loadAddon() {}
    open() {}
    focus() {}
    dispose() {}
    write(d: string | Uint8Array) { this.written.push(typeof d === "string" ? d : new TextDecoder().decode(d)); }
    onData(fn: (d: string) => void) { this.data.push(fn); return { dispose: () => { this.data = []; } }; }
    onBinary() { return { dispose() {} }; }
    onResize() { return { dispose() {} }; }
    type(keys: string) { this.data.forEach((fn) => fn(keys)); }
  },
}));
vi.mock("@xterm/addon-fit", () => ({ FitAddon: class { fit() {} } }));
vi.mock("@xterm/xterm/css/xterm.css", () => ({}));

/** A WebSocket of the tests. The test plays the relay and the server. */
class FakeSocket {
  static OPEN = 1;
  static all: FakeSocket[] = [];
  readyState = 1;
  binaryType = "blob";
  sent: (string | Uint8Array)[] = [];
  onmessage: ((e: { data: unknown }) => void) | null = null;
  onclose: ((e: { code: number; reason: string }) => void) | null = null;
  constructor(public url: string) { FakeSocket.all.push(this); }
  send(d: string | Uint8Array) { this.sent.push(d); }
  close(code = 1000, reason = "") {
    this.readyState = 3;
    this.onclose?.({ code, reason });
  }
  receive(data: string | ArrayBuffer) { this.onmessage?.({ data }); }
}

afterEach(() => {
  cleanup();
  reset();
  terms.length = 0;
  FakeSocket.all = [];
});

async function open(name: string): Promise<{ api: PanelApi; id: string }> {
  const api = await signedIn("owner");
  vi.stubGlobal("WebSocket", FakeSocket);
  const id = website(name).id;
  render(<MemoryRouter initialEntries={[`/websites/${id}/commands`]}><App /></MemoryRouter>);
  return { api, id };
}

const session = (over: Partial<TerminalSession> = {}): TerminalSession => ({
  id: "s-1", websiteId: "", state: "opening", actor: "Olu Owner", ip: "203.0.113.14", jobId: "job-1", endReason: null, exitCode: null, commandCount: 0,
  hasRecording: false, recordingCut: false, createdAt: "2026-11-14T10:00:00+00:00", startedAt: null, endedAt: null, ...over,
});

/** Opens the terminal and gives the WebSocket of the browser. */
async function openTerminal(api: PanelApi, id: string): Promise<FakeSocket> {
  api.on(`POST /websites/${id}/terminal-sessions`, () => [202, { session: session({ websiteId: id }), job: apiJob({ websiteId: id, action: "OpenTerminal", state: "running" }), path: "/api/terminal/s-1/browser", ticket: "1800000000.ab" }]);
  api.on("GET /jobs/job-1", () => ({ data: apiJob({ websiteId: id, action: "OpenTerminal" }) }));
  fireEvent.click(await screen.findByRole("button", { name: "Open the terminal" }));
  await waitFor(() => expect(FakeSocket.all.length).toBe(1));
  return FakeSocket.all[0];
}

describe("the web terminal", () => {
  it("connects the terminal to the shell of the website", async () => {
    const { api, id } = await open("api.acme.example.com");
    const ws = await openTerminal(api, id);
    expect(api.changes.find((c) => c.path === `/websites/${id}/terminal-sessions`)?.body).toEqual({ cols: 80, rows: 24 });
    expect(ws.url).toBe(`ws://${window.location.host}/api/terminal/s-1/browser?ticket=1800000000.ab`);
    expect(ws.binaryType).toBe("arraybuffer");
    expect(screen.getByRole("status", { name: "" }).textContent).toBe("Connecting to the server…");

    // The keys go to the shell only when the server is connected.
    terms[0].type("x");
    expect(ws.sent).toEqual([]);
    ws.receive('{"type":"paired"}');
    expect(await screen.findByText("Connected")).toBeTruthy();
    terms[0].type("ls\r");
    expect(new TextDecoder().decode(ws.sent[0] as Uint8Array)).toBe("ls\r");
    ws.receive(new TextEncoder().encode("composer.json\r\napp$ ").buffer);
    expect(terms[0].written.join("")).toContain("composer.json");

    // The shell ends: the panel shows the reason of the server and the exit code.
    ws.receive('{"type":"exit","code":0}');
    ws.close(1000, "The shell ended.");
    expect(await screen.findByText("The shell ended. Exit code 0.")).toBeTruthy();
    expect(screen.getByRole("button", { name: "Open a new terminal" })).toBeTruthy();
  });

  it("closes the terminal", async () => {
    const { api, id } = await open("api.acme.example.com");
    const ws = await openTerminal(api, id);
    ws.receive('{"type":"paired"}');
    fireEvent.click(await screen.findByRole("button", { name: "Close the terminal" }));
    expect(await screen.findByText("You closed the terminal.")).toBeTruthy();
    expect(ws.readyState).toBe(3);
  });

  it("shows why a terminal did not open", async () => {
    const { api, id } = await open("api.acme.example.com");
    const ws = await openTerminal(api, id);
    api.on(`GET /websites/${id}/terminal-sessions/s-1`, () => ({ session: session({ websiteId: id, state: "failed", endReason: "The demonstration has no terminal relay, so the terminal cannot open." }) }));
    ws.close(1006, "");
    expect(await screen.findByText("The demonstration has no terminal relay, so the terminal cannot open.")).toBeTruthy();
  });

  it("does not open a terminal on a stopped website", async () => {
    await open("old.acme.example.com");
    expect((await screen.findByRole("button", { name: "Open the terminal" }) as HTMLButtonElement).disabled).toBe(true);
    expect(screen.getByText("The website does not run. Start it before you open the terminal.")).toBeTruthy();
  });

  it("shows the commands of the terminal in the log, and a session with its recording", async () => {
    const { api, id } = await open("api.acme.example.com");
    const row = (await screen.findByText("tail -n 2 laravel.log")).closest("tr")!;
    expect(within(row).getByText("In the terminal")).toBeTruthy();
    // The sample session and its commands, from the answers of the API for the lists.
    const sample = (await import("../test/fixtures/owner.json")).default as unknown as Record<string, { body: { data: Record<string, unknown>[] } }>;
    const one = sample[`GET /websites/${id}/terminal-sessions`].body.data[0];
    const log = sample[`GET /websites/${id}/commands`].body.data.filter((c) => c.sessionId === one.id);
    api.on(`GET /websites/${id}/terminal-sessions/${String(one.id)}`, () => ({ session: one }));
    api.on(`GET /websites/${id}/commands?sessionId=${String(one.id)}&perPage=200`, () => ({ data: log, meta: { currentPage: 1, lastPage: 1, total: log.length } }));
    api.on(`GET /websites/${id}/terminal-sessions/*/recording`, () => '{"version":2,"width":100,"height":30}\n[0.4,"o","app$ "]\n[24,"o","production.ERROR: The export directory is not writable.\\r\\n"]\n');
    fireEvent.click(within(row).getByRole("button", { name: /^Show session/ }));
    const dialog = await screen.findByRole("dialog");
    const commands = await within(dialog).findByRole("list", { name: "Commands of the session" });
    expect(within(commands).getAllByRole("listitem").map((li) => li.querySelector("code")?.textContent)).toEqual(["cd storage/logs", "ls -lh", "tail -n 2 laravel.log", "exit"]);
    expect(within(dialog).getByText("The shell ended. Exit code 0.")).toBeTruthy();
    await waitFor(() => expect(terms.at(-1)?.written.join("")).toContain("The export directory is not writable."));
    expect(terms.at(-1)?.options.disableStdin).toBe(true);
    expect(within(dialog).getByRole("button", { name: "Download the recording" })).toBeTruthy();
    // The sessions of the terminal.
    const sessions = screen.getByRole("columnheader", { name: "Opened" }).closest("table")!;
    expect(within(sessions).getByText("Closed")).toBeTruthy();
    expect(within(sessions).getByText("48 s")).toBeTruthy();
  });
});

describe("the parts of the terminal", () => {
  it("reads a recording", () => {
    expect(parseRecording('{"version":2,"width":80,"height":24}\n[0.1,"o","a"]\n[0.2,"r","120x40"]\n[0.3,"o","b"]\n[0.4,"o","c')).toEqual({ cols: 120, rows: 24, output: "ab" });
  });

  it("makes the address of the WebSocket", () => {
    expect(socketUrl("/api/terminal/s-1/browser", "1.a+b")).toBe(`ws://${window.location.host}/api/terminal/s-1/browser?ticket=1.a%2Bb`);
  });
});
