import { MoreHorizontal } from "lucide-react";
import { useEffect, useRef, useState } from "react";
import { useNavigate } from "react-router-dom";
import { createStagingCopy, deleteWebsite, setPower, type StagingCredential } from "../api/store";
import type { Website } from "../api/types";
import { ENGINE_NAME } from "../lib/labels";
import { CopyField } from "../pages/website/AccessTab";
import { Modal } from "./ui";

/** The actions menu of a website (FR-204, FR-205). */
export function WebsiteMenu({ website, bordered = false }: { website: Website; bordered?: boolean }) {
  const [open, setOpen] = useState(false);
  const [confirm, setConfirm] = useState<null | "stop" | "delete">(null);
  const [typed, setTyped] = useState("");
  const [error, setError] = useState<{ title: string; text: string } | null>(null);
  const [staging, setStaging] = useState<{ websiteId: string; credentials: StagingCredential[] } | null>(null);
  // Batch 2, finding 11: show the error of each action.
  const attempt = (title: string, fn: () => unknown) => {
    void (async () => {
      try {
        await fn();
      } catch (e) {
        setError({ title, text: (e as Error).message });
      }
    })();
  };
  const ref = useRef<HTMLDivElement>(null);
  const navigate = useNavigate();

  useEffect(() => {
    if (!open) return;
    const close = (e: MouseEvent) => !ref.current?.contains(e.target as Node) && setOpen(false);
    const esc = (e: KeyboardEvent) => e.key === "Escape" && setOpen(false);
    document.addEventListener("mousedown", close);
    document.addEventListener("keydown", esc);
    return () => {
      document.removeEventListener("mousedown", close);
      document.removeEventListener("keydown", esc);
    };
  }, [open]);

  const item = (label: string, action: () => void, disabled = false, danger = false) => (
    <li role="none">
      <button
        type="button"
        role="menuitem"
        disabled={disabled}
        className={danger ? "text-danger" : ""}
        onClick={() => {
          setOpen(false);
          action();
        }}
      >
        {label}
      </button>
    </li>
  );

  const running = website.status === "running";
  const busy = website.status === "starting" || website.status === "stopping";

  return (
    <div className="menu" ref={ref}>
      <button
        type="button"
        className={`btn btn-icon${bordered ? "" : " btn-ghost"}`}
        aria-label={`More actions for ${website.name}`}
        aria-haspopup="menu"
        aria-expanded={open}
        onClick={() => setOpen((o) => !o)}
      >
        <MoreHorizontal size={18} aria-hidden="true" />
      </button>
      {open && (
        <ul className="context-menu menu-list" role="menu">
          {item("Open", () => navigate(`/websites/${website.id}`))}
          {running
            ? item("Stop", () => setConfirm("stop"), busy)
            : item("Start", () => attempt("Cannot start the website", () => setPower(website.id, "start")), busy)}
          {item("Restart", () => attempt("Cannot restart the website", () => setPower(website.id, "restart")), !running)}
          {item("Make staging copy", () => attempt("Cannot make a staging copy", async () => {
            const { websiteId, credentials } = await createStagingCopy(website.id);
            // The API shows the passwords of the new database users once.
            if (credentials?.length) setStaging({ websiteId, credentials });
            else navigate(`/websites/${websiteId}`);
          }), !!website.stagingOf)}
          {item("Remove website", () => setConfirm("delete"), false, true)}
        </ul>
      )}
      {error && (
        <Modal title={error.title} onClose={() => setError(null)} footer={<button type="button" className="btn" onClick={() => setError(null)}>Close</button>}>
          <p>{error.text}</p>
        </Modal>
      )}
      {staging && (
        <Modal
          title="The staging copy is starting"
          wide
          onClose={() => setStaging(null)}
          footer={<button type="button" className="btn btn-primary" onClick={() => { const id = staging.websiteId; setStaging(null); navigate(`/websites/${id}`); }}>Open the staging copy</button>}
        >
          <p>The copy has its own copies of the databases, in the database server of each source database. Each database server has a new database user with access to the copies. Put these users in the configuration of the copy, so that it does not change the live data.</p>
          {staging.credentials.some((c) => c.engine === "valkey") && <p>The data of a Valkey database is not copied. The copy gets a new, empty Valkey database with its own number. Find the number on the Databases page of the copy.</p>}
          <p><strong>Copy the passwords now.</strong> The panel shows them only once.</p>
          {staging.credentials.map((c) => (
            <div key={c.user} style={{ marginTop: 12 }}>
              <p className="small">{ENGINE_NAME[c.engine]} user <code>{c.user}</code>{c.host && c.port && <> on <code>{c.host}:{c.port}</code></>} · {c.engine === "valkey" ? "new, empty database" : "databases"} {c.databases.map((d) => <code key={d} style={{ marginRight: 6 }}>{d}</code>)}</p>
              <CopyField value={c.password} label={`Password of ${c.user}`} />
            </div>
          ))}
        </Modal>
      )}
      {confirm === "stop" && (
        <Modal
          title={`Stop ${website.name}?`}
          onClose={() => setConfirm(null)}
          footer={
            <>
              <button type="button" className="btn" onClick={() => setConfirm(null)}>Cancel</button>
              <button type="button" className="btn btn-danger-solid" onClick={() => { setConfirm(null); attempt("Cannot stop the website", () => setPower(website.id, "stop")); }}>Stop website</button>
            </>
          }
        >
          <p>Visitors see an error page until you start the website again. The files and the databases stay.</p>
        </Modal>
      )}
      {confirm === "delete" && (
        <Modal
          title={`Remove ${website.name}?`}
          onClose={() => { setConfirm(null); setTyped(""); }}
          footer={
            <>
              <button type="button" className="btn" onClick={() => { setConfirm(null); setTyped(""); }}>Cancel</button>
              <button
                type="button"
                className="btn btn-danger-solid"
                disabled={typed !== website.name}
                onClick={() => {
                  setConfirm(null);
                  // A failed job shows in the notifications.
                  attempt("Cannot remove the website", () => deleteWebsite(website.id));
                  navigate("/websites");
                }}
              >
                Remove website
              </button>
            </>
          }
        >
          <p>This removes the container, the files and the databases of the website. The backups stay for the retention period.</p>
          <div className="field">
            <label htmlFor="confirm-name">Type <strong>{website.name}</strong> to confirm</label>
            <input id="confirm-name" className="input" value={typed} onChange={(e) => setTyped(e.target.value)} autoComplete="off" />
          </div>
        </Modal>
      )}
    </div>
  );
}
