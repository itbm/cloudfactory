import { AlertTriangle, MoreHorizontal, X } from "lucide-react";
import { useEffect, useId, useRef, useState, type ReactNode } from "react";
import { Link } from "react-router-dom";
import { useStore } from "../api/hooks";
import type { ApiError } from "../api/store";
import type { Job, Tone } from "../api/types";
import { shortAgo } from "../lib/format";
import { jobFailure, resolvedFailures } from "../lib/jobFailure";

export type { Tone };

/** A status always has a text label and a colour. Colour alone never shows a status. */
export function Status({ tone, children, className = "" }: { tone: Tone; children: ReactNode; className?: string }) {
  return <span className={`status status-${tone} ${className}`}>{children}</span>;
}

export function Meter({ value, label, inline = false, tone }: { value: number; label: string; inline?: boolean; tone?: "warning" | "danger" }) {
  const pct = Math.round(Math.min(1, Math.max(0, value)) * 100);
  return (
    <span
      className={`${inline ? "meter meter-inline" : "meter"}${tone ? ` meter-${tone}` : ""}`}
      role="meter"
      aria-label={label}
      aria-valuemin={0}
      aria-valuemax={100}
      aria-valuenow={pct}
    >
      <span style={{ width: `${Math.max(pct, pct > 0 ? 2 : 0)}%` }} />
    </span>
  );
}

export function Card({ title, actions, children, className = "", headingLevel = 2, id }: {
  title?: ReactNode;
  actions?: ReactNode;
  children: ReactNode;
  className?: string;
  headingLevel?: 2 | 3;
  id?: string;
}) {
  const Heading = headingLevel === 2 ? "h2" : "h3";
  return (
    <section className={`card ${className}`} id={id}>
      {(title || actions) && (
        <div className="card-header">
          {title && <Heading>{title}</Heading>}
          {actions}
        </div>
      )}
      {children}
    </section>
  );
}

export function Live() {
  return <span className="live">Live</span>;
}

export function PageHeader({ title, description, actions }: { title: ReactNode; description?: ReactNode; actions?: ReactNode }) {
  return (
    <div className="page-header">
      <div>
        <h1>{title}</h1>
        {description && <p>{description}</p>}
      </div>
      {actions && <div className="btn-group">{actions}</div>}
    </div>
  );
}

export function Empty({ title, children }: { title: string; children?: ReactNode }) {
  return (
    <div className="placeholder">
      <h2>{title}</h2>
      {children}
    </div>
  );
}

/** A modal dialog. Esc and the close button close it. Focus moves into the dialog. */
export function Modal({ title, onClose, children, footer, wide = false }: {
  title: string;
  onClose: () => void;
  children: ReactNode;
  footer?: ReactNode;
  wide?: boolean;
}) {
  const ref = useRef<HTMLDivElement>(null);
  const titleId = useId();
  // Most callers give a new onClose function on each render. The effect must not run again
  // for that, because it moves the focus to the first field: the focus then left the field
  // that the user typed in, after each character.
  const close = useRef(onClose);
  useEffect(() => { close.current = onClose; });
  useEffect(() => {
    const previous = document.activeElement as HTMLElement | null;
    const first = ref.current?.querySelector<HTMLElement>("input, select, textarea, button:not(.modal-close)");
    (first ?? ref.current)?.focus();
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") close.current();
      if (e.key === "Tab" && ref.current) {
        const items = ref.current.querySelectorAll<HTMLElement>("a[href], button:not(:disabled), input, select, textarea");
        if (!items.length) return;
        const firstItem = items[0];
        const last = items[items.length - 1];
        if (e.shiftKey && document.activeElement === firstItem) {
          e.preventDefault();
          last.focus();
        } else if (!e.shiftKey && document.activeElement === last) {
          e.preventDefault();
          firstItem.focus();
        }
      }
    };
    document.addEventListener("keydown", onKey);
    return () => {
      document.removeEventListener("keydown", onKey);
      previous?.focus();
    };
  }, []);
  return (
    <div className="palette-backdrop" onMouseDown={(e) => e.target === e.currentTarget && onClose()}>
      <div ref={ref} className={`modal${wide ? " modal-wide" : ""}`} role="dialog" aria-modal="true" aria-labelledby={titleId} tabIndex={-1}>
        <div className="modal-header">
          <h2 id={titleId}>{title}</h2>
          <button type="button" className="btn btn-icon btn-ghost modal-close" aria-label="Close" onClick={onClose}>
            <X size={18} aria-hidden="true" />
          </button>
        </div>
        <div className="modal-body">{children}</div>
        {footer && <div className="modal-footer">{footer}</div>}
      </div>
    </div>
  );
}

