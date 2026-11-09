import { useEffect, useState } from "react";
import { Link } from "react-router-dom";
import { useAction, useStore } from "../../api/hooks";
import { addSshKey, removeSshKey, setAccess, setSftp } from "../../api/store";
import type { SftpAccess, Website } from "../../api/types";
import { Card, ConfirmButton, Field, FormError, Toggle } from "../../components/ui";
import { relativeTime } from "../../lib/format";

function sftpUrl(website: Website) {
  if (website.sftp) return `sftp://${website.sftp.username}@${website.sftp.host}${website.sftp.port === 22 ? "" : `:${website.sftp.port}`}`;
  const server = website.serverId.replace("srv_", "").replace(/(\D+)(\d+)/, "$1-$2");
  // Mock mode: the default port of the SFTP container.
  return `sftp://${website.unixUser}@${server}.cloudfactory.io:2200`;
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
  if (website.sftp) {
    const sftp = website.sftp;
    return (
      <Card title="SFTP">
        <div className="card-body">
          {sftp.enabled ? <CopyField value={sftpUrl(website)} label="SFTP address" /> : <p>SFTP is off.</p>}
          <p className="muted small" style={{ marginTop: 14 }}>
            {sftp.authorizedKeys.length} SSH {sftp.authorizedKeys.length === 1 ? "key" : "keys"}. Sign in with an SSH key. Passwords are not accepted. <Link to={`/websites/${website.id}/access`}>Manage SFTP</Link>
          </p>
        </div>
      </Card>
    );
  }
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

/** The type, the comment and the SHA-256 fingerprint of an OpenSSH public key, as ssh-keygen -l shows it. */
function useKeyInfo(key: string) {
  const [type, blob, ...comment] = key.split(" ");
  const [fingerprint, setFingerprint] = useState("");
  useEffect(() => {
    let live = true;
    try {
      const bytes = Uint8Array.from(atob(blob), (c) => c.charCodeAt(0));
      void crypto.subtle?.digest("SHA-256", bytes).then((d) => {
        if (live) setFingerprint(`SHA256:${btoa(String.fromCharCode(...new Uint8Array(d))).replace(/=+$/, "")}`);
      });
    } catch {
      setFingerprint("");
    }
    return () => { live = false; };
  }, [blob]);
  return { type, comment: comment.join(" "), fingerprint };
}

function KeyRow({ value, onRemove, busy }: { value: string; onRemove: () => void; busy: boolean }) {
  const info = useKeyInfo(value);
  return (
    <tr>
      <td>{info.comment || <span className="muted">No comment</span>}<span className="cell-sub">{info.type}</span></td>
      <td><code style={{ wordBreak: "break-all" }}>{info.fingerprint || "…"}</code></td>
      <td className="cell-actions" style={{ width: 120 }}>
        <ConfirmButton className="btn btn-sm btn-danger" title="Remove this key?" confirm="Remove key" message={<p>This key cannot sign in after you remove it.</p>} onConfirm={onRemove} disabled={busy}>Remove</ConfirmButton>
      </td>
    </tr>
  );
}

/**
 * Spec section 10, live mode: the owner signs in with SFTP as the Unix user of the website, with
 * an SSH key. The server refuses passwords, and allows only SFTP, in the directory of the website.
 */
function SftpTab({ website, sftp }: { website: Website; sftp: SftpAccess }) {
  const state = useStore();
  const [key, setKey] = useState("");
  // The panel saves a change when the agent has applied it. Until then, a second change would start from the old state.
  const applying = state.jobs.some((j) => j.websiteId === website.id && (j.state === "queued" || j.state === "running") && j.title.endsWith("SFTP access"));
  const action = useAction((next: Partial<Parameters<typeof setSftp>[1]>) => setSftp(website.id, { enabled: sftp.enabled, authorizedKeys: sftp.authorizedKeys, ...next }));
  // SFTP accepts keys only. The last key cannot go while SFTP is on.
  const lastKey = sftp.enabled && sftp.authorizedKeys.length === 1;
  const change = { ...action, busy: action.busy || applying };
  const addKey = async (e: React.FormEvent) => {
    e.preventDefault();
    if ((await change.run({ authorizedKeys: [...sftp.authorizedKeys, key] })).ok) setKey("");
  };
  return (
    <div className="grid-2">
      <div className="stack">
        <Card title="Connection">
          <div className="card-body">
            <FormError error={change.error && !change.error.fields.key ? change.error : null} />
            {applying && <p className="alert alert-info" role="status" style={{ marginBottom: 12 }}>The server is applying the change. This takes some seconds.</p>}
            <CopyField value={sftpUrl(website)} label="SFTP address" />
            <table className="kv" style={{ marginTop: 14 }}>
              <tbody>
                <tr><th scope="row">Host</th><td><code>{sftp.host}</code></td></tr>
                <tr><th scope="row">Port</th><td><code>{sftp.port}</code></td></tr>
                <tr><th scope="row">User name</th><td><code>{sftp.username}</code></td></tr>
                <tr><th scope="row">Directory</th><td>The files of the website. The web server serves <code>{website.runtime.documentRoot === "." ? "/" : `/${website.runtime.documentRoot ?? "public"}`}</code>.</td></tr>
              </tbody>
            </table>
            <div style={{ marginTop: 16 }} className="section-gap">
              <Toggle checked={sftp.enabled} label="SFTP" disabled={change.busy || (!sftp.enabled && sftp.authorizedKeys.length === 0)} onChange={(v) => void change.run({ enabled: v })}>
                SFTP: {sftp.enabled ? "on" : "off"}. Only SFTP is allowed. There is no shell.
              </Toggle>
              <p className="muted small" style={{ marginTop: 8 }}>{sftp.authorizedKeys.length === 0 ? "Add an SSH key, then turn on SFTP." : "Sign in with one of the SSH keys. The server does not accept passwords."}</p>
            </div>
          </div>
        </Card>
      </div>
      <div className="stack">
        <Card title="SSH keys">
          <div className="table-wrap">
            <table>
              <thead><tr><th scope="col">Key</th><th scope="col">Fingerprint</th><th scope="col"><span className="visually-hidden">Actions</span></th></tr></thead>
              <tbody>
                {sftp.authorizedKeys.map((k) => <KeyRow key={k} value={k} busy={change.busy || lastKey} onRemove={() => void change.run({ authorizedKeys: sftp.authorizedKeys.filter((x) => x !== k) })} />)}
                {sftp.authorizedKeys.length === 0 && <tr><td colSpan={3} className="muted">No SSH keys.</td></tr>}
              </tbody>
            </table>
          </div>
          <form className="card-body" style={{ paddingTop: 16 }} noValidate onSubmit={addKey}>
            {lastKey && <p className="muted small" style={{ marginBottom: 12 }}>To remove the last key, turn off SFTP first.</p>}
            <Field label="Public key" hint="Up to 20 keys. The comment at the end of the key names it in this list." error={change.error?.fields.key ?? change.error?.fields["authorizedKeys.0"]}>
              {(p) => <textarea {...p} className="textarea mono" rows={3} value={key} onChange={(e) => setKey(e.target.value)} placeholder="ssh-ed25519 AAAA… me@laptop" spellCheck={false} />}
            </Field>
            <button type="submit" className="btn btn-primary" style={{ marginTop: 12 }} disabled={change.busy || !key.trim() || sftp.authorizedKeys.length >= 20}>Add key</button>
          </form>
        </Card>
      </div>
    </div>
  );
}

/** SFTP and SSH access (FR-801, FR-802). */
export function AccessTab({ website }: { website: Website }) {
  if (website.sftp) return <SftpTab website={website} sftp={website.sftp} />;
  return <MockAccessTab website={website} />;
}

function MockAccessTab({ website }: { website: Website }) {
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
