import { Download, File, FileArchive, Folder, Link2, LoaderCircle, Upload, X } from "lucide-react";
import { useRef, useState } from "react";
import { useAction, useLoad } from "../api/hooks";
import { ApiError, MAX_UPLOAD_BYTES, deleteFile, extractArchive, listFiles, makeArchive, makeDirectory, moveFile, readFile, setFileMode, writeFile } from "../api/store";
import type { FileEntry, Job, Website } from "../api/types";
import { Card, Field, FormError, Modal, RowMenu, TableWrap } from "../components/ui";
import { bytes, count, relativeTime } from "../lib/format";

/** The largest file that the editor opens. A larger text file is for SFTP. */
const MAX_EDIT_BYTES = 1024 * 1024;

const join = (dir: string, name: string) => `${dir === "/" ? "" : dir}/${name}`;
const parentOf = (path: string) => path.slice(0, path.lastIndexOf("/")) || "/";

/** The archives that the server extracts (FileController::ARCHIVE). */
export const isArchive = (name: string) => /\.(zip|tar|tar\.gz|tgz|tar\.bz2|tbz2)$/i.test(name);

/** The formats of a new archive. */
const FORMATS = [
  { ext: ".zip", label: "ZIP (.zip)" },
  { ext: ".tar.gz", label: "Compressed tar (.tar.gz)" },
  { ext: ".tar", label: "Tar with no compression (.tar)" },
] as const;

/** Base64 of the bytes of a file. The loop keeps the argument list of String.fromCharCode small. */
export function toBase64(data: Uint8Array): string {
  let text = "";
  for (let i = 0; i < data.length; i += 0x8000) text += String.fromCharCode(...data.subarray(i, i + 0x8000));
  return btoa(text);
}

function save(name: string, content: string, encoding: "utf8" | "base64") {
  const data = encoding === "base64" ? Uint8Array.from(atob(content), (c) => c.charCodeAt(0)) : new TextEncoder().encode(content);
  const url = URL.createObjectURL(new Blob([data]));
  const a = document.createElement("a");
  a.href = url;
  a.download = name;
  a.click();
  URL.revokeObjectURL(url);
}

/** "1 file", "12 files". */
const plural = (n: number, one: string, many: string) => `${count(n)} ${n === 1 ? one : many}`;

/** The text of the files, folders and links of a summary, for example "12 files and 3 folders". */
function contents(s: NonNullable<Job["result"]>): string {
  const parts = [plural(s.files, "file", "files"), plural(s.directories, "folder", "folders")];
  if (s.links) parts.push(plural(s.links, "link", "links"));
  return `${parts.slice(0, -1).join(", ")} and ${parts.at(-1)}`;
}

/** The permission bits as text, for example "rwxr-xr-x" for 0755. */
export function symbolic(mode: number): string {
  return [6, 3, 0].map((shift) => {
    const bits = (mode >> shift) & 7;
    return `${bits & 4 ? "r" : "-"}${bits & 2 ? "w" : "-"}${bits & 1 ? "x" : "-"}`;
  }).join("");
}

/** A mode of 3 octal digits, with a 0 before them or not (the rule "fileMode"). */
const validMode = (text: string) => /^0?[0-7]{3}$/.test(text);
const toMode = (text: string) => parseInt(text, 8);
const toText = (mode: number) => mode.toString(8).padStart(4, "0");