/** A form field with a label, a hint and an error. */
export function Field({ label, hint, error, children, id }: { label: string; hint?: ReactNode; error?: string; children: (props: { id: string; "aria-describedby"?: string; "aria-invalid"?: boolean }) => ReactNode; id?: string }) {
  const auto = useId();
  const fieldId = id ?? auto;
  const hintId = `${fieldId}-hint`;
  const errorId = `${fieldId}-error`;
  const describedBy = [hint ? hintId : "", error ? errorId : ""].filter(Boolean).join(" ") || undefined;
  return (
    <div className={`field${error ? " field-error" : ""}`}>
      <label htmlFor={fieldId}>{label}</label>
      {children({ id: fieldId, "aria-describedby": describedBy, "aria-invalid": error ? true : undefined })}
      {hint && <p className="hint" id={hintId}>{hint}</p>}
      {error && <p className="error" id={errorId}>{error}</p>}
    </div>
  );
}

/**
 * The message of an error. "shown" lists the fields that the form shows next to their inputs.
 * When all fields of the error are in it, the form already shows each message once, so the
 * banner does not repeat them.
 */
export function FormError({ error, shown }: { error: ApiError | null; shown?: string[] }) {
  if (!error) return null;
  const keys = Object.keys(error.fields ?? {});
  if (shown && keys.length > 0 && keys.every((k) => shown.includes(k))) return null;
  return (
    <div className="alert alert-danger" role="alert">
      <p>{error.message}</p>
    </div>
  );
}

export function Toggle({ checked, onChange, label, children, disabled }: { checked: boolean; onChange: (v: boolean) => void; label: string; children?: ReactNode; disabled?: boolean }) {
  return (
    <span className="toggle">
      <button type="button" role="switch" aria-checked={checked} aria-label={label} disabled={disabled} onClick={() => onChange(!checked)} />
      {children}
    </span>
  );
}

export interface TabItem {
  id: string;
  label: string;
  href?: string;
}

/** Tabs that are links (for pages) or buttons (inside a page). */
export function Tabs({ items, current, onSelect, label }: { items: TabItem[]; current: string; onSelect?: (id: string) => void; label: string }) {
  // Audit item U6: on a narrow screen the tab bar scrolls. A shadow at an edge shows that
  // more tabs are there, and the selected tab is always in view.
  const ref = useRef<HTMLElement>(null);
  const [more, setMore] = useState({ left: false, right: false });
  useEffect(() => {
    const nav = ref.current;
    if (!nav) return;
    const measure = () => setMore({ left: nav.scrollLeft > 2, right: nav.scrollLeft + nav.clientWidth < nav.scrollWidth - 2 });
    const selected = nav.querySelector<HTMLElement>('[aria-current="page"]');
    if (selected && nav.scrollWidth > nav.clientWidth) nav.scrollLeft = Math.max(0, selected.offsetLeft - (nav.clientWidth - selected.offsetWidth) / 2);
    measure();
    nav.addEventListener("scroll", measure, { passive: true });
    window.addEventListener("resize", measure);
    return () => {
      nav.removeEventListener("scroll", measure);
      window.removeEventListener("resize", measure);
    };
  }, [current, items.length]);
  return (
    <div className={`tabs-wrap${more.left ? " more-left" : ""}${more.right ? " more-right" : ""}`}>
      <nav className="tabs" aria-label={label} ref={ref}>
        {items.map((t) =>
          t.href ? (
            <Link key={t.id} to={t.href} className="tab" aria-current={t.id === current ? "page" : undefined}>{t.label}</Link>
          ) : (
            <button key={t.id} type="button" className="tab" aria-current={t.id === current ? "page" : undefined} onClick={() => onSelect?.(t.id)}>{t.label}</button>
          ),
        )}
      </nav>
    </div>
  );
}

