import { useEffect, useRef, useState } from "react";
import type { ITheme, Terminal as XTerm } from "@xterm/xterm";
import { ApiError, openTerminal, readTerminalSession } from "../api/store";
import { FormError } from "../components/ui";

/**
 * The web terminal of a website (FR-802). The browser and the agent of the server connect to the
 * terminal relay of the panel with a WebSocket. A binary message is keys (to the shell) or output
 * (from the shell). A text message is JSON: {"type":"paired"} from the relay when the server is
 * connected, {"type":"resize"} to the shell, and {"type":"exit","code":0} when the shell ends.
 */

/** The colours of the terminal. It is dark in the light and in the dark theme of the panel. */
export const TERMINAL_THEME: ITheme = {
  background: "#0f1b33", foreground: "#e6eaf2", cursor: "#e6eaf2", cursorAccent: "#0f1b33", selectionBackground: "#35507f",
};

const FONT = 'ui-monospace, "SF Mono", "JetBrains Mono", Menlo, Consolas, monospace';

/** The terminal library is large, so the panel loads it only when a user opens a terminal. */
export async function loadTerminal() {
  const [{ Terminal }, { FitAddon }] = await Promise.all([import("@xterm/xterm"), import("@xterm/addon-fit"), import("@xterm/xterm/css/xterm.css")]);
  return { Terminal, FitAddon };
}

/** The address of the WebSocket for a path of the panel domain. */
export function socketUrl(path: string, ticket: string): string {
  const scheme = window.location.protocol === "https:" ? "wss:" : "ws:";
  return `${scheme}//${window.location.host}${path}?ticket=${encodeURIComponent(ticket)}`;
}

type Phase = "idle" | "opening" | "connecting" | "open" | "ended";

const STATUS: Record<Phase, string> = {
  idle: "",
  opening: "Opening the terminal…",
  connecting: "Connecting to the server…",
  open: "Connected",
  ended: "",
};

export function WebTerminal({ websiteId, label, disabled, onChange }: {
  websiteId: string;
  /** The accessible name of the terminal, for example "Terminal of shop.example.com". */
  label: string;
  disabled: boolean;
  /** Called when a session starts (true) or ends (false), for example to load the command log again. */
  onChange?: (open: boolean) => void;
}) {
  const box = useRef<HTMLDivElement>(null);
  const term = useRef<XTerm | null>(null);
  const socket = useRef<WebSocket | null>(null);
  const [phase, setPhase] = useState<Phase>("idle");
  const [message, setMessage] = useState("");
  const [error, setError] = useState<ApiError | null>(null);
  const changed = useRef(onChange);
  useEffect(() => { changed.current = onChange; });

  useEffect(() => () => {
    socket.current?.close(1000, "The user left the page.");
    term.current?.dispose();
  }, []);

  const open = async () => {
    setError(null);
    setMessage("");
    setPhase("opening");
    let t: XTerm | null = null;
    try {
      const { Terminal, FitAddon } = await loadTerminal();
      term.current?.dispose();
      t = new Terminal({ cursorBlink: true, fontFamily: FONT, fontSize: 14, theme: TERMINAL_THEME, scrollback: 5000 });
      const fit = new FitAddon();
      t.loadAddon(fit);
      t.open(box.current!);
      fit.fit();
      term.current = t;
      const opening = await openTerminal(websiteId, Math.max(10, t.cols), Math.max(2, t.rows));
      changed.current?.(true);
      setPhase("connecting");
      const ws = new WebSocket(socketUrl(opening.path, opening.ticket));
      ws.binaryType = "arraybuffer";
      socket.current = ws;
      const shell = t;
      const encoder = new TextEncoder();
      let paired = false;
      let exit: number | null = null;
      const send = (data: string | Uint8Array<ArrayBuffer>) => {
        if (paired && ws.readyState === WebSocket.OPEN) ws.send(data);
      };
      const keys = shell.onData((d) => send(encoder.encode(d)));
      const binary = shell.onBinary((d) => send(Uint8Array.from(d, (c) => c.charCodeAt(0) & 255)));
      const resize = shell.onResize(({ cols, rows }) => send(JSON.stringify({ type: "resize", cols, rows })));
      const onWindow = () => fit.fit();
      window.addEventListener("resize", onWindow);
      ws.onmessage = (e: MessageEvent) => {
        if (typeof e.data !== "string") {
          shell.write(new Uint8Array(e.data as ArrayBuffer));
          return;
        }
        let m: { type?: string; code?: number } = {};
        try {
          m = JSON.parse(e.data) as typeof m;
        } catch {
          return;
        }
        if (m.type === "paired") {
          paired = true;
          setPhase("open");
          shell.focus();
        } else if (m.type === "exit" && typeof m.code === "number") {
          exit = m.code;
        }
      };
      ws.onclose = (e: CloseEvent) => {
        keys.dispose();
        binary.dispose();
        resize.dispose();
        window.removeEventListener("resize", onWindow);
        // The shell does not take keys now, so the terminal does not show a cursor.
        shell.options.cursorBlink = false;
        shell.write("\x1b[?25l");
        setPhase("ended");
        changed.current?.(false);
        if (paired) {
          const reason = e.reason || "The connection to the terminal closed.";
          setMessage(exit === null ? reason : `${reason} Exit code ${exit}.`);
          return;
        }
        // The terminal did not open. The session tells why, for example when the server is offline.
        setMessage(e.reason || "The terminal could not connect to the server.");
        void readTerminalSession(websiteId, opening.session.id).then((s) => {
          if (s.state === "failed" && s.endReason) setMessage(s.endReason);
        }).catch(() => {});
      };
    } catch (e) {
      setError(e instanceof ApiError ? e : new ApiError((e as Error).message));
      setPhase(t ? "ended" : "idle");
    }
  };

  const close = () => socket.current?.close(1000, "You closed the terminal.");
  const busy = phase === "opening" || phase === "connecting" || phase === "open";
  const shown = phase !== "idle";

  return (
    <div className="card-body">
      <FormError error={error} />
      <div className="terminal-bar">
        <span className="muted small" role="status">{STATUS[phase] || message}</span>
        <span className="terminal-actions">
          {busy
            ? <button type="button" className="btn btn-sm" onClick={close} disabled={phase === "opening"}>Close the terminal</button>
            : <button type="button" className="btn btn-primary btn-sm" onClick={() => void open()} disabled={disabled}>{shown ? "Open a new terminal" : "Open the terminal"}</button>}
        </span>
      </div>
      <div className={shown ? "terminal-box" : "terminal-box terminal-box-hidden"} ref={box} role="region" aria-label={label} />
    </div>
  );
}