/** A grid of check boxes for the permission bits, and the octal value. */
function PermissionGrid({ legend, value, onChange, folder, error }: { legend: string; value: string; onChange: (v: string) => void; folder: boolean; error?: string }) {
  const mode = validMode(value) ? toMode(value) : 0;
  const id = legend.toLowerCase().replace(/[^a-z]+/g, "-");
  const flip = (bit: number) => onChange(toText(mode ^ bit));
  return (
    <fieldset className="permissions">
      <legend>{legend}</legend>
      <div className="permissions-body">
        <table className="permission-grid">
          <thead>
            <tr><th scope="col"><span className="visually-hidden">Who</span></th><th scope="col">Read</th><th scope="col">Write</th><th scope="col">{folder ? "Open" : "Run"}</th></tr>
          </thead>
          <tbody>
            {(["Owner", "Group", "Others"] as const).map((who, i) => (
              <tr key={who}>
                <th scope="row">{who}</th>
                {[4, 2, 1].map((bit) => {
                  const mask = bit << (6 - 3 * i);
                  const what = bit === 4 ? "read" : bit === 2 ? "write" : folder ? "open" : "run";
                  return (
                    <td key={bit}>
                      <input type="checkbox" checked={(mode & mask) !== 0} aria-label={`${who}: ${what} (${legend.toLowerCase()})`} onChange={() => flip(mask)} />
                    </td>
                  );
                })}
              </tr>
            ))}
          </tbody>
        </table>
        <Field label={`Octal value (${legend.toLowerCase()})`} id={`mode-${id}`} error={error} hint={validMode(value) ? <code>{symbolic(mode)}</code> : undefined}>
          {(p) => <input {...p} className="input mono mode-input" value={value} inputMode="numeric" maxLength={4} onChange={(e) => onChange(e.target.value.trim())} spellCheck={false} />}
        </Field>
      </div>
    </fieldset>
  );
}

type Dialog =
  | { kind: "directory" | "file" }
  | { kind: "move"; entry: FileEntry }
  | { kind: "permissions"; entry: FileEntry }
  | { kind: "archive"; entries: FileEntry[] }
  | { kind: "extract"; entry: FileEntry }
  | { kind: "remove"; entry: FileEntry };

/** A message about an archive job: in progress, done or failed. */
type Notice = { id: string; tone: "progress" | "info" | "danger"; text: string; skipped?: string[] };

/**
 * The file manager of one website. The server does each operation as the Unix user of the
 * website, in the directory of the website. Thus the file manager cannot go out of that directory.
 */
