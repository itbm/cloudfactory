import { Download, File, Folder, Link2, Upload } from "lucide-react";
import { useRef, useState } from "react";
import { useAction, useLoad } from "../api/hooks";
import { ApiError, MAX_UPLOAD_BYTES, deleteFile, listFiles, makeDirectory, moveFile, readFile, writeFile } from "../api/store";
import type { FileEntry, Website } from "../api/types";
import { Card, ConfirmButton, Field, FormError, Modal, TableWrap } from "../components/ui";
import { bytes, relativeTime } from "../lib/format";

/** The largest file that the editor opens. A larger text file is for SFTP. */
const MAX_EDIT_BYTES = 1024 * 1024;

const join = (dir: string, name: string) => `${dir === "/" ? "" : dir}/${name}`;
const parentOf = (path: string) => path.slice(0, path.lastIndexOf("/")) || "/";

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

type Dialog = { kind: "directory" | "file" } | { kind: "move"; entry: FileEntry };

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
  const [editing, setEditing] = useState<{ entry: FileEntry; content: string } | null>(null);
  const picker = useRef<HTMLInputElement>(null);

  const done = () => list.reload();
  const create = useAction(async (kind: "directory" | "file", path: string) => {
    if (kind === "directory") await makeDirectory(id, path);
    else await writeFile(id, path, "", { createOnly: true });
  });
  const move = useAction((from: string, to: string) => moveFile(id, from, to));
  const remove = useAction((path: string) => deleteFile(id, path).then(done));
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
    create.clear();
    move.clear();
  };
  const submitDialog = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!dialog) return;
    const value = name.trim();
    const result = dialog.kind === "move"
      ? await move.run(dialog.entry.path, value.startsWith("/") ? value : join(parentOf(dialog.entry.path), value))
      : await create.run(dialog.kind, join(dir, value));
    if (result.ok) {
      closeDialog();
      done();
    }
  };

  const parts = dir.split("/").filter(Boolean);
  const entries = list.data?.path === dir ? list.data.entries : [];
  const actionError = upload.error ?? remove.error ?? open.error ?? download.error;
  const dialogError = create.error ?? move.error;

  return (
    <Card
      title="Files"
      actions={
        <div className="btn-group">
          <button type="button" className="btn" onClick={() => setDialog({ kind: "directory" })}>New folder</button>
          <button type="button" className="btn" onClick={() => setDialog({ kind: "file" })}>New file</button>
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
        <nav aria-label="Folder" className="file-path">
          <button type="button" className="btn btn-sm btn-ghost" aria-current={parts.length === 0 ? "location" : undefined} onClick={() => setDir("/")}>Website files</button>
          {parts.map((p, i) => {
            const path = `/${parts.slice(0, i + 1).join("/")}`;
            return (
              <span key={path}>
                <span aria-hidden="true">/ </span>
                <button type="button" className="btn btn-sm btn-ghost" aria-current={i === parts.length - 1 ? "location" : undefined} onClick={() => setDir(path)}>{p}</button>
              </span>
            );
          })}
        </nav>
      </div>
      <TableWrap>
        <table aria-busy={list.loading}>
          <thead><tr><th scope="col">Name</th><th scope="col">Size</th><th scope="col">Changed</th><th scope="col"><span className="visually-hidden">Actions</span></th></tr></thead>
          <tbody>
            {dir !== "/" && (
              <tr>
                <td colSpan={4}>
                  <button type="button" className="btn btn-sm btn-ghost" onClick={() => setDir(parentOf(dir))}>.. (up one folder)</button>
                </td>
              </tr>
            )}
            {entries.map((f) => (
              <tr key={f.path}>
                <td>
                  {f.type === "directory" ? (
                    <button type="button" className="btn btn-sm btn-ghost" onClick={() => setDir(f.path)}>
                      <Folder size={16} aria-hidden="true" /> <span className="visually-hidden">Folder </span>{f.name}
                    </button>
                  ) : (
                    <span style={{ display: "inline-flex", gap: 8, alignItems: "center", paddingLeft: 12 }}>
                      {f.type === "link" ? <Link2 size={16} aria-hidden="true" /> : <File size={16} aria-hidden="true" />} {f.name}
                      {f.type === "link" && f.target && <span className="muted">to {f.target}</span>}
                    </span>
                  )}
                  {f.path === root && <span className="cell-sub" style={{ paddingLeft: 12 }}>The web server serves this folder.</span>}
                </td>
                <td>{f.type === "file" ? bytes(f.sizeBytes) : "–"}</td>
                <td>{relativeTime(f.modifiedAt)}</td>
                <td>
                  <div className="btn-group" style={{ justifyContent: "flex-end" }}>
                    {f.type === "file" && (
                      <>
                        <button type="button" className="btn btn-sm" disabled={open.busy} onClick={() => void open.run(f)}>Edit<span className="visually-hidden"> {f.name}</span></button>
                        <button type="button" className="btn btn-sm btn-icon" disabled={download.busy} aria-label={`Download ${f.name}`} title="Download" onClick={() => void download.run(f)}><Download size={16} aria-hidden="true" /></button>
                      </>
                    )}
                    <button type="button" className="btn btn-sm" onClick={() => { setDialog({ kind: "move", entry: f }); setName(f.name); }}>Rename<span className="visually-hidden"> {f.name}</span></button>
                    <ConfirmButton
                      className="btn btn-sm btn-danger"
                      title={`Remove ${f.name}?`}
                      confirm="Remove"
                      message={<p>{f.type === "directory" ? "The server removes the folder and all files in it." : "The server removes the file."} To get it back, you must restore it from a backup.</p>}
                      onConfirm={() => void remove.run(f.path)}
                    >
                      Remove<span className="visually-hidden"> {f.name}</span>
                    </ConfirmButton>
                  </div>
                </td>
              </tr>
            ))}
            {entries.length === 0 && <tr><td colSpan={4} className="muted">{list.loading ? "Loading…" : list.error ? "The panel could not read this folder." : "This folder is empty."}</td></tr>}
          </tbody>
        </table>
      </TableWrap>
      <p className="card-body muted small" style={{ paddingTop: 12 }}>
        {list.data?.truncated && <>This folder has more files than the panel shows. Use SFTP to see all files. </>}
        The file manager works in the files of this website only, as the Unix user <code>{website.unixUser}</code>. For files larger than {bytes(MAX_UPLOAD_BYTES)}, use SFTP.
      </p>

      {dialog && (
        <Modal
          title={dialog.kind === "move" ? `Rename ${dialog.entry.name}` : dialog.kind === "directory" ? "New folder" : "New file"}
          onClose={closeDialog}
          footer={<><button type="button" className="btn" onClick={closeDialog}>Cancel</button><button type="submit" form="file-dialog" className="btn btn-primary" disabled={!name.trim() || create.busy || move.busy}>{dialog.kind === "move" ? "Rename" : "Create"}</button></>}
        >
          <form id="file-dialog" noValidate onSubmit={(e) => void submitDialog(e)}>
            <FormError error={dialogError && !Object.keys(dialogError.fields).length ? dialogError : null} />
            <Field
              label="Name"
              hint={dialog.kind === "move" ? <>To move it, type a full path that starts with <code>/</code>, for example <code>/public/old.txt</code>.</> : undefined}
              error={dialogError ? Object.values(dialogError.fields)[0] : undefined}
            >
              {(p) => <input {...p} className="input mono" value={name} onChange={(e) => setName(e.target.value)} autoCapitalize="none" spellCheck={false} />}
            </Field>
          </form>
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
