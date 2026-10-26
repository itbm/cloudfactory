import { useState } from "react";
import { Link } from "react-router-dom";
import { useAction, useStore } from "../../api/hooks";
import { addDomain, checkDns, makePrimary, removeDomain, verifyDomain } from "../../api/store";
import type { DomainKind, Website } from "../../api/types";
import { Card, ConfirmButton, Field, FormError, Status } from "../../components/ui";
import { certificateUntil } from "../../lib/labels";

const KIND_LABEL: Record<DomainKind, string> = { primary: "Primary", redirect: "Redirect", alias: "Alias" };

export function DomainsTab({ website }: { website: Website }) {
  const state = useStore();
  const [name, setName] = useState("");
  const [kind, setKind] = useState<DomainKind>("alias");
  const add = useAction(async (n: string, k: DomainKind) => {
    const job = addDomain(website.id, n, k);
    setName("");
    return job;
  });
  const remove = useAction(removeDomain);
  const verify = useAction(verifyDomain);
  const server = state.servers.find((s) => s.id === website.serverId);
  const checking = (d: string) => state.jobs.some((j) => j.websiteId === website.id && j.state === "running" && j.title === `Check DNS for ${d}`);

  return (
    <div className="stack">
      <Card title="Domains of this website">
        <div className="card-body" style={{ paddingBottom: 0 }}>
          <FormError error={remove.error ?? verify.error} />
        </div>
        <div className="table-wrap">
          <table>
            <thead>
              <tr><th scope="col">Domain</th><th scope="col">Type</th><th scope="col">DNS</th><th scope="col">Certificate</th><th scope="col"><span className="visually-hidden">Actions</span></th></tr>
            </thead>
            <tbody>
              {website.domains.map((d) => {
                const zone = state.zones.find((z) => d.name === z.name || d.name.endsWith(`.${z.name}`));
                return (
                  <tr key={d.name}>
                    <td>
                      <strong>{d.name}</strong>
                      {zone && <span className="cell-sub">DNS zone on this panel: <Link to={`/dns/${zone.name}`}>{zone.name}</Link></span>}
                    </td>
                    <td>{KIND_LABEL[d.kind]}{d.kind === "redirect" && <span className="cell-sub">301 to the primary domain</span>}</td>
                    <td>
                      {d.verification ? (
                        <>
                          {/* CF-03: the edge proxy serves the domain only after this check. */}
                          <Status tone="warning">Not verified</Status>
                          <span className="cell-sub">Add this DNS record to prove that you control the domain:</span>
                          <dl className="verify-record">
                            <dt>Type</dt><dd><code>{d.verification.type}</code></dd>
                            <dt>Name</dt><dd><code>{d.verification.name}</code></dd>
                            <dt>Value</dt><dd><code>{d.verification.value}</code></dd>
                          </dl>
                        </>
                      ) : d.dnsPointsHere
                        ? <Status tone="success">Points here</Status>
                        : (
                          <>
                            <Status tone="warning">Does not point here</Status>
                            {/* FR-403: show what the DNS must be. */}
                            <span className="cell-sub">Set an A record to <code>203.0.113.10</code> ({server?.name}).</span>
                          </>
                        )}
                    </td>
                    <td className="muted">{certificateUntil(d.certificate).replace(/^./, (c) => c.toUpperCase())}</td>
                    <td>
                      <div className="btn-group" style={{ justifyContent: "flex-end" }}>
                        {d.verification && (
                          <button type="button" className="btn btn-sm btn-primary" disabled={verify.busy} onClick={() => void verify.run(website.id, d.name)}>
                            {verify.busy ? "Checking…" : "Verify"}
                          </button>
                        )}
                        {!d.verification && !d.dnsPointsHere && (
                          <button type="button" className="btn btn-sm" disabled={checking(d.name)} onClick={() => void checkDns(website.id, d.name)}>
                            {checking(d.name) ? "Checking…" : "Check DNS"}
                          </button>
                        )}
                        {d.kind !== "primary" && !d.verification && <button type="button" className="btn btn-sm" onClick={() => makePrimary(website.id, d.name)}>Make primary</button>}
                        {d.kind !== "primary" && (
                          <ConfirmButton className="btn btn-sm btn-danger" title={`Remove ${d.name}?`} confirm="Remove domain" message={<p>The edge proxy stops sending traffic for {d.name} to this website.</p>} onConfirm={() => void remove.run(website.id, d.name)}>
                            Remove
                          </ConfirmButton>
                        )}
                      </div>
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      </Card>
      <Card title="Add domain">
        <form
          className="card-body"
          noValidate
          onSubmit={(e) => {
            e.preventDefault();
            void add.run(name, kind);
          }}
        >
          <FormError error={add.error} />
          <div className="inline-form">
            <Field label="Domain" error={add.error?.fields.name}>
              {(p) => <input {...p} className="input" value={name} onChange={(e) => setName(e.target.value)} placeholder="www.example.com" autoCapitalize="none" spellCheck={false} />}
            </Field>
            <Field label="Type">
              {(p) => (
                <select {...p} className="select" value={kind} onChange={(e) => setKind(e.target.value as DomainKind)}>
                  <option value="alias">Alias: shows this website</option>
                  <option value="redirect">Redirect: goes to the primary domain</option>
                </select>
              )}
            </Field>
            <div className="field">
              <span className="label" aria-hidden="true">&nbsp;</span>
              <button type="submit" className="btn btn-primary">Add domain</button>
            </div>
          </div>
          <p className="hint" style={{ marginTop: 10 }}>Before the edge proxy serves a new domain, you must prove that you control it with a DNS TXT record. A sub-domain of a verified domain does not need a new record. The edge proxy gets a TLS certificate through ACME when the DNS points to this server.</p>
        </form>
      </Card>
    </div>
  );
}
