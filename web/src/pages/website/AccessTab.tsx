import { useState } from "react";
import { Link } from "react-router-dom";
import { useAction } from "../../api/hooks";
import { addSshKey, removeSshKey, setAccess } from "../../api/store";
import type { Website } from "../../api/types";
import { Card, ConfirmButton, Field, FormError, Toggle } from "../../components/ui";
import { relativeTime } from "../../lib/format";

function sftpUrl(website: Website) {
  const server = website.serverId.replace("srv_", "").replace(/(\D+)(\d+)/, "$1-$2");
  return `sftp://${website.unixUser}@${server}.cloudfactory.io`;
}

export function CopyField({ value, label }: { value: string; label: string }) {
  const [copied, setCopied] = useState(false);
  return (
    <div className="copy-field">
      <code aria-label={label}>{value}</code>
      <button
        type="button"
        className="btn"
        onClick={() => {
          void navigator.clipboard?.writeText(value);
          setCopied(true);
          setTimeout(() => setCopied(false), 1500);
        }}
      >
        {copied ? "Copied" : "Copy"}
      </button>
    </div>
  );
}

/** The SFTP and SSH card on the overview. */
export function SshCard({ website }: { website: Website }) {
  return (
    <Card title="SFTP and SSH">
      <div className="card-body">
        <CopyField value={sftpUrl(website)} label="SFTP address" />
        <p className="muted small" style={{ marginTop: 14 }}>
          Sign in with an SSH key. {website.sshKeys.length} {website.sshKeys.length === 1 ? "key" : "keys"} added. <Link to={`/websites/${website.id}/access`}>Manage keys</Link>
        </p>
        <p className="muted small" style={{ marginTop: 10 }}>SSH shell: {website.shellEnabled ? "on" : "off"}. The shell opens inside this website's container only.</p>
      </div>
    </Card>
  );
}

/** SFTP and SSH access (FR-801, FR-802). */
export function AccessTab({ website }: { website: Website }) {
  const [name, setName] = useState("");
  const [key, setKey] = useState("");
  const add = useAction(async () => {
    addSshKey(website.id, name, key);
    setName("");
    setKey("");
  });
  return (
    <div className="grid-2">
      <div className="stack">
        <Card title="Connection">
          <div className="card-body">
            <CopyField value={sftpUrl(website)} label="SFTP address" />
            <div style={{ marginTop: 16 }} className="section-gap">
              <Toggle checked={website.shellEnabled} label="SSH shell" onChange={(v) => setAccess(website.id, { shellEnabled: v })}>
                SSH shell: {website.shellEnabled ? "on" : "off"}. The shell opens inside the website container, as {website.unixUser}. It cannot reach the host.
              </Toggle>
              <Toggle checked={website.sftpPasswordEnabled} label="Password sign-in" onChange={(v) => setAccess(website.id, { sftpPasswordEnabled: v })}>
                Password sign-in: {website.sftpPasswordEnabled ? "on" : "off"}. SSH keys are safer.
              </Toggle>
            </div>
          </div>
        </Card>
      </div>
      <div className="stack">
        <Card title="SSH keys">
          <div className="table-wrap">
            <table>
              <thead><tr><th scope="col">Name</th><th scope="col">Fingerprint</th><th scope="col">Added</th><th scope="col"><span className="visually-hidden">Actions</span></th></tr></thead>
              <tbody>
                {website.sshKeys.map((k) => (
                  <tr key={k.id}>
                    <td>{k.name}</td>
                    <td><code>{k.fingerprint}</code></td>
                    <td>{relativeTime(k.addedAt)}</td>
                    <td className="cell-actions" style={{ width: 120 }}>
                      <ConfirmButton className="btn btn-sm btn-danger" title={`Remove ${k.name}?`} confirm="Remove key" message={<p>This key cannot sign in after you remove it.</p>} onConfirm={() => removeSshKey(website.id, k.id)}>Remove</ConfirmButton>
                    </td>
                  </tr>
                ))}
                {website.sshKeys.length === 0 && <tr><td colSpan={4} className="muted">No SSH keys.</td></tr>}
              </tbody>
            </table>
          </div>
          <form className="card-body" style={{ paddingTop: 16 }} noValidate onSubmit={(e) => { e.preventDefault(); void add.run(); }}>
            <FormError error={add.error} />
            <Field label="Name">
              {(p) => <input {...p} className="input" value={name} onChange={(e) => setName(e.target.value)} placeholder="My laptop" />}
            </Field>
            <Field label="Public key" error={add.error?.fields.key}>
              {(p) => <textarea {...p} className="textarea" rows={3} value={key} onChange={(e) => setKey(e.target.value)} placeholder="ssh-ed25519 AAAA…" spellCheck={false} />}
            </Field>
            <button type="submit" className="btn btn-primary" style={{ marginTop: 12 }}>Add key</button>
          </form>
        </Card>
      </div>
    </div>
  );
}