/** A button that asks for confirmation before a change that cannot be undone. */
export function ConfirmButton({ children, title, message, confirm, onConfirm, className = "btn", danger = true, disabled = false }: {
  children: ReactNode;
  title: string;
  message: ReactNode;
  confirm: string;
  onConfirm: () => unknown;
  className?: string;
  danger?: boolean;
  disabled?: boolean;
}) {
  const [open, setOpen] = useState(false);
  return (
    <>
      <button type="button" className={className} disabled={disabled} onClick={() => setOpen(true)}>{children}</button>
      {open && (
        <Modal
          title={title}
          onClose={() => setOpen(false)}
          footer={
            <>
              <button type="button" className="btn" onClick={() => setOpen(false)}>Cancel</button>
              <button
                type="button"
                className={danger ? "btn btn-danger-solid" : "btn btn-primary"}
                onClick={() => {
                  setOpen(false);
                  onConfirm();
                }}
              >
                {confirm}
              </button>
            </>
          }
        >
          {message}
        </Modal>
      )}
    </>
  );
}

/** FR-1607: a label on each resource that Terraform controls. */
export function TerraformBadge() {
  return <span className="badge badge-neutral" title="Terraform controls this resource">Terraform</span>;
}

/** FR-1607: a warning before a user changes a resource that Terraform controls. */
export function TerraformNotice({ resource }: { resource: string }) {
  return (
    <div className="alert alert-warning" role="note">
      <p>
        <AlertTriangle size={16} aria-hidden="true" style={{ verticalAlign: "-3px", marginRight: 6 }} />
        Terraform controls {resource}. If you change it here, the next <code>terraform apply</code> changes it back. Change the Terraform code instead.
      </p>
    </div>
  );
}

/**
 * Audit item U2: the cause of a failed job. A customer gets a short text and a reference. An
 * administrator also gets the error of the server.
 */
export function JobError({ job, resolved = false }: { job: Job; resolved?: boolean }) {
  const role = useStore().session.user.role;
  if (job.state !== "failed") return null;
  const failure = jobFailure(job, role);
  return (
    <>
      {resolved
        ? <span className="cell-sub">A later try was successful. No action is necessary.</span>
        : <span className="cell-sub text-danger">{failure.text}</span>}
      <span className="cell-sub">Reference <code>{failure.reference}</code></span>
      {failure.details && (
        <details className="job-details">
          <summary>Details for administrators</summary>
          <pre>{failure.details}</pre>
        </details>
      )}
    </>
  );
}

/** Shows the progress of a job. The store changes the job live. */
export function JobRow({ job }: { job: Job }) {
  const jobs = useStore().jobs;
  if (job.state === "running" || job.state === "queued") {
    return (
      <li className="job-row">
        <div className="row">
          <span>{job.title}{job.ref && <> <code>{job.ref}</code></>}</span>
          <span className="muted">Step {Math.min(job.step + 1, job.steps.length)} of {job.steps.length}</span>
        </div>
        <Meter value={job.step / job.steps.length} label={`${job.title}: step ${job.step + 1} of ${job.steps.length}`} />
        <p className="detail">{job.steps[Math.min(job.step, job.steps.length - 1)]}</p>
      </li>
    );
  }
  const resolved = job.state === "failed" && resolvedFailures(jobs).has(job.id);
  return (
    <li className="job-row job-done">
      <span className="job-text">{job.title}{job.ref && <> <code>{job.ref}</code></>}<JobError job={job} resolved={resolved} /></span>
      <JobState job={job} resolved={resolved} />
    </li>
  );
}


export function JobState({ job, resolved = false }: { job: Job; resolved?: boolean }) {
  const when = job.finishedAt ? ` · ${shortAgo(job.finishedAt)}` : "";
  if (job.state === "succeeded") return <span className="text-success nowrap">Done{when}</span>;
  if (job.state === "failed") return resolved ? <span className="muted nowrap">Failed, then done{when}</span> : <span className="text-danger nowrap">Failed{when}</span>;
  return <span className="muted nowrap">Queued</span>;
}

export interface RowAction {
  label: string;
  onSelect: () => void;
  danger?: boolean;
  disabled?: boolean;
}

