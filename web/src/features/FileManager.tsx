import { File, Folder, Upload } from "lucide-react";
import { useRef, useState } from "react";
import { useAction, useStore } from "../api/hooks";
import { createNode, deleteNode, renameNode, saveFile } from "../api/store";
import type { FileNode } from "../api/types";
import { Card, ConfirmButton, Field, FormError, Modal } from "../components/ui";
import { bytes, relativeTime } from "../lib/format";

const TEXT = /\.(php|html?|css|js|ts|json|txt|md|xml|ya?ml|ini|conf|env|log|htaccess|sh)$|^\.[\w-]+$/i;

/** The file manager of one website (FR-803). Paths are inside the website container only. */
export function FileManager({ websiteId }: { websiteId: string }) {
  const state = useStore();
  const all = state.files[websiteId] ?? [];
  const [dir, setDir] = useState("/public");
  const [dialog, setDialog] = useState<null | { kind: "dir" | "file" } | { kind: "rename"; node: FileNode }>(null);
  const [name, setName] = useState("");
  const [editing, setEditing] = useState<FileNode | null>(null);
  const [content, setContent] = useState("");
  const upload = useRef<HTMLInputElement>(null);
  const create = useAction(createNode);
  const rename = useAction(renameNode);
  const uploadAction = useAction(createNode);

  const exists = all.some((f) => f.path === dir && f.type === "dir");
  const current = exists ? dir : "";
  const entries = all
    .filter((f) => f.path.slice(0, f.path.lastIndexOf("/")) === current)
    .sort((a, b) => (a.type === b.type ? a.name.localeCompare(b.name) : a.type === "dir" ? -1 : 1));
  const parts = current.split("/").filter(Boolean);

  const closeDialog = () => {
    setDialog(null);
    setName("");
    create.clear();
    rename.clear();
  };

  return (
    <Card
      title="Files"
      actions={
        <div className="btn-group">
          <button type="button" className="btn" onClick={() => setDialog({ kind: "dir" })}>New folder</button>
          <button type="button" className="btn" onClick={() => setDialog({ kind: "file" })}>New file</button>
          <button type="button" className="btn btn-primary" onClick={() => upload.current?.click()}>
            <Upload size={16} aria-hidden="true" /> Upload
          </button>
          <input
            ref={upload}
            type="file"
            multiple
            hidden
            aria-label="Upload files"
            onChange={async (e) => {
              for (const f of Array.from(e.target.files ?? [])) {
                const text = TEXT.test(f.name) && f.size < 512_000 ? await f.text() : "";
                await uploadAction.run(websiteId, current, f.name, "file", text);
              }
              e.target.value = "";
            }}
          />
        </div>
      }
    >
      <div className="card-body" style={{ paddingBottom: 8 }}>
        <FormError error={uploadAction.error} />
        <nav aria-label="Folder" className="file-path">
          <button type="button" className="btn btn-sm btn-ghost" onClick={() => setDir("")}>Home</button>
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
      <div className="table-wrap">
        <table>
          <thead><tr><th scope="col">Name</th><th scope="col">Size</th><th scope="col">Changed</th><th scope="col"><span className="visually-hidden">Actions</span></th></tr></thead>
          <tbody>
            {current && (
              <tr>
                <td colSpan={4}>
                  <button type="button" className="btn btn-sm btn-ghost" onClick={() => setDir(current.slice(0, current.lastIndexOf("/")))}>.. (up one folder)</button>
                </td>
              </tr>
            )}
            {entries.map((f) => (
              <tr key={f.path}>
                <td>
                  {f.type === "dir" ? (
                    <button type="button" className="btn btn-sm btn-ghost" onClick={() => setDir(f.path)}>
                      <Folder size={16} aria-hidden="true" /> <span className="visually-hidden">Folder </span>{f.name}
                    </button>
                  ) : (
                    <span style={{ display: "inline-flex", gap: 8, alignItems: "center", paddingLeft: 12 }}>
                      <File size={16} aria-hidden="true" /> {f.name}
                    </span>
                  )}
                </td>
                <td>{f.type === "dir" ? "–" : bytes(f.sizeBytes)}</td>
                <td>{relativeTime(f.modifiedAt)}</td>
                <td>
                  <div className="btn-group" style={{ justifyContent: "flex-end" }}>
                    {f.type === "file" && TEXT.test(f.name) && (
                      <button type="button" className="btn btn-sm" onClick={() => { setEditing(f); setContent(f.content ?? ""); }}>Edit<span className="visually-hidden"> {f.name}</span></button>
                    )}
                    <button type="button" className="btn btn-sm" onClick={() => { setDialog({ kind: "rename", node: f }); setName(f.name); }}>Rename<span className="visually-hidden"> {f.name}</span></button>
                    <ConfirmButton className="btn btn-sm btn-danger" title={`Remove ${f.name}?`} confirm="Remove" message={<p>{f.type === "dir" ? "The folder and all files in it are removed." : "The file is removed."} You can restore it from a backup.</p>} onConfirm={() => deleteNode(websiteId, f.path)}>
                      Remove<span className="visually-hidden"> {f.name}</span>
                    </ConfirmButton>
                  </div>
                </td>
              </tr>
            ))}
            {entries.length === 0 && <tr><td colSpan={4} className="muted">This folder is empty.</td></tr>}
          </tbody>
        </table>
      </div>

      {dialog && (
        <Modal
          title={"node" in dialog ? `Rename ${dialog.node.name}` : dialog.kind === "dir" ? "New folder" : "New file"}
          onClose={closeDialog}
          footer={<><button type="button" className="btn" onClick={closeDialog}>Cancel</button><button type="submit" form="fm" className="btn btn-primary">{"node" in dialog ? "Rename" : "Create"}</button></>}
        >
          <form
            id="fm"
            noValidate
            onSubmit={async (e) => {
              e.preventDefault();
              const result = "node" in dialog ? await rename.run(websiteId, dialog.node.path, name) : await create.run(websiteId, current, name, dialog.kind);
              if (result.ok) closeDialog();
            }}
          >
            <FormError error={create.error ?? rename.error} />
            <Field label="Name" error={create.error?.fields.name}>
              {(p) => <input {...p} className="input" value={name} onChange={(e) => setName(e.target.value)} autoCapitalize="none" spellCheck={false} />}
            </Field>
          </form>
        </Modal>
      )}
      {editing && (
        <Modal
          wide
          title={`Edit ${editing.path}`}
          onClose={() => setEditing(null)}
          footer={
            <>
              <button type="button" className="btn" onClick={() => setEditing(null)}>Cancel</button>
              <button type="button" className="btn btn-primary" onClick={() => { saveFile(websiteId, editing.path, content); setEditing(null); }}>Save</button>
            </>
          }
        >
          <label className="visually-hidden" htmlFor="editor">Content of {editing.name}</label>
          <textarea id="editor" className="textarea editor" value={content} onChange={(e) => setContent(e.target.value)} spellCheck={false} />
        </Modal>
      )}
    </Card>
  );
}