/** A recording of a session in the asciicast v2 format: the size and the output. */
export function parseRecording(text: string): { cols: number; rows: number; output: string } {
  const lines = text.split("\n").filter((l) => l.trim() !== "");
  let cols = 80;
  let rows = 24;
  const out: string[] = [];
  lines.forEach((line, i) => {
    try {
      const v = JSON.parse(line) as unknown;
      if (i === 0 && v && typeof v === "object" && !Array.isArray(v)) {
        const h = v as { width?: number; height?: number };
        cols = h.width ?? cols;
        rows = h.height ?? rows;
      } else if (Array.isArray(v) && v[1] === "o" && typeof v[2] === "string") {
        out.push(v[2]);
      } else if (Array.isArray(v) && v[1] === "r" && typeof v[2] === "string") {
        const [c] = v[2].split("x").map(Number);
        if (c > cols) cols = c;
      }
    } catch {
      // A line that is not JSON: the recording was cut.
    }
  });
  return { cols, rows, output: out.join("") };
}

/** Shows all the output of a recording in a terminal that does not take keys. */
export function RecordingView({ text, label }: { text: string; label: string }) {
  const box = useRef<HTMLDivElement>(null);
  const [error, setError] = useState<ApiError | null>(null);
  useEffect(() => {
    let t: XTerm | null = null;
    let gone = false;
    const { cols, output } = parseRecording(text);
    void loadTerminal().then(({ Terminal }) => {
      if (gone || !box.current) return;
      const width = Math.min(Math.max(cols, 20), 400);
      // A smaller font when the recording is wider than the box. A very wide recording scrolls sideways.
      const space = box.current.clientWidth - 16;
      const fontSize = space > 0 ? Math.min(13, Math.max(8, Math.floor(space / (width * 0.6)))) : 13;
      t = new Terminal({ cols: width, rows: 20, disableStdin: true, cursorBlink: false, cursorInactiveStyle: "none", fontFamily: FONT, fontSize, theme: TERMINAL_THEME, scrollback: 100000 });
      t.open(box.current);
      t.write(output);
    }).catch((e: unknown) => setError(new ApiError((e as Error).message)));
    return () => {
      gone = true;
      t?.dispose();
    };
  }, [text]);
  return (
    <>
      <FormError error={error} />
      <div className="terminal-box terminal-recording" ref={box} role="region" aria-label={label} />
    </>
  );
}
