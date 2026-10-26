import { useState } from "react";
import { Link } from "react-router-dom";
import { useAction, useStore } from "../api/hooks";
import { LIVE } from "../api/mode";
import { addImage } from "../api/store";
import type { UpdatePolicy } from "../api/types";
import { Topbar } from "../components/Layout";
import { Card, Field, FormError, Modal, PageHeader, Status } from "../components/ui";
import { POLICY_LABEL } from "../lib/labels";

const CATALOGUE = [
  { name: "WordPress", family: "frankenphp", runtime: "FrankenPHP · PHP 8.5", text: "Blog and content site. Install in one action is planned for a later release.", later: true },
  { name: "WooCommerce", family: "frankenphp", runtime: "FrankenPHP · PHP 8.4", text: "WordPress with a shop. Uses MariaDB and Redis.", later: true },
  { name: "Laravel", family: "frankenphp", runtime: "FrankenPHP worker · PHP 8.5", text: "Deploy from Git. Worker mode is available for Laravel Octane." },
  { name: "Symfony", family: "frankenphp", runtime: "FrankenPHP · PHP 8.5", text: "Deploy from Git. Uses composer install in a build container." },
  { name: "Static site", family: "edge", runtime: "Caddy · static files", text: "The edge proxy serves the files directly. Build with npm in a build container." },
  { name: "Node.js app", family: "node", runtime: "Node.js 24 · OCI image", text: "Runs your own OCI image. Set the port and the start command." },
];

/** Apps and own OCI images (FR-307, FR-1110). */
export function AppsPage() {
  const state = useStore();
  const isAdmin = state.session.user.role === "administrator";
  const custom = state.images.filter((i) => i.custom);
  const [open, setOpen] = useState(false);
  const [form, setForm] = useState({ name: "", reference: "", tag: "latest", policy: "manual" as UpdatePolicy });
  const add = useAction(addImage);
  const pending = state.jobs.filter((j) => j.state === "running" && j.title.startsWith("Add image"));
  return (
    <>
      <Topbar />
      <main id="main" className="content">
        <PageHeader title="Apps" description="Start points for common applications, and the OCI images that administrators added." actions={isAdmin && <button type="button" className="btn btn-primary" onClick={() => setOpen(true)}>Add your own image</button>} />
        <div className="grid-3">
          {/* In live mode, show only the apps that have an image on this panel. */}
          {CATALOGUE.filter((a) => !LIVE || state.images.some((i) => i.family === a.family)).map((a) => (
            <section key={a.name} className="card app-tile" aria-labelledby={`app-${a.name}`}>
              <h2 id={`app-${a.name}`}>{a.name}</h2>
              <span className="tag" style={{ alignSelf: "flex-start" }}>{a.runtime}</span>
              <p>{a.text}</p>
              <Link className="btn" to="/websites/new">New {a.name} website</Link>
            </section>
          ))}
        </div>
        <Card title="Own images" className="mt">
          <div className="table-wrap">
            <table>
              <thead><tr><th scope="col">Image</th><th scope="col">Reference</th><th scope="col">Tag</th><th scope="col">Signature</th><th scope="col">Updates</th></tr></thead>
              <tbody>
                {custom.map((i) => (
                  <tr key={i.id}>
                    <td><strong>{i.name}</strong></td>
                    <td><code>{i.reference}</code></td>
                    <td><code>{i.running}</code></td>
                    <td>{i.signatureVerified ? <Status tone="success">Verified</Status> : <Status tone="warning">No signature</Status>}</td>
                    <td>{POLICY_LABEL[i.policy]}<span className="cell-sub">The panel checks the registry each hour for a new digest of the same tag.</span></td>
                  </tr>
                ))}
                {pending.map((j) => <tr key={j.id}><td colSpan={5} className="muted">{j.title}: {j.steps[Math.min(j.step, j.steps.length - 1)]}…</td></tr>)}
              </tbody>
            </table>
          </div>
        </Card>
      </main>
      {open && (
        <Modal title="Add your own image" onClose={() => { setOpen(false); add.clear(); }} footer={<><button type="button" className="btn" onClick={() => setOpen(false)}>Cancel</button><button type="submit" form="img" className="btn btn-primary">Add image</button></>}>
          <form id="img" noValidate onSubmit={async (e) => { e.preventDefault(); if ((await add.run(form.name, form.reference, form.tag, form.policy)).ok) setOpen(false); }}>
            <FormError error={add.error} />
            <Field label="Name" error={add.error?.fields.name}>{(p) => <input {...p} className="input" value={form.name} onChange={(e) => setForm({ ...form, name: e.target.value })} placeholder="Python 3.14" />}</Field>
            <Field label="Image reference" error={add.error?.fields.reference}>{(p) => <input {...p} className="input mono" value={form.reference} onChange={(e) => setForm({ ...form, reference: e.target.value })} placeholder="docker.io/library/python" spellCheck={false} />}</Field>
            <Field label="Tag" error={add.error?.fields.tag}>{(p) => <input {...p} className="input mono" value={form.tag} onChange={(e) => setForm({ ...form, tag: e.target.value })} spellCheck={false} />}</Field>
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
