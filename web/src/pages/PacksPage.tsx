import { useState, type ChangeEvent } from "react";
import { Link } from "react-router-dom";
import { useAction, useLoad } from "../api/hooks";
import { installPack, loadPacks, previewPack, removePack, setPackDeveloperMode, syncPacks, updatePack, type PackPreview } from "../api/store";
import type { Confirmation, Pack, PackServer, PackSigner } from "../api/types";
import { ConfirmIdentityFields, NO_CONFIRMATION } from "../components/ConfirmIdentity";
import { Topbar } from "../components/Layout";
import { TrustedKeys } from "../components/TrustedKeys";
import { Card, Empty, Field, FormError, Modal, PageHeader, RowMenu, Status, TableWrap, Toggle } from "../components/ui";
import { longDate, relativeTime } from "../lib/format";
import { isPhpPack, phpFamilyName } from "../lib/labels";

const SERVER_TEXT: Record<PackServer["state"], string> = { installed: "Loaded", pending: "Waiting", failing: "Not loaded" };
const SERVER_TONE = { installed: "success", pending: "warning", failing: "danger" } as const;

/** The first 12 digits of a digest, enough to compare two digests. */
const shortDigest = (digest: string) => digest.replace(/^sha256:/, "").slice(0, 12);

/** A list in words, for example "8.3 and 8.2". */
const list = (items: string[]) => (items.length < 2 ? items[0] ?? "" : `${items.slice(0, -1).join(", ")} and ${items[items.length - 1]}`);

/** A list of PHP versions in words, for example "PHP 8.3 and 8.2". */
const phpList = (versions: string[]) => `PHP ${list(versions)}`;

/** "1 database server" or "3 database servers" */
const databaseServers = (n: number) => `${n} ${n === 1 ? "database server" : "database servers"}`;

function SignedBy({ signer, developerMode }: { signer: PackSigner | null; developerMode?: boolean }) {
  if (signer?.release) return <Status tone="success">Signed by the Cloudfactory release key</Status>;
  if (signer) return <Status tone="success">Signed by {signer.name}</Status>;
  if (developerMode === undefined) return <Status tone="warning">No signature (developer mode)</Status>;
  return <Status tone={developerMode ? "warning" : "danger"}>{developerMode ? "No trusted signature. The developer mode accepts it." : "No trusted signature. The panel refuses it."}</Status>;
}

/**
 * Packs (docs/packs.md): a signed manifest from a publisher adds a website runtime, for example
 * Go, older versions of PHP for a built-in PHP runtime, or a database engine, for example
 * MySQL, with no new agent code. The panel sends each pack to each web server. Each agent
 * checks the signature again, and Podman pulls an image of a pack only with a signature of the
 * key of its publisher.
 */
export function PacksPage() {
  const [turn, setTurn] = useState(0);
  const data = useLoad(() => loadPacks(), [turn]);
  const reload = () => setTurn((t) => t + 1);
  const [installing, setInstalling] = useState(false);
  const sync = useAction(async () => { await syncPacks(); reload(); });

  return (
    <>
      <Topbar />
      <main id="main" className="content content-wide">
        <PageHeader
          title="Packs"
          description="A pack adds a website runtime, for example Go, more versions of PHP, or a database engine, for example MySQL, with no new agent code. Its publisher signs the manifest and each image. The panel installs a pack only when a trusted key signed it, and each server checks the signatures again."
          actions={
            <>
              <button type="button" className="btn" disabled={sync.busy || !data.data?.packs.length} onClick={() => void sync.run()}>Send to the servers again</button>
              <button type="button" className="btn btn-primary" onClick={() => setInstalling(true)}>Install pack</button>
            </>
          }
        />
        <FormError error={data.error ?? sync.error} />
        {data.data?.developerMode && (
          <div className="alert alert-warning" role="status">
            <p><strong>The developer mode is on.</strong> The panel installs packs with no signature. The servers pull the images of such a pack only from a registry that you allowed with <code>--allow-unsigned</code>. Turn it off on a production panel.</p>
          </div>
        )}
        <div className="stack">
          {data.loading && !data.data && <p className="muted">Loading the packs…</p>}
          {data.data && data.data.packs.length === 0 && (
            <Card title="Installed packs">
              <div className="card-body">
                <Empty title="No packs">To install a pack, add the key of its publisher in "Trusted keys", then select <strong>Install pack</strong>. The packs of Cloudfactory have the signature of the release key, which the panel trusts already.</Empty>
              </div>
            </Card>
          )}
          {data.data?.packs.map((p) => <PackCard key={p.id} pack={p} onChange={reload} />)}
          {data.data && <TrustedKeys keys={data.data.keys} onChange={reload} />}
          {data.data && <DeveloperMode on={data.data.developerMode} onChange={reload} />}
        </div>
        {installing && <InstallDialog onClose={() => setInstalling(false)} onInstalled={() => { setInstalling(false); reload(); }} />}
      </main>
    </>
  );
}