export function FileManager({ website }: { website: Website }) {
  const id = website.id;
  const root = website.runtime.documentRoot && website.runtime.documentRoot !== "." ? `/${website.runtime.documentRoot}` : "/";
  const [dir, setDir] = useState("/");
  const list = useLoad(() => listFiles(id, dir), [id, dir]);
  const [dialog, setDialog] = useState<Dialog | null>(null);
  const [name, setName] = useState("");
  const [format, setFormat] = useState<(typeof FORMATS)[number]["ext"]>(".zip");
  const [modes, setModes] = useState({ mode: "", fileMode: "0644", recursive: false });
  const [overwrite, setOverwrite] = useState(false);
  const [editing, setEditing] = useState<{ entry: FileEntry; content: string } | null>(null);
  const [selected, setSelected] = useState<Set<string>>(new Set());
  const [notices, setNotices] = useState<Notice[]>([]);
  const picker = useRef<HTMLInputElement>(null);

  const done = () => list.reload();
  const openFolder = (path: string) => {
    setDir(path);
    setSelected(new Set());
  };
  const notice = (n: Notice) => setNotices((all) => [n, ...all.filter((x) => x.id !== n.id)].slice(0, 3));
  /** Follows an archive job: a message while it runs, then the summary or the error. */
  const follow = (key: string, running: string, job: Promise<Job>, text: (j: Job) => Pick<Notice, "text" | "skipped">) => {
    notice({ id: key, tone: "progress", text: running });
    void job.then((j) => {
      notice(j.state === "succeeded"
        ? { id: key, tone: "info", ...text(j) }
        : { id: key, tone: "danger", text: `${j.title} ${j.ref ?? ""} failed. ${j.error ?? "The server could not do the job."}` });
      done();
    }, (e: unknown) => notice({ id: key, tone: "danger", text: e instanceof Error ? e.message : "The panel lost the job." }));
  };

  const create = useAction(async (kind: "directory" | "file", path: string) => {
    if (kind === "directory") await makeDirectory(id, path);
    else await writeFile(id, path, "", { createOnly: true });
  });
  const move = useAction((from: string, to: string) => moveFile(id, from, to));
  const remove = useAction((path: string) => deleteFile(id, path).then(done));
  const chmod = useAction(async (entry: FileEntry) => {
    const folder = entry.type === "directory";
    const r = await setFileMode(id, entry.path, modes.mode, folder && modes.recursive ? { recursive: true, fileMode: modes.fileMode } : {});
    if (folder && modes.recursive) notice({ id: `mode:${entry.path}`, tone: "info", text: `Changed the permissions of ${plural(r.changed, "file or folder", "files and folders")} in ${entry.path}.` });
  });
  const archive = useAction(async (paths: string[], path: string) => {
    const { done: job } = await makeArchive(id, paths, path);
    const archiveName = path.slice(path.lastIndexOf("/") + 1);
    follow(`archive:${path}`, `Making the archive ${archiveName}…`, job, (j) => ({
      text: `The archive ${archiveName} is ready: ${contents(j.result!)}, ${bytes(j.result!.sizeBytes)}.`
        + (j.result!.skipped ? ` The archive does not have ${plural(j.result!.skipped, "special file", "special files")} (FIFOs, sockets or devices).` : ""),
      skipped: j.result!.skippedNames,
    }));
  });
  const extract = useAction(async (entry: FileEntry, to: string, replace: boolean) => {
    const { done: job } = await extractArchive(id, entry.path, to, replace);
    follow(`extract:${entry.path}`, `Extracting ${entry.name} to ${to}…`, job, (j) => {
      const s = j.result!;
      let text = `Extracted ${entry.name} to ${to}: ${contents(s)}.`;
      if (s.kept) text += ` ${plural(s.kept, "file exists", "files exist")} already and did not change.`;
      if (s.skipped) text += ` The server skipped ${plural(s.skipped, "entry", "entries")}: names that go out of the folder, special files, or names that are a folder in one place and a file in the other.`;
      return { text, skipped: s.skippedNames };
    });
  });
  const open = useAction(async (entry: FileEntry) => {
    if (entry.sizeBytes > MAX_EDIT_BYTES) throw new ApiError(`${entry.name} is too large for the editor. Download the file, or use SFTP.`);
    const r = await readFile(id, entry.path);
    if (r.encoding !== "utf8") throw new ApiError(`${entry.name} is not a text file. Download the file to see it.`);
    setEditing({ entry, content: r.content });
  });
  const download = useAction(async (entry: FileEntry) => {
    const r = await readFile(id, entry.path);
    save(entry.name, r.content, r.encoding);
  });
  const write = useAction(async () => {
    if (!editing) return;
    await writeFile(id, editing.entry.path, editing.content);
    setEditing(null);
    done();
  });
  const upload = useAction(async (files: File[]) => {
    const large = files.find((f) => f.size > MAX_UPLOAD_BYTES);
    if (large) throw new ApiError(`${large.name} is larger than ${bytes(MAX_UPLOAD_BYTES)}. Use SFTP for large files.`);
    try {
      for (const f of files) await writeFile(id, join(dir, f.name), toBase64(new Uint8Array(await f.arrayBuffer())), { encoding: "base64" });
    } finally {
      done();
    }
  });

  const closeDialog = () => {
    setDialog(null);
    setName("");
    setOverwrite(false);
    for (const a of [create, move, chmod, archive, extract]) a.clear();
  };
  const show = (d: Dialog) => {
    closeDialog();
    setDialog(d);
    if (d.kind === "move") setName(d.entry.name);
    if (d.kind === "extract") setName(parentOf(d.entry.path));
    if (d.kind === "archive") setName(d.entries.length === 1 ? d.entries[0].name.replace(/\.[^.]+$/, "") || d.entries[0].name : dir === "/" ? "website" : dir.slice(dir.lastIndexOf("/") + 1));
    if (d.kind === "permissions") {
      const folder = d.entry.type === "directory";
      setModes({ mode: d.entry.mode || (folder ? "0755" : "0644"), fileMode: "0644", recursive: false });
    }
  };
  const submitDialog = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!dialog) return;
    const value = name.trim();
    let result: { ok: boolean } = { ok: false };
    switch (dialog.kind) {
      case "move":
        result = await move.run(dialog.entry.path, value.startsWith("/") ? value : join(parentOf(dialog.entry.path), value));
        break;
      case "directory":
      case "file":
        result = await create.run(dialog.kind, join(dir, value));
        break;
      case "permissions":
        result = await chmod.run(dialog.entry);
        break;
      case "archive":
        result = await archive.run(dialog.entries.map((f) => f.path), join(dir, value + format));
        if (result.ok) setSelected(new Set());
        break;
      case "extract":
        result = await extract.run(dialog.entry, value.startsWith("/") ? value : join(dir, value), overwrite);
        break;
    }
    if (result.ok) {
      closeDialog();
      done();
    }
  };

  const parts = dir.split("/").filter(Boolean);
  const entries = list.data?.path === dir ? list.data.entries : [];
  const chosen = entries.filter((f) => selected.has(f.path));
  const toggle = (path: string) => setSelected((s) => {
    const next = new Set(s);
    if (!next.delete(path)) next.add(path);
    return next;
  });
  const actionError = upload.error ?? remove.error ?? open.error ?? download.error;
  const dialogError = create.error ?? move.error ?? chmod.error ?? archive.error ?? extract.error;
  const folder = dialog?.kind === "permissions" && dialog.entry.type === "directory";
  const lockedOut = (text: string) => validMode(text) && (toMode(text) & 0o500) !== 0o500;
  const modeError = dialog?.kind === "permissions"
    ? !validMode(modes.mode) ? "Enter 3 octal digits, for example 644 or 0755." : folder && lockedOut(modes.mode) ? "The owner must keep Read and Open for a folder." : undefined
    : undefined;
  const fileModeError = folder && modes.recursive && !validMode(modes.fileMode) ? "Enter 3 octal digits, for example 644." : undefined;
  const busy = create.busy || move.busy || chmod.busy || archive.busy || extract.busy;
  const canSubmit = dialog?.kind === "permissions" ? !modeError && !fileModeError && !busy : dialog?.kind === "remove" ? !remove.busy : !!name.trim() && !busy;

  const submitLabel = dialog ? { directory: "Create", file: "Create", move: "Rename", permissions: "Change", archive: "Make archive", extract: "Extract", remove: "Remove" }[dialog.kind] : "";
  const dialogTitle = !dialog ? "" : {
    directory: "New folder",
    file: "New file",
    move: `Rename ${"entry" in dialog ? dialog.entry.name : ""}`,
    permissions: `Permissions of ${"entry" in dialog ? dialog.entry.name : ""}`,
    archive: "Make an archive",
    extract: `Extract ${"entry" in dialog ? dialog.entry.name : ""}`,
    remove: `Remove ${"entry" in dialog ? dialog.entry.name : ""}?`,
  }[dialog.kind];

  return (
    <Card
      title="Files"
      actions={
        <div className="btn-group">
          <button type="button" className="btn" onClick={() => show({ kind: "directory" })}>New folder</button>
          <button type="button" className="btn" onClick={() => show({ kind: "file" })}>New file</button>
          <button type="button" className="btn btn-primary" disabled={upload.busy} onClick={() => picker.current?.click()}>
            <Upload size={16} aria-hidden="true" /> {upload.busy ? "Uploading…" : "Upload"}
          </button>
          <input
            ref={picker}
            type="file"
            multiple
            hidden
            aria-label="Upload files"
            onChange={(e) => {
              const files = Array.from(e.target.files ?? []);
              e.target.value = "";
              if (files.length) void upload.run(files);
            }}
          />
        </div>
      }
    >
      <div className="card-body" style={{ paddingBottom: 8 }}>
        <FormError error={list.error ?? actionError} />
        {notices.length > 0 && (
          <div className="file-notices" aria-live="polite">
            {notices.map((n) => (
              <div key={n.id} className={`alert file-notice ${n.tone === "danger" ? "alert-danger" : n.tone === "info" ? "alert-info" : ""}`} role={n.tone === "danger" ? "alert" : "status"}>
                {n.tone === "progress" && <LoaderCircle size={18} aria-hidden="true" />}
                <div className="file-notice-text">
                  <p>{n.text}</p>
                  {n.skipped && n.skipped.length > 0 && (
                    <details>
                      <summary>Skipped entries</summary>
                      <ul className="notice-list mono small">{n.skipped.map((s) => <li key={s}>{s}</li>)}</ul>
                    </details>
                  )}
                </div>
                {n.tone !== "progress" && (
                  <button type="button" className="btn btn-sm btn-ghost btn-icon" aria-label="Close the message" title="Close" onClick={() => setNotices((all) => all.filter((x) => x.id !== n.id))}>
                    <X size={16} aria-hidden="true" />
                  </button>
                )}
              </div>
            ))}
          </div>
        )}
        <nav aria-label="Folder" className="file-path">
          <button type="button" className="btn btn-sm btn-ghost" aria-current={parts.length === 0 ? "location" : undefined} onClick={() => openFolder("/")}>Website files</button>
          {parts.map((p, i) => {
            const path = `/${parts.slice(0, i + 1).join("/")}`;
            return (
              <span key={path}>
                <span aria-hidden="true">/ </span>
                <button type="button" className="btn btn-sm btn-ghost" aria-current={i === parts.length - 1 ? "location" : undefined} onClick={() => openFolder(path)}>{p}</button>
              </span>
            );
          })}
        </nav>
        {chosen.length > 0 && (
          <div className="selection-bar" role="region" aria-label="Selected files">
            <span>{plural(chosen.length, "item", "items")} selected</span>
            <div className="btn-group">
              <button type="button" className="btn btn-sm btn-primary" onClick={() => show({ kind: "archive", entries: chosen })}>
                <FileArchive size={16} aria-hidden="true" /> Make archive
              </button>
              <button type="button" className="btn btn-sm btn-ghost" onClick={() => setSelected(new Set())}>Clear selection</button>
            </div>
          </div>
        )}
      </div>
      <TableWrap>
        <table aria-busy={list.loading} className="file-table">
          <thead>
            <tr>
              <th scope="col" className="select-cell">
                <input
                  type="checkbox"
                  aria-label="Select all files and folders"
                  checked={entries.length > 0 && chosen.length === entries.length}
                  ref={(el) => { if (el) el.indeterminate = chosen.length > 0 && chosen.length < entries.length; }}
                  onChange={() => setSelected(chosen.length === entries.length ? new Set() : new Set(entries.map((f) => f.path)))}
                  disabled={entries.length === 0}
                />
              </th>
              <th scope="col">Name</th><th scope="col">Size</th><th scope="col">Permissions</th><th scope="col">Changed</th><th scope="col"><span className="visually-hidden">Actions</span></th>
            </tr>
          </thead>
          <tbody>
            {dir !== "/" && (
              <tr>
                <td colSpan={6}>
                  <button type="button" className="btn btn-sm btn-ghost" onClick={() => openFolder(parentOf(dir))}>.. (up one folder)</button>
                </td>
              </tr>
            )}
            {entries.map((f) => (
              <tr key={f.path} className={selected.has(f.path) ? "selected" : undefined}>
                <td className="select-cell"><input type="checkbox" checked={selected.has(f.path)} aria-label={`Select ${f.name}`} onChange={() => toggle(f.path)} /></td>
                <td>
                  {f.type === "directory" ? (
                    <button type="button" className="btn btn-sm btn-ghost" onClick={() => openFolder(f.path)}>
                      <Folder size={16} aria-hidden="true" /> <span className="visually-hidden">Folder </span>{f.name}
                    </button>
                  ) : (
                    <span style={{ display: "inline-flex", gap: 8, alignItems: "center", paddingLeft: 12 }}>
                      {f.type === "link" ? <Link2 size={16} aria-hidden="true" /> : isArchive(f.name) ? <FileArchive size={16} aria-hidden="true" /> : <File size={16} aria-hidden="true" />} {f.name}
                      {f.type === "link" && f.target && <span className="muted">to {f.target}</span>}
                    </span>
                  )}
                  {f.path === root && <span className="cell-sub" style={{ paddingLeft: 12 }}>The web server serves this folder.</span>}
                </td>
                <td>{f.type === "file" ? bytes(f.sizeBytes) : "–"}</td>
                <td>{f.type === "link" || !f.mode ? "–" : <code title={symbolic(toMode(f.mode))}>{f.mode}</code>}</td>
                <td>{relativeTime(f.modifiedAt)}</td>
                <td>
                  <div className="btn-group" style={{ justifyContent: "flex-end", flexWrap: "nowrap" }}>
                    {f.type === "file" && isArchive(f.name) && (
                      <button type="button" className="btn btn-sm" onClick={() => show({ kind: "extract", entry: f })}>Extract<span className="visually-hidden"> {f.name}</span></button>
                    )}
                    {f.type === "file" && !isArchive(f.name) && (
                      <button type="button" className="btn btn-sm" disabled={open.busy} onClick={() => void open.run(f)}>Edit<span className="visually-hidden"> {f.name}</span></button>
                    )}
                    {f.type === "file" && (
                      <button type="button" className="btn btn-sm btn-icon" disabled={download.busy} aria-label={`Download ${f.name}`} title="Download" onClick={() => void download.run(f)}><Download size={16} aria-hidden="true" /></button>
                    )}
                    <RowMenu
                      label={`More actions for ${f.name}`}
                      actions={[
                        { label: "Rename", onSelect: () => show({ kind: "move", entry: f }) },
                        ...(f.type === "link" ? [] : [{ label: "Permissions", onSelect: () => show({ kind: "permissions", entry: f }) }]),
                        { label: "Make archive", onSelect: () => show({ kind: "archive", entries: [f] }) },
                        { label: "Remove", danger: true, onSelect: () => show({ kind: "remove", entry: f }) },
                      ]}
                    />
                  </div>
                </td>
              </tr>
            ))}
            {entries.length === 0 && <tr><td colSpan={6} className="muted">{list.loading ? "Loading…" : list.error ? "The panel could not read this folder." : "This folder is empty."}</td></tr>}
          </tbody>
        </table>
      </TableWrap>
      <p className="card-body muted small" style={{ paddingTop: 12 }}>
        {list.data?.truncated && <>This folder has more files than the panel shows. Use SFTP to see all files. </>}
        The file manager works in the files of this website only, as the Unix user <code>{website.unixUser}</code>. For files larger than {bytes(MAX_UPLOAD_BYTES)}, use SFTP or an archive.
      </p>

      {dialog && (
        <Modal
          title={dialogTitle}
          onClose={closeDialog}
          footer={
            <>
              <button type="button" className="btn" onClick={closeDialog}>Cancel</button>
              {dialog.kind === "remove" ? (
                <button type="button" className="btn btn-danger-solid" disabled={!canSubmit} onClick={() => { const path = dialog.entry.path; closeDialog(); void remove.run(path); }}>Remove</button>
              ) : (
                <button type="submit" form="file-dialog" className="btn btn-primary" disabled={!canSubmit}>{submitLabel}</button>
              )}
            </>
          }
        >
          {dialog.kind === "remove" ? (
            <p>{dialog.entry.type === "directory" ? "The server removes the folder and all files in it." : "The server removes the file."} To get it back, you must restore it from a backup.</p>
          ) : (
            <form id="file-dialog" noValidate onSubmit={(e) => void submitDialog(e)}>
              <FormError error={dialogError && !Object.keys(dialogError.fields).length ? dialogError : null} />
              {dialog.kind === "permissions" ? (
                <>
                  <PermissionGrid legend={folder ? "Folders" : "File"} value={modes.mode} folder={folder} error={modeError ?? dialogError?.fields.mode} onChange={(mode) => setModes({ ...modes, mode })} />
                  {folder && (
                    <label className="check">
                      <input type="checkbox" checked={modes.recursive} onChange={(e) => setModes({ ...modes, recursive: e.target.checked })} />
                      Also change all folders and files in this folder
                    </label>
                  )}
                  {folder && modes.recursive && (
                    <PermissionGrid legend="Files" value={modes.fileMode} folder={false} error={fileModeError ?? dialogError?.fields.fileMode} onChange={(fileMode) => setModes({ ...modes, fileMode })} />
                  )}
                  <p className="muted small">
                    The website runs as the Unix user <code>{website.unixUser}</code>, which owns all files of the website. The group and other permissions do not give other websites access.
                    {folder && modes.recursive && " The server does not change links."}
                  </p>
                </>
              ) : dialog.kind === "archive" ? (
                <>
                  <p>
                    {dialog.entries.length === 1 ? <>The archive has <code>{dialog.entries[0].name}</code></> : <>The archive has {plural(dialog.entries.length, "item", "items")}: {dialog.entries.slice(0, 5).map((f) => f.name).join(", ")}{dialog.entries.length > 5 ? " and more" : ""}</>}
                    {dialog.entries.some((f) => f.type === "directory") && " with all files in the folders"}. The server saves it in {dir === "/" ? "the website files" : <code>{dir}</code>}.
                  </p>
                  <Field label="Name of the archive" error={dialogError?.fields.path ?? dialogError?.fields.paths}>
                    {(p) => <input {...p} className="input mono" value={name} onChange={(e) => setName(e.target.value)} autoCapitalize="none" spellCheck={false} />}
                  </Field>
                  <Field label="Format">
                    {(p) => (
                      <select {...p} className="select" value={format} onChange={(e) => setFormat(e.target.value as typeof format)}>
                        {FORMATS.map((f) => <option key={f.ext} value={f.ext}>{f.label}</option>)}
                      </select>
                    )}
                  </Field>
                  <p className="muted small">The archive file is <code>{join(dir, name.trim() + format)}</code>. Large archives can take some minutes.</p>
                </>
              ) : dialog.kind === "extract" ? (
                <>
                  <Field
                    label="Extract to the folder"
                    hint={<>A path that starts with <code>/</code>, or a folder in <code>{dir}</code>. The server makes the folder when it does not exist.</>}
                    error={dialogError?.fields.to}
                  >
                    {(p) => <input {...p} className="input mono" value={name} onChange={(e) => setName(e.target.value)} autoCapitalize="none" spellCheck={false} />}
                  </Field>
                  <label className="check">
                    <input type="checkbox" checked={overwrite} onChange={(e) => setOverwrite(e.target.checked)} />
                    Replace files that exist
                  </label>
                  <p className="muted small">
                    {overwrite ? "The server replaces each file that the archive has. You cannot undo this. Make a backup first." : "Files that exist stay as they are. The server writes only the new files."}
                    {" "}The server does not write outside the folder. An archive with a password is not possible.
                  </p>
                </>
              ) : (
                <Field
                  label="Name"
                  hint={dialog.kind === "move" ? <>To move it, type a full path that starts with <code>/</code>, for example <code>/public/old.txt</code>.</> : undefined}
                  error={dialogError ? Object.values(dialogError.fields)[0] : undefined}
                >
                  {(p) => <input {...p} className="input mono" value={name} onChange={(e) => setName(e.target.value)} autoCapitalize="none" spellCheck={false} />}
                </Field>
              )}
            </form>
          )}
        </Modal>
      )}
      {editing && (
        <Modal
          wide
          title={`Edit ${editing.entry.path}`}
          onClose={() => { setEditing(null); write.clear(); }}
          footer={
            <>
              <button type="button" className="btn" onClick={() => { setEditing(null); write.clear(); }}>Cancel</button>
              <button type="button" className="btn btn-primary" disabled={write.busy} onClick={() => void write.run()}>{write.busy ? "Saving…" : "Save"}</button>
            </>
          }
        >
          <FormError error={write.error} />
          <label className="visually-hidden" htmlFor="editor">Content of {editing.entry.name}</label>
          <textarea id="editor" className="textarea editor mono" value={editing.content} onChange={(e) => setEditing({ ...editing, content: e.target.value })} spellCheck={false} />
        </Modal>
      )}
    </Card>
  );
}
