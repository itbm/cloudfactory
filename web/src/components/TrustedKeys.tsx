import { useState } from "react";
import { useAction } from "../api/hooks";
import { addTrustedKey, removeTrustedKey } from "../api/store";
import type { Confirmation, TrustedKey } from "../api/types";
import { relativeTime } from "../lib/format";
import { ConfirmIdentityFields, NO_CONFIRMATION } from "./ConfirmIdentity";
import { Card, ConfirmButton, Field, FormError, Modal, TableWrap } from "./ui";

/**
 * The public keys of the publishers that the administrator trusts. The Plugins page and the
 * Packs page show the same list: one key can sign plugins and packs.
 */
export function TrustedKeys({ keys, onChange }: { keys: TrustedKey[]; onChange: () => void }) {
  const [adding, setAdding] = useState(false);
  const [name, setName] = useState("");
  const [publicKey, setPublicKey] = useState("");
  const [confirmation, setConfirmation] = useState<Confirmation>(NO_CONFIRMATION);
  const add = useAction(async () => {
    await addTrustedKey(name, publicKey, confirmation);
    setAdding(false);
    setName("");
    setPublicKey("");
    setConfirmation(NO_CONFIRMATION);
    onChange();
  });
  const remove = useAction(async (id: string) => { await removeTrustedKey(id); onChange(); });
  return (
    <Card title="Trusted keys" actions={<button type="button" className="btn" onClick={() => setAdding(true)}>Add key</button>}>
      <div className="card-body" style={{ paddingBottom: 12 }}>
        <p className="muted" style={{ marginTop: 0 }}>The panel installs a plugin or a pack only when one of these keys signed its manifest. Add only the keys of publishers that you trust.</p>
        <FormError error={remove.error} />
      </div>
      <TableWrap>
        <table>
          <thead><tr><th scope="col">Publisher</th><th scope="col">Fingerprint (SHA-256)</th><th scope="col">Added</th><th scope="col"><span className="visually-hidden">Actions</span></th></tr></thead>
          <tbody>
            {keys.map((k) => (
              <tr key={k.id}>
                <td><strong>{k.name}</strong></td>
                <td><code>{k.fingerprint.match(/.{1,4}/g)?.slice(0, 8).join(" ")} …</code></td>
                <td>{relativeTime(k.createdAt)}</td>
                <td className="cell-actions" style={{ width: 110 }}>
                  <ConfirmButton className="btn btn-sm btn-danger" title={`Stop trusting ${k.name}?`} confirm="Remove key" message={<p>The installed plugins stay. The panel keeps a key while a pack that it signed is installed, because each server checks the pack with the key. A new install or a new version needs a different trusted key.</p>} onConfirm={() => void remove.run(k.id)}>Remove</ConfirmButton>
                </td>
              </tr>
            ))}
            {keys.length === 0 && <tr><td colSpan={4} className="muted">No trusted keys.</td></tr>}
          </tbody>
        </table>
      </TableWrap>
      {adding && (
        <Modal title="Add a trusted key" onClose={() => setAdding(false)} footer={<><button type="button" className="btn" onClick={() => setAdding(false)}>Cancel</button><button type="submit" form="trusted-key" className="btn btn-primary" disabled={add.busy}>Add key</button></>}>
          <form id="trusted-key" noValidate onSubmit={(e) => { e.preventDefault(); void add.run(); }}>
            <FormError error={add.error} shown={["name", "publicKey", "password", "totp"]} />
            <Field label="Publisher" error={add.error?.fields.name}>{(p) => <input {...p} className="input" value={name} onChange={(e) => setName(e.target.value)} placeholder="Example Packs Ltd" />}</Field>
            <Field label="Public key" hint="An ECDSA P-256 key in PEM format, for example the file cosign.pub of the publisher." error={add.error?.fields.publicKey}>
              {(p) => <textarea {...p} className="input manifest-input" spellCheck={false} value={publicKey} onChange={(e) => setPublicKey(e.target.value)} placeholder="-----BEGIN PUBLIC KEY-----" />}
            </Field>
            <ConfirmIdentityFields value={confirmation} onChange={setConfirmation} error={add.error} reason="A trusted key lets its owner sign plugins and packs for this panel." />
          </form>
        </Modal>
      )}
    </Card>
  );
}
