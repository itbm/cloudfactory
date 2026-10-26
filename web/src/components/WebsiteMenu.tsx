import { MoreHorizontal } from "lucide-react";
import { useEffect, useRef, useState } from "react";
import { useNavigate } from "react-router-dom";
import { features } from "../api/mode";
import { createStagingCopy, deleteWebsite, setPower } from "../api/store";
import type { Website } from "../api/types";
import { Modal } from "./ui";

/** The actions menu of a website (FR-204, FR-205). */
export function WebsiteMenu({ website, bordered = false }: { website: Website; bordered?: boolean }) {
  const [open, setOpen] = useState(false);
  const [confirm, setConfirm] = useState<null | "stop" | "delete">(null);
  const [typed, setTyped] = useState("");
  const [error, setError] = useState("");
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
          {features.files && item("File manager", () => navigate(`/websites/${website.id}/files`))}
          {running
            ? item("Stop", () => setConfirm("stop"), busy)
            : item("Start", () => void setPower(website.id, "start"), busy)}
          {item("Restart", () => void setPower(website.id, "restart"), !running)}
          {item("Make staging copy", async () => {
            try {
              const { websiteId } = await createStagingCopy(website.id);
              navigate(`/websites/${websiteId}`);
            } catch (e) {
              setError((e as Error).message);
            }
          }, !!website.stagingOf)}
          {item("Remove website", () => setConfirm("delete"), false, true)}
        </ul>
      )}
      {error && (
        <Modal title="Cannot make a staging copy" onClose={() => setError("")} footer={<button type="button" className="btn" onClick={() => setError("")}>Close</button>}>
          <p>{error}</p>
        </Modal>
      )}
      {confirm === "stop" && (
        <Modal
          title={`Stop ${website.name}?`}
          onClose={() => setConfirm(null)}
          footer={
            <>
              <button type="button" className="btn" onClick={() => setConfirm(null)}>Cancel</button>
              <button type="button" className="btn btn-danger-solid" onClick={() => { setConfirm(null); void setPower(website.id, "stop"); }}>Stop website</button>
            </>
          }
        >
          <p>Visitors see an error page until you start the website again. The files, databases and email stay.</p>
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
                  void deleteWebsite(website.id);
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
