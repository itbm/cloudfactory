import { AlertTriangle, X } from "lucide-react";
import { useEffect, useId, useRef, useState, type ReactNode } from "react";
import { Link } from "react-router-dom";
import type { ApiError } from "../api/store";
import type { Job, Tone } from "../api/types";
import { shortAgo } from "../lib/format";

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
  useEffect(() => {
    const previous = document.activeElement as HTMLElement | null;
    const first = ref.current?.querySelector<HTMLElement>("input, select, textarea, button:not(.modal-close)");
    (first ?? ref.current)?.focus();
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") onClose();
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
  }, [onClose]);
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

export function FormError({ error }: { error: ApiError | null }) {
  if (!error) return null;
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
  return (
    <nav className="tabs" aria-label={label}>
      {items.map((t) =>
        t.href ? (
          <Link key={t.id} to={t.href} className="tab" aria-current={t.id === current ? "page" : undefined}>{t.label}</Link>
        ) : (
          <button key={t.id} type="button" className="tab" aria-current={t.id === current ? "page" : undefined} onClick={() => onSelect?.(t.id)}>{t.label}</button>
        ),
      )}
    </nav>
  );
}

/** A button that asks for confirmation before a change that cannot be undone. */
export function ConfirmButton({ children, title, message, confirm, onConfirm, className = "btn", danger = true }: {
  children: ReactNode;
  title: string;
  message: ReactNode;
  confirm: string;
  onConfirm: () => unknown;
  className?: string;
  danger?: boolean;
}) {
  const [open, setOpen] = useState(false);
  return (
    <>
      <button type="button" className={className} onClick={() => setOpen(true)}>{children}</button>
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

/** Shows the progress of a job. The store changes the job live. */
export function JobRow({ job }: { job: Job }) {
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
  return (
    <li className="job-row job-done">
      <span>{job.title}{job.ref && <> <code>{job.ref}</code></>}{job.error && <span className="cell-sub text-danger">{job.error}</span>}</span>
      <JobState job={job} />
    </li>
  );
}


export function JobState({ job }: { job: Job }) {
  const when = job.finishedAt ? ` · ${shortAgo(job.finishedAt)}` : "";
  if (job.state === "succeeded") return <span className="text-success nowrap">Done{when}</span>;
  if (job.state === "failed") return <span className="text-danger nowrap">Failed{when}</span>;
  return <span className="muted nowrap">Queued</span>;
}