/**
 * Audit item U7: the actions of a table row in one menu. A row with many buttons was wider
 * than the table, and the last button was cut.
 */
export function RowMenu({ label, actions }: { label: string; actions: RowAction[] }) {
  const [open, setOpen] = useState(false);
  // A table scrolls its content, so a menu in the table would be cut. The menu has a fixed
  // position next to its button instead.
  const [place, setPlace] = useState<React.CSSProperties>({});
  const ref = useRef<HTMLDivElement>(null);
  const button = useRef<HTMLButtonElement>(null);
  useEffect(() => {
    if (!open) return;
    const close = (e: MouseEvent) => !ref.current?.contains(e.target as Node) && setOpen(false);
    const esc = (e: KeyboardEvent) => {
      if (e.key !== "Escape") return;
      setOpen(false);
      button.current?.focus();
    };
    const away = () => setOpen(false);
    document.addEventListener("mousedown", close);
    document.addEventListener("keydown", esc);
    window.addEventListener("resize", away);
    window.addEventListener("scroll", away, true);
    // The first action gets the focus, so that the arrow keys and Tab work in the menu.
    ref.current?.querySelector<HTMLButtonElement>('[role="menuitem"]:not(:disabled)')?.focus();
    return () => {
      document.removeEventListener("mousedown", close);
      document.removeEventListener("keydown", esc);
      window.removeEventListener("resize", away);
      window.removeEventListener("scroll", away, true);
    };
  }, [open]);
  if (actions.length === 0) return null;
  const toggle = () => {
    const r = button.current?.getBoundingClientRect();
    if (r) {
      const height = actions.length * 44 + 10;
      const below = r.bottom + 4 + height <= window.innerHeight;
      setPlace({ position: "fixed", left: "auto", right: Math.max(8, window.innerWidth - r.right), top: below ? r.bottom + 4 : "auto", bottom: below ? "auto" : window.innerHeight - r.top + 4 });
    }
    setOpen((o) => !o);
  };
  const move = (e: React.KeyboardEvent) => {
    if (e.key !== "ArrowDown" && e.key !== "ArrowUp") return;
    e.preventDefault();
    const items = [...(ref.current?.querySelectorAll<HTMLButtonElement>('[role="menuitem"]:not(:disabled)') ?? [])];
    const at = items.indexOf(document.activeElement as HTMLButtonElement);
    items[(at + (e.key === "ArrowDown" ? 1 : items.length - 1)) % items.length]?.focus();
  };
  return (
    <div className="menu" ref={ref}>
      <button ref={button} type="button" className="btn btn-icon btn-ghost" aria-label={label} aria-haspopup="menu" aria-expanded={open} onClick={toggle}>
        <MoreHorizontal size={18} aria-hidden="true" />
      </button>
      {open && (
        <ul className="context-menu menu-list" role="menu" aria-label={label} onKeyDown={move} style={place}>
          {actions.map((a) => (
            <li key={a.label} role="none">
              <button type="button" role="menuitem" disabled={a.disabled} className={a.danger ? "text-danger" : ""} onClick={() => { setOpen(false); a.onSelect(); }}>{a.label}</button>
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}

/**
 * Audit item U6: the frame of a table. On a wide screen, the table scrolls in the frame when
 * it does not fit. On a phone, each row shows as a card: each cell is on its own line, with
 * the heading of its column before it.
 */
export function TableWrap({ children, className = "" }: { children: ReactNode; className?: string }) {
  const ref = useRef<HTMLDivElement>(null);
  // The headings come from the table itself, so each table needs no second list of them.
  useEffect(() => {
    const table = ref.current?.querySelector("table");
    if (!table) return;
    const headings = [...table.querySelectorAll("thead th")].map((th) => (th.querySelector(".visually-hidden") ? "" : th.textContent?.trim() ?? ""));
    for (const row of table.querySelectorAll("tbody tr")) {
      [...row.children].forEach((cell, i) => {
        const label = (cell as HTMLTableCellElement).colSpan > 1 ? "" : headings[i] ?? "";
        if (label) cell.setAttribute("data-label", label);
        else cell.removeAttribute("data-label");
      });
    }
  });
  return <div ref={ref} className={`table-wrap table-cards${className ? ` ${className}` : ""}`}>{children}</div>;
}
