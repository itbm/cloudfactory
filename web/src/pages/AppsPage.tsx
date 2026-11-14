import { useState } from "react";
import { Link } from "react-router-dom";
import { useAction, useStore } from "../api/hooks";
import { addImage } from "../api/store";
import type { UpdatePolicy } from "../api/types";
import { Topbar } from "../components/Layout";
import { Card, Field, FormError, Modal, PageHeader, Status, TableWrap } from "../components/ui";
import { jobActive, POLICY_LABEL } from "../lib/labels";

// The API of this release has no Git deploys or build containers.
const CATALOGUE = [
  { name: "WordPress", family: "frankenphp", runtime: "FrankenPHP · PHP 8.5", text: "Blog and content site. Upload WordPress with SFTP and set the document root to \".\".", later: true },
  { name: "WooCommerce", family: "frankenphp", runtime: "FrankenPHP · PHP 8.4", text: "WordPress with a shop. Uses a MariaDB database.", later: true },
  { name: "Laravel", family: "frankenphp", runtime: "FrankenPHP worker · PHP 8.5", text: "Upload the files with SFTP. Worker mode is available for Laravel Octane." },
  { name: "Symfony", family: "frankenphp", runtime: "FrankenPHP · PHP 8.5", text: "Upload the files with SFTP, with the vendor directory." },
  { name: "Static site", family: "edge", runtime: "Caddy · static files", text: "A small Caddy container serves the files. Upload them with SFTP." },
  { name: "Node.js app", family: "node", runtime: "Node.js 24 · OCI image", text: "Runs your own OCI image. An administrator sets the port and the start command." },
];

/** Apps and own OCI images (FR-307, FR-1110). */
export function AppsPage() {
  const state = useStore();
  const isAdmin = state.session.user.role === "administrator";
  const custom = state.images.filter((i) => i.custom);
  const [open, setOpen] = useState(false);
  const [form, setForm] = useState({ name: "", reference: "", tag: "latest", policy: "manual" as UpdatePolicy, containerPort: "", command: "" });
  const add = useAction(addImage);
  const pending = state.jobs.filter((j) => jobActive(j) && j.title.startsWith("Add image"));
  return (
    <>
      <Topbar />
      <main id="main" className="content">
        <PageHeader title="Apps" description={isAdmin ? "Start points for common applications, and the OCI images that administrators added." : "Start points for common applications, and the OCI images that your provider added."} actions={isAdmin && <button type="button" className="btn btn-primary" onClick={() => setOpen(true)}>Add your own image</button>} />
        <div className="grid-3">
          {/* Show only the apps that have an image on this panel. */}
          {CATALOGUE.filter((a) => state.images.some((i) => i.family === a.family)).map((a) => (
            <section key={a.name} className="card app-tile" aria-labelledby={`app-${a.name}`}>
              <h2 id={`app-${a.name}`}>{a.name}</h2>
              <span className="tag" style={{ alignSelf: "flex-start" }}>{a.runtime}</span>
              <p>{isAdmin ? a.text : a.text.replace("An administrator sets", "Your provider sets")}</p>
              <Link className="btn" to="/websites/new">New {a.name} website</Link>
            </section>
          ))}
        </div>
        <Card title="Own images" className="mt">
          <TableWrap>
            <table>
              <thead><tr><th scope="col">Image</th><th scope="col">Reference</th><th scope="col">Tag</th><th scope="col">Port and command</th><th scope="col">Signature</th><th scope="col">Updates</th></tr></thead>
              <tbody>
                {custom.map((i) => (
                  <tr key={i.id}>
                    <td><strong>{i.name}</strong></td>
                    <td><code>{i.reference}</code></td>
                    <td><code>{i.running}</code></td>
                    <td><code>{i.containerPort ?? 8080}</code><span className="cell-sub">{i.command?.length ? <code>{i.command.join(" ")}</code> : "Command of the image"}</span></td>
                    <td>{i.signatureVerified ? <Status tone="success">Verified</Status> : <Status tone="warning">No signature</Status>}</td>
                    <td>{POLICY_LABEL[i.policy]}<span className="cell-sub">The panel checks the registry each hour for a new digest of the same tag.</span></td>
                  </tr>
                ))}
                {pending.map((j) => <tr key={j.id}><td colSpan={6} className="muted">{j.title}: {j.steps[Math.min(j.step, j.steps.length - 1)]}…</td></tr>)}
              </tbody>
            </table>
          </TableWrap>
        </Card>
      </main>
      {open && (
        <Modal title="Add your own image" onClose={() => { setOpen(false); add.clear(); }} footer={<><button type="button" className="btn" onClick={() => setOpen(false)}>Cancel</button><button type="submit" form="img" className="btn btn-primary">Add image</button></>}>
          <form id="img" noValidate onSubmit={async (e) => { e.preventDefault(); if ((await add.run(form.name, form.reference, form.tag, form.policy, { containerPort: form.containerPort, command: form.command })).ok) setOpen(false); }}>
            <FormError error={add.error} />
            <Field label="Name" error={add.error?.fields.name}>{(p) => <input {...p} className="input" value={form.name} onChange={(e) => setForm({ ...form, name: e.target.value })} placeholder="Python 3.14" />}</Field>
            <Field label="Image reference" error={add.error?.fields.reference}>{(p) => <input {...p} className="input mono" value={form.reference} onChange={(e) => setForm({ ...form, reference: e.target.value })} placeholder="docker.io/library/python" spellCheck={false} />}</Field>
            <Field label="Tag" error={add.error?.fields.tag}>{(p) => <input {...p} className="input mono" value={form.tag} onChange={(e) => setForm({ ...form, tag: e.target.value })} spellCheck={false} />}</Field>
            <div className="form-row">
              <Field label="HTTP port" hint="The port in the container. The default is 8080." error={add.error?.fields.containerPort}>{(p) => <input {...p} className="input mono" inputMode="numeric" value={form.containerPort} onChange={(e) => setForm({ ...form, containerPort: e.target.value })} placeholder="8080" />}</Field>
              <Field label="Start command (optional)" hint="Leave empty to use the command of the image. Quotes keep spaces." error={add.error?.fields.command}>{(p) => <input {...p} className="input mono" value={form.command} onChange={(e) => setForm({ ...form, command: e.target.value })} placeholder="node server.js" spellCheck={false} />}</Field>
            </div>
            <Field label="Updates" hint="When the registry has a new digest for the tag.">
              {(p) => (
                <select {...p} className="select" value={form.policy} onChange={(e) => setForm({ ...form, policy: e.target.value as UpdatePolicy })}>
                  <option value="manual">Manual</option>
                  <option value="automatic">Automatic, in the maintenance window</option>
                </select>
              )}
            </Field>
            <p className="muted small">The image runs in gVisor like all containers. The agent checks the signature when the image has one.</p>
          </form>
        </Modal>
      )}
    </>
  );
}