function PackCard({ pack: p, onChange }: { pack: Pack; onChange: () => void }) {
  const [updating, setUpdating] = useState(false);
  const [removing, setRemoving] = useState(false);
  const remove = useAction(async () => { await removePack(p.id); onChange(); });
  const db = p.database;
  const used = db ? db.databaseServers : p.images.reduce((n, i) => n + i.websites, 0);
  const waiting = p.images.filter((i) => i.availableDigest);
  const php = isPhpPack(p.runtime);

  return (
    <Card className="plugin-card" title={`${p.name} ${p.version}`} actions={
      <RowMenu label={`Actions for ${p.name}`} actions={[
        { label: "Install a new version", onSelect: () => setUpdating(true) },
        { label: "Remove pack", danger: true, onSelect: () => setRemoving(true) },
      ]} />
    }>
      <div className="card-body">
        {p.description && <p style={{ marginTop: 0 }}>{p.description}</p>}
        <FormError error={remove.error} />
        <div className="two-col">
          <table className="kv">
            <tbody>
              <tr><th scope="row">Publisher</th><td>{p.publisher}</td></tr>
              <tr><th scope="row">Signature</th><td><SignedBy signer={p.signedBy} /></td></tr>
              {db ? (
                <>
                  <tr><th scope="row">Database engine</th><td><code>{p.slug}</code><span className="cell-sub">{db.contract}, port {db.port}, data in <code>{db.dataDir}</code></span></td></tr>
                  <tr><th scope="row">Host ports</th><td>{db.firstPort} to {db.lastPort}<span className="cell-sub">Each customer can have {db.lastPort - db.firstPort + 1} database servers of {p.name} on each server. The websites connect by the network name and port {db.port}.</span></td></tr>
                </>
              ) : php ? (
                <tr><th scope="row">Runtime</th><td>{phpFamilyName(p.runtime.family)}<span className="cell-sub">{p.runtime.contract}: adds {phpList(p.images.map((i) => i.version))}</span></td></tr>
              ) : (
                <>
                  <tr><th scope="row">Runtime</th><td><code>{p.slug}</code><span className="cell-sub">{p.runtime.contract}, type {p.runtime.type}</span></td></tr>
                  <tr><th scope="row">Commands</th><td>Start: <code>{p.runtime.defaultCommand}</code>{p.runtime.defaultInstall && <><br />Install: <code>{p.runtime.defaultInstall}</code></>}<span className="cell-sub">Each website can change them.</span></td></tr>
                </>
              )}
              <tr><th scope="row">{db ? "Database servers" : "Websites"}</th><td>{used === 0 ? "None" : used}</td></tr>
              <tr><th scope="row">Installed</th><td>{relativeTime(p.installedAt)}{p.installedBy && <> by {p.installedBy}</>}{p.updatedAt !== p.installedAt && <span className="cell-sub">Last version {relativeTime(p.updatedAt)}</span>}</td></tr>
            </tbody>
          </table>
          <div>
            <h4 style={{ margin: "0 0 8px" }}>Web servers</h4>
            {p.servers.length === 0 && <p className="muted">No web servers yet. A new web server gets the pack when it joins.</p>}
            <ul className="scope-list">
              {p.servers.map((s) => (
                <li key={s.id}>
                  <strong>{s.name}</strong>
                  <span>
                    <Status tone={SERVER_TONE[s.state]}>{SERVER_TEXT[s.state]}</Status>
                    {s.state === "pending" && s.version && <span className="cell-sub">The server has version {s.version}.</span>}
                    {s.state === "failing" && <span className="cell-sub">The panel sends the pack again with each report of the server. Look at the jobs of the server.</span>}
                  </span>
                </li>
              ))}
            </ul>
          </div>
        </div>
      </div>
      <TableWrap>
        <table>
          <thead><tr><th scope="col">Version</th><th scope="col">Image</th><th scope="col">Digest</th><th scope="col">End of support</th><th scope="col">{db ? "Database servers" : "Websites"}</th></tr></thead>
          <tbody>
            {p.images.map((i) => (
              <tr key={i.id ?? i.version}>
                <td><strong>{i.name}</strong></td>
                <td><code>{i.reference}:{i.tag}</code></td>
                <td><code title={i.digest}>{shortDigest(i.digest)}</code>{i.availableDigest && <span className="cell-sub note-warning">New digest <code>{shortDigest(i.availableDigest)}</code> waits for a rollout</span>}</td>
                <td>{i.supportEndsAt ? longDate(i.supportEndsAt) : "Not given"}</td>
                <td>{db ? i.databaseServers : i.websites}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </TableWrap>
      {waiting.length > 0 && (
        <div className="card-body">
          <p className="muted small" style={{ margin: 0 }}>Websites use {waiting.map((i) => i.name).join(", ")}, so the new image waits. Start the rollout on <Link to="/updates">Images and updates</Link>.</p>
        </div>
      )}
      {removing && (
        <Modal title={`Remove ${p.name}?`} onClose={() => setRemoving(false)} footer={<><button type="button" className="btn" onClick={() => setRemoving(false)}>Cancel</button><button type="button" className="btn btn-danger-solid" disabled={remove.busy || used > 0} onClick={() => { setRemoving(false); void remove.run(); }}>Remove pack</button></>}>
          {used > 0
            ? db
              ? <p style={{ marginTop: 0 }}>{databaseServers(used)} of {p.name} {used === 1 ? "uses" : "use"} this pack. Remove these database servers before you remove the pack.</p>
              : <p style={{ marginTop: 0 }}>{used === 1 ? "1 website uses" : `${used} websites use`} {php ? "a version of PHP of this pack" : "this runtime"}. Change the runtime of these websites, or remove them, before you remove the pack.</p>
            : <p style={{ marginTop: 0 }}>{db ? <>The database engine {p.name}</> : php ? <>{phpList(p.images.map((i) => i.version))} for {phpFamilyName(p.runtime.family)}</> : <>The runtime {p.name}</>} and its images leave the panel. Each web server removes the pack and the rules of its images from the image policy.</p>}
        </Modal>
      )}
      {updating && <InstallDialog pack={p} onClose={() => setUpdating(false)} onInstalled={() => { setUpdating(false); onChange(); }} />}
    </Card>
  );
}

/** Reads a file as text with no change, so that the signature still covers each byte. */
function readFile(e: ChangeEvent<HTMLInputElement>, set: (text: string) => void) {
  const file = e.target.files?.[0];
  if (file) void file.text().then(set);
}

/** Install a pack, or a new version of an installed pack. */
function InstallDialog({ pack, onClose, onInstalled }: { pack?: Pack; onClose: () => void; onInstalled: (p: Pack) => void }) {
  const [manifest, setManifest] = useState("");
  const [signature, setSignature] = useState("");
  const [confirmation, setConfirmation] = useState<Confirmation>(NO_CONFIRMATION);
  const [preview, setPreview] = useState<PackPreview | null>(null);
  const check = useAction(async () => setPreview(await previewPack(manifest, signature)));
  const install = useAction(async () => onInstalled(pack ? await updatePack(pack.id, manifest, signature, confirmation) : await installPack(manifest, signature, confirmation)));
  const error = install.error ?? check.error;
  const m = preview?.manifest;
  const versions = m?.images.map((i) => i.version) ?? [];
  const before = pack?.images.map((i) => i.version) ?? [];
  const added = versions.filter((v) => !before.includes(v));
  const removed = before.filter((v) => !versions.includes(v));
  const php = !!m && isPhpPack(m.runtime);
  const db = m?.kind === "database" ? m.database : null;

  return (
    <Modal wide title={pack ? `Install a new version of ${pack.name}` : "Install pack"} onClose={onClose} footer={
      <>
        <button type="button" className="btn" onClick={preview ? () => setPreview(null) : onClose}>{preview ? "Back" : "Cancel"}</button>
        {preview
          ? <button type="submit" form="pack-install" className="btn btn-primary" disabled={install.busy}>{pack ? "Install version" : "Install pack"}</button>
          : <button type="submit" form="pack-check" className="btn btn-primary" disabled={check.busy || !manifest.trim()}>Check the manifest</button>}
      </>
    }>
      {!preview && (
        <form id="pack-check" noValidate onSubmit={(e) => { e.preventDefault(); void check.run(); }}>
          <FormError error={error} shown={["manifest", "signature"]} />
          <p className="muted" style={{ marginTop: 0 }}>The publisher gives you the file <code>pack.json</code> and its signature <code>pack.json.sig</code>. The panel reads the manifest only as data. It does not run code from it.</p>
          <div className="form-row">
            <Field label="Manifest file" hint="The signature covers each byte. Select the file to keep it exactly, or paste it below.">
              {(p) => <input {...p} type="file" className="input" accept=".json,application/json" onChange={(e) => readFile(e, setManifest)} />}
            </Field>
            <Field label="Signature file">
              {(p) => <input {...p} type="file" className="input" accept=".sig,text/plain" onChange={(e) => readFile(e, (text) => setSignature(text.trim()))} />}
            </Field>
          </div>
          <Field label="Manifest (pack.json)" error={error?.fields.manifest}>
            {(p) => <textarea {...p} className="input manifest-input" spellCheck={false} value={manifest} onChange={(e) => setManifest(e.target.value)} placeholder='{"contract": "cf-pack/1", "kind": "runtime", "slug": "golang", …}' />}
          </Field>
          <Field label="Signature" hint="The text of pack.json.sig, from cosign sign-blob." error={error?.fields.signature}>
            {(p) => <textarea {...p} className="input manifest-input" style={{ minHeight: 64 }} spellCheck={false} value={signature} onChange={(e) => setSignature(e.target.value)} />}
          </Field>
        </form>
      )}
      {preview && m && (
        <form id="pack-install" noValidate onSubmit={(e) => { e.preventDefault(); void install.run(); }}>
          <FormError error={error} shown={["password", "totp"]} />
          <div className="plugin-head">
            <div>
              <h3>{m.name} {m.version}</h3>
              <div className="plugin-meta"><span>By {m.publisher}</span><span><code>{m.slug}</code></span><span>{m.contract}</span></div>
            </div>
            <SignedBy signer={preview.signedBy} developerMode={preview.developerMode} />
          </div>
          {m.description && <p>{m.description}</p>}
          {preview.installed && !pack && <div className="alert alert-warning" role="status"><p>Version {preview.installed.version} of this pack is installed. To change it, use <strong>Install a new version</strong> on its card.</p></div>}
          {pack && preview.installed && <p>This replaces version {preview.installed.version}.{added.length > 0 && <> New versions: {added.join(", ")}.</>}{removed.length > 0 && <> Removed versions: {removed.join(", ")}.</>}</p>}
          <h4>{db ? "Database engine" : "Runtime"}</h4>
          {db
            ? <p>Customers can make database servers of {m.name}, with the version {list(versions)}. A database server keeps its version. The engine listens on port {db.port} in its container, and keeps its data in <code>{db.dataDir}</code>. The websites connect by the network name of the database server. A database server of a pack cannot move to a different server, and has no Adminer and no access from anywhere.</p>
            : php
            ? <p>The pack adds {phpList(versions)} to {phpFamilyName(m.runtime.family)}. Customers can select these versions for a new website, and for a website that uses {phpFamilyName(m.runtime.family)} now. The websites keep the PHP settings of {phpFamilyName(m.runtime.family)}: the extensions of the plan, the php.ini values and the document root.</p>
            : <p>Customers can select {m.name} for a new website. The website runs the start command <code>{m.runtime.defaultCommand}</code>{m.runtime.defaultInstall && <> after the install command <code>{m.runtime.defaultInstall}</code> in a build container</>}. Each website can change the commands.</p>}
          <h4>Images</h4>
          <TableWrap>
            <table>
              <thead><tr><th scope="col">Version</th><th scope="col">Image</th><th scope="col">Digest</th><th scope="col">End of support</th></tr></thead>
              <tbody>
                {m.images.map((i) => (
                  <tr key={i.version}><td><strong>{php ? `PHP ${i.version}` : `${m.name} ${i.version}`}</strong></td><td><code>{i.ref}</code></td><td><code title={i.digest}>{shortDigest(i.digest)}</code></td><td>{i.supportEndsAt ? longDate(i.supportEndsAt) : "Not given"}</td></tr>
                ))}
              </tbody>
            </table>
          </TableWrap>
          <h4>What the pack can do</h4>
          <ul className="muted">
            <li>Each server pulls only these digests, and only with a signature of the key that signed the manifest.</li>
            {db
              ? <li>A database server of the pack runs in gVisor with a read-only root file system, no capabilities and the limits of its plan, as all database servers do.</li>
              : <li>A website of the pack runs in gVisor with a read-only root file system, no capabilities and the limits of its plan, as all websites do.</li>}
            <li>{db ? "The manifest gives only data. The agent runs only the program /cf/manage of the image, in the container of the database server, to make databases and users, and for backups." : "The pack gives only data. It cannot run a command on a server or change the settings of a container."}</li>
          </ul>
          <ConfirmIdentityFields value={confirmation} onChange={setConfirmation} error={install.error} reason={db ? "A pack adds a database engine to each web server." : php ? "A pack adds images to each web server." : "A pack adds a runtime to each web server."} />
        </form>
      )}
    </Modal>
  );
}

function DeveloperMode({ on, onChange }: { on: boolean; onChange: () => void }) {
  const [asking, setAsking] = useState(false);
  const [confirmation, setConfirmation] = useState<Confirmation>(NO_CONFIRMATION);
  const change = useAction(async (value: boolean) => {
    await setPackDeveloperMode(value, confirmation);
    setAsking(false);
    setConfirmation(NO_CONFIRMATION);
    onChange();
  });
  return (
    <Card title="Developer mode">
      <div className="card-body">
        <FormError error={asking ? null : change.error} />
        <Toggle checked={on} label="Developer mode for packs" disabled={change.busy} onChange={(value) => (value ? setAsking(true) : void change.run(false))}>
          Accept packs with no signature. Use it only to develop a pack.
        </Toggle>
      </div>
      {asking && (
        <Modal title="Turn on the developer mode for packs" onClose={() => setAsking(false)} footer={<><button type="button" className="btn" onClick={() => setAsking(false)}>Cancel</button><button type="submit" form="pack-dev" className="btn btn-danger-solid" disabled={change.busy}>Turn on</button></>}>
          <form id="pack-dev" noValidate onSubmit={(e) => { e.preventDefault(); void change.run(true); }}>
            <FormError error={change.error} shown={["password", "totp"]} />
            <p style={{ marginTop: 0 }}>With the developer mode, an administrator can install a pack that nobody signed. Do not turn it on for a production panel.</p>
            <ConfirmIdentityFields value={confirmation} onChange={setConfirmation} error={change.error} reason="The developer mode turns off the signature check of packs." />
          </form>
        </Modal>
      )}
    </Card>
  );
}
