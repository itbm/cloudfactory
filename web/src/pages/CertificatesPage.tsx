import { Check, Circle, LoaderCircle } from "lucide-react";
import { useEffect, useState } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { useAction, useStore } from "../api/hooks";
import { contextCustomers, inContext } from "../api/selectors";
import { deleteCertificate, renewCertificate, replaceCertificate, requestCertificate, showCertificate, uploadCertificate } from "../api/store";
import type { CustomerCertificate, DnsZone, State } from "../api/types";
import { Topbar } from "../components/Layout";
import { Card, Empty, Field, FormError, Modal, PageHeader, RowMenu, Status, TableWrap } from "../components/ui";
import { daysUntil, longDate, relativeTime } from "../lib/format";
import { CERTIFICATE_STAGE, CERTIFICATE_STAGES, certificateStatus } from "../lib/labels";
import { CopyField } from "./website/AccessTab";

/**
 * The certificates of the customers (docs/certificates.md): certificates that the customer
 * uploads, and certificates that the panel gets from Let's Encrypt with DNS validation. Each
 * domain of a website uses the best certificate of its customer that is valid for it.
 */
export function CertificatesPage() {
  const { id } = useParams();
  return id ? <CertificatePage id={id} /> : <CertificateList />;
}

const SOURCE_LABEL: Record<CustomerCertificate["source"], string> = { upload: "Uploaded", acme: "Let's Encrypt, DNS validation" };

/** The names of a certificate, short: the first name and the number of the others. */
function shortNames(c: CustomerCertificate): string {
  return c.names.length > 2 ? `${c.names[0]} and ${c.names.length - 1} more names` : c.names.join(", ");
}

/** The zones that can hold the records of the DNS validation: the registrar sends the queries to this panel. */
function usableZones(state: State, customerId: string): DnsZone[] {
  return state.dnsZones.filter((z) => z.customerId === customerId && z.status !== "removing" && z.delegation.state === "here");
}

function ExpiryCell({ c }: { c: CustomerCertificate }) {
  if (!c.expiresAt) return <span className="muted">None</span>;
  const days = daysUntil(c.expiresAt);
  return (
    <>
      <span className="nowrap">{longDate(c.expiresAt)}</span>
      <span className={days <= 14 && c.source === "upload" ? "cell-sub text-danger" : "cell-sub"}>
        {days < 0 ? "Expired" : c.source === "acme" && c.renewAt ? `Renews ${relativeTime(c.renewAt)}` : `In ${days} days`}
      </span>
    </>
  );
}

function CertificateList() {
  const state = useStore();
  const navigate = useNavigate();
  const customers = contextCustomers(state);
  const certificates = state.certificates.filter((c) => inContext(state, c.customerId));
  const [dialog, setDialog] = useState<"upload" | "acme" | null>(null);
  const [replacing, setReplacing] = useState<CustomerCertificate | null>(null);
  const [removing, setRemoving] = useState<CustomerCertificate | null>(null);
  const renew = useAction(renewCertificate);
  const remove = useAction(deleteCertificate);
  const customer = (id: string) => state.customers.find((c) => c.id === id);
  const showCustomer = customers.length > 1 || certificates.some((c) => c.customerId !== customers[0]?.id);

  return (
    <>
      <Topbar />
      <main id="main" className="content content-wide">
        <PageHeader
          title="Certificates"
          description="The TLS certificates of your domains. The edge proxy gets a certificate for each domain by itself. Add a certificate here to use your own certificate, or to get one wildcard certificate for all the names of a domain."
          actions={
            <div className="btn-group">
              <button type="button" className="btn" onClick={() => setDialog("upload")}>Upload certificate</button>
              <button type="button" className="btn btn-primary" onClick={() => setDialog("acme")}>Get a wildcard certificate</button>
            </div>
          }
        />
        <div className="stack">
          <Card title="Certificates">
            <div className="card-body" style={{ paddingBottom: 12 }}>
              <p className="muted">Each domain of a website uses the certificate of its customer that is valid for it: a certificate with the exact name first, then a wildcard certificate. A domain with no such certificate gets a certificate from the edge proxy.</p>
              <FormError error={renew.error ?? remove.error} />
            </div>
            <TableWrap>
              <table>
                <thead>
                  <tr>
                    <th scope="col">Names</th>
                    <th scope="col">Source</th>
                    <th scope="col">Status</th>
                    <th scope="col">Expires</th>
                    <th scope="col">Used by</th>
                    <th scope="col"><span className="visually-hidden">Actions</span></th>
                  </tr>
                </thead>
                <tbody>
                  {certificates.map((c) => {
                    const status = certificateStatus(c);
                    return (
                      <tr key={c.id}>
                        <td>
                          <Link className="cell-title" to={`/certificates/${c.id}`}>{shortNames(c)}</Link>
                          {showCustomer && <span className="cell-sub">{customer(c.customerId)?.name ?? "Removed customer"}</span>}
                        </td>
                        <td>
                          {SOURCE_LABEL[c.source]}
                          {c.issuer && <span className="cell-sub">{c.issuer}</span>}
                        </td>
                        <td style={{ minWidth: 140 }}>
                          <Status tone={status.tone}>{status.label}</Status>
                          {c.order && <span className="cell-sub">{CERTIFICATE_STAGE[c.order.stage]}…</span>}
                          {!c.order && c.error && c.state !== "failed" && <span className="cell-sub text-danger">The last renewal failed</span>}
                          {c.trusted === false && <span className="cell-sub">Browsers do not trust it</span>}
                        </td>
                        <td><ExpiryCell c={c} /></td>
                        <td style={{ whiteSpace: "nowrap" }}>{c.usedBy.length === 0 ? <span className="muted">No domain</span> : `${c.usedBy.length} ${c.usedBy.length === 1 ? "domain" : "domains"}`}</td>
                        <td className="cell-actions">
                          <RowMenu label={`Actions for the certificate ${c.names[0]}`} actions={[
                            { label: "Open", onSelect: () => navigate(`/certificates/${c.id}`) },
                            ...(c.source === "acme" ? [{ label: c.state === "failed" ? "Try again" : "Renew now", onSelect: () => void renew.run(c.id) }] : [{ label: "Replace", onSelect: () => setReplacing(c) }]),
                            { label: "Remove", danger: true, onSelect: () => setRemoving(c) },
                          ]} />
                        </td>
                      </tr>
                    );
                  })}
                  {certificates.length === 0 && <tr><td colSpan={6} className="muted">No certificates. Each domain uses a certificate from the edge proxy.</td></tr>}
                </tbody>
              </table>
            </TableWrap>
          </Card>
        </div>
      </main>
      {dialog === "upload" && <UploadDialog onClose={() => setDialog(null)} />}
      {dialog === "acme" && <RequestDialog onClose={() => setDialog(null)} />}
      {replacing && <UploadDialog replace={replacing} onClose={() => setReplacing(null)} />}
      {removing && <RemoveDialog certificate={removing} onClose={() => setRemoving(null)} onRemove={() => void remove.run(removing.id)} />}
    </>
  );
}

function CustomerField({ customerId, setCustomerId, error }: { customerId: string; setCustomerId: (id: string) => void; error?: string }) {
  const state = useStore();
  const customers = contextCustomers(state);
  if (customers.length <= 1) return null;
  return (
    <Field label="Customer" error={error}>
      {(p) => (
        <select {...p} className="select" value={customerId} onChange={(e) => setCustomerId(e.target.value)}>
          {customers.map((c) => <option key={c.id} value={c.id}>{c.name}</option>)}
        </select>
      )}
    </Field>
  );
}

/** The customer for a new item: the customer of the context, or the first one. */
function useFirstCustomer(state: State) {
  const customers = contextCustomers(state);
  return useState(() => customers.find((c) => c.id === state.session.contextId)?.id ?? customers[0]?.id ?? "");
}

/** A text area for PEM data, with a button that reads a file. */
function PemField({ label, hint, value, onChange, error, placeholder }: { label: string; hint: string; value: string; onChange: (v: string) => void; error?: string; placeholder: string }) {
  return (
    <Field label={label} hint={hint} error={error}>
      {(p) => (
        <>
          <textarea {...p} className="input mono" rows={6} value={value} onChange={(e) => onChange(e.target.value)} placeholder={placeholder} spellCheck={false} autoCapitalize="none" />
          <label className="btn btn-sm" style={{ marginTop: 8, display: "inline-flex" }}>
            Open a file…
            <input type="file" className="visually-hidden" accept=".pem,.crt,.cer,.key,.txt" onChange={(e) => {
              const file = e.target.files?.[0];
              if (file) void file.text().then(onChange);
            }} />
          </label>
        </>
      )}
    </Field>
  );
}

function UploadDialog({ replace, onClose }: { replace?: CustomerCertificate; onClose: () => void }) {
  const state = useStore();
  const navigate = useNavigate();
  const [customerId, setCustomerId] = useFirstCustomer(state);
  const [chain, setChain] = useState("");
  const [key, setKey] = useState("");
  const upload = useAction(uploadCertificate);
  const change = useAction(replaceCertificate);
  const action = replace ? change : upload;

  return (
    <Modal
      wide
      title={replace ? `Replace the certificate ${replace.names[0]}` : "Upload a certificate"}
      onClose={onClose}
      footer={<><button type="button" className="btn" onClick={onClose}>Cancel</button><button type="submit" form="upload-certificate" className="btn btn-primary" disabled={action.busy}>{replace ? "Replace certificate" : "Upload certificate"}</button></>}
    >
      <form id="upload-certificate" noValidate onSubmit={async (e) => {
        e.preventDefault();
        if (replace) {
          const r = await change.run(replace.id, chain, key);
          if (r.ok) onClose();
          return;
        }
        const r = await upload.run(customerId, chain, key);
        if (r.ok) {
          onClose();
          navigate(`/certificates/${r.value}`);
        }
      }}>
        <FormError error={action.error} shown={["certificate", "privateKey", "customerId"]} />
        {!replace && <CustomerField customerId={customerId} setCustomerId={setCustomerId} error={upload.error?.fields.customerId} />}
        <div style={{ marginTop: 14 }}>
          <PemField label="Certificate chain" hint="The certificate and the intermediate certificates of the CA, in PEM format. The order is not important." value={chain} onChange={setChain} error={action.error?.fields.certificate} placeholder="-----BEGIN CERTIFICATE-----" />
        </div>
        <div style={{ marginTop: 14 }}>
          <PemField label="Private key" hint="The private key of the certificate, in PEM format, with no password. The panel keeps it encrypted, and only the edge proxy of a server gets it." value={key} onChange={setKey} error={action.error?.fields.privateKey} placeholder="-----BEGIN PRIVATE KEY-----" />
        </div>
        <p className="muted small" style={{ marginTop: 14 }}>
          {replace
            ? "The domains that use the certificate get the new certificate. The names of the new certificate can be different."
            : "The domains of the websites of the customer that the certificate is valid for get it. The panel does not renew an uploaded certificate: upload a new certificate before it expires."}
        </p>
      </form>
    </Modal>
  );
}

function RequestDialog({ onClose }: { onClose: () => void }) {
  const state = useStore();
  const navigate = useNavigate();
  const [customerId, setCustomerId] = useFirstCustomer(state);
  const zones = usableZones(state, customerId);
  const [domain, setDomain] = useState(() => zones[0]?.name ?? "");
  const [wildcard, setWildcard] = useState(true);
  const request = useAction(requestCertificate);
  const name = domain.trim().toLowerCase().replace(/\.$/, "") || "example.com";

  return (
    <Modal
      title="Get a wildcard certificate"
      onClose={onClose}
      footer={<><button type="button" className="btn" onClick={onClose}>Cancel</button><button type="submit" form="request-certificate" className="btn btn-primary" disabled={request.busy}>Get certificate</button></>}
    >
      <form id="request-certificate" noValidate onSubmit={async (e) => {
        e.preventDefault();
        const r = await request.run(customerId, domain, wildcard);
        if (r.ok) {
          onClose();
          navigate(`/certificates/${r.value}`);
        }
      }}>
        <FormError error={request.error} shown={["domain", "customerId", "wildcard"]} />
        <p className="muted" style={{ marginBottom: 14 }}>
          The panel gets the certificate from Let's Encrypt. To prove that you control the domain, the panel adds a TXT record to the DNS zone of the domain on this panel. The panel renews the certificate by itself.
        </p>
        <CustomerField customerId={customerId} setCustomerId={(id) => { setCustomerId(id); setDomain(usableZones(state, id)[0]?.name ?? ""); }} error={request.error?.fields.customerId} />
        <div style={{ marginTop: 14 }}>
          <Field label="Domain" hint="A domain with a DNS zone on this panel. The registrar of the domain must have the name servers of this panel." error={request.error?.fields.domain}>
            {(p) => (
              <>
                <input {...p} className="input" list="certificate-zones" value={domain} onChange={(e) => setDomain(e.target.value)} placeholder="example.com" autoCapitalize="none" spellCheck={false} />
                <datalist id="certificate-zones">{zones.map((z) => <option key={z.id} value={z.name} />)}</datalist>
              </>
            )}
          </Field>
        </div>
        <label className="check" style={{ marginTop: 14 }}>
          <input type="checkbox" checked={wildcard} onChange={(e) => setWildcard(e.target.checked)} />
          <span>Also for each name directly below the domain (<code>*.{name}</code>)</span>
        </label>
        {zones.length === 0 && (
          <p className="alert alert-info" role="status" style={{ marginTop: 14 }}>
            This customer has no DNS zone that the registrar sends to this panel. Make a zone on the <Link to="/dns">DNS</Link> page, and give the name servers of the panel to the registrar of the domain.
          </p>
        )}
      </form>
    </Modal>
  );
}

function RemoveDialog({ certificate, onClose, onRemove }: { certificate: CustomerCertificate; onClose: () => void; onRemove: () => void }) {
  return (
    <Modal
      title={`Remove the certificate ${certificate.names[0]}?`}
      onClose={onClose}
      footer={<><button type="button" className="btn" onClick={onClose}>Cancel</button><button type="button" className="btn btn-danger-solid" onClick={() => { onClose(); onRemove(); }}>Remove certificate</button></>}
    >
      <p>The panel deletes the certificate and its private key. You cannot restore them.</p>
      {certificate.usedBy.length > 0 && (
        <p style={{ marginTop: 10 }}>{certificate.usedBy.length} {certificate.usedBy.length === 1 ? "domain uses" : "domains use"} the certificate now. Each domain then uses a different certificate of the customer, or a certificate from the edge proxy.</p>
      )}
    </Modal>
  );
}

/** The stages of an order, with the stage that runs now. */
function OrderStages({ c }: { c: CustomerCertificate }) {
  if (!c.order) return null;
  const current = CERTIFICATE_STAGES.indexOf(c.order.stage);
  return (
    <ol className="cluster-steps" aria-label="Stages of the order">
      {CERTIFICATE_STAGES.map((s, i) => {
        const step = i < current ? "done" : i > current ? "todo" : "now";
        const Icon = { done: Check, now: LoaderCircle, todo: Circle }[step];
        return (
          <li key={s} className={`cluster-step cluster-step-${step}`} aria-current={step === "now" ? "step" : undefined}>
            <Icon size={16} aria-hidden="true" />
            <span>{CERTIFICATE_STAGE[s]}</span>
            <span className="visually-hidden">{{ done: "Done", now: "In progress", todo: "Not started" }[step]}</span>
          </li>
        );
      })}
    </ol>
  );
}

function CertificatePage({ id }: { id: string }) {
  const state = useStore();
  const navigate = useNavigate();
  const c = state.certificates.find((x) => x.id === id && inContext(state, x.customerId));
  const exists = !!c;
  // The list has no chain. The page reads the certificate with its chain.
  useEffect(() => { if (exists) void showCertificate(id); }, [id, exists]);
  const [replacing, setReplacing] = useState(false);
  const [removing, setRemoving] = useState(false);
  const renew = useAction(renewCertificate);
  const remove = useAction(deleteCertificate);

  if (!c) {
    return (
      <>
        <Topbar />
        <main id="main" className="content"><Empty title="This certificate does not exist"><p><Link to="/certificates">Go to all certificates</Link></p></Empty></main>
      </>
    );
  }
  const status = certificateStatus(c);
  const customer = state.customers.find((x) => x.id === c.customerId);

  return (
    <>
      <Topbar />
      <main id="main" className="content content-wide">
        <ol className="breadcrumbs"><li><Link to="/certificates">Certificates</Link></li><li aria-current="page">{c.names[0]}</li></ol>
        <div className="detail-header">
          <div>
            <div className="detail-title"><h1>{c.names[0]}</h1><Status tone={status.tone}>{status.label}</Status></div>
            <p className="detail-meta">{[customer?.name, SOURCE_LABEL[c.source], `${c.names.length} ${c.names.length === 1 ? "name" : "names"}`].filter(Boolean).join(" · ")}</p>
          </div>
          <div className="btn-group">
            <button type="button" className="btn btn-danger" onClick={() => setRemoving(true)}>Remove</button>
            {c.source === "acme"
              ? <button type="button" className="btn btn-primary" disabled={renew.busy} onClick={() => void renew.run(c.id)}>{c.state === "failed" ? "Try again" : c.order ? "Start again" : "Renew now"}</button>
              : <button type="button" className="btn btn-primary" onClick={() => setReplacing(true)}>Replace</button>}
          </div>
        </div>
        <FormError error={renew.error ?? remove.error} />

        {c.error && !c.order && (
          <div className="alert alert-danger alert-stack mt" role="alert">
            <p>
              <strong>{c.state === "failed" ? "The panel could not get the certificate." : "The last renewal failed."}</strong>{" "}
              {c.state === "failed" ? "Correct the cause, and then try again." : <>The current certificate stays in use until it expires.{c.nextTryAt && <> The panel tries again {relativeTime(c.nextTryAt)}.</>}</>}
            </p>
            <div className="cause">{c.error}</div>
          </div>
        )}
        {c.source === "upload" && c.expiresAt && daysUntil(c.expiresAt) <= 14 && (
          <div className={`alert ${daysUntil(c.expiresAt) < 0 ? "alert-danger" : "alert-warning"} alert-stack mt`} role="status">
            <p><strong>{daysUntil(c.expiresAt) < 0 ? "The certificate expired." : `The certificate expires in ${daysUntil(c.expiresAt)} days.`}</strong> The panel does not renew an uploaded certificate. Get a new certificate from your CA, and replace this one. {daysUntil(c.expiresAt) < 0 && "Until then, its domains use a certificate from the edge proxy."}</p>
          </div>
        )}

        {c.order && (
          <Card className="mt" title={c.state === "renewing" ? "Renewal" : "Order"}>
            <div className="card-body">
              <OrderStages c={c} />
              <p className="muted" style={{ marginTop: 12 }}>
                {c.order.stage === "dns" && "The panel added these TXT records to the DNS zone, and waits until each DNS server has them."}
                {c.order.stage === "validating" && "Let's Encrypt checks the TXT records now."}
                {c.order.stage === "finalizing" && "Let's Encrypt makes the certificate now."}
                {c.order.stage === "new" && "The panel makes the order now."}
                {" "}The panel removes the records after the order. This page shows the new state by itself.
              </p>
              {c.order.records.length > 0 && (
                <TableWrap>
                  <table>
                    <thead><tr><th scope="col">Name</th><th scope="col">Type</th><th scope="col">Value</th></tr></thead>
                    <tbody>{c.order.records.map((r) => <tr key={r.value}><td><code>{r.name}</code></td><td><span className="tag">TXT</span></td><td style={{ overflowWrap: "anywhere" }}><code>{r.value}</code></td></tr>)}</tbody>
                  </table>
                </TableWrap>
              )}
            </div>
          </Card>
        )}

        <div className="grid-2 mt">
          <Card title="Details">
            <div className="card-body">
              <table className="kv">
                <tbody>
                  <tr><th scope="row">Names</th><td>{c.names.map((n) => <code key={n} style={{ display: "block" }}>{n}</code>)}</td></tr>
                  <tr><th scope="row">Issuer</th><td>{c.issuer ?? <span className="muted">None yet</span>}{c.trusted === false && <span className="cell-sub">Browsers do not trust this issuer.</span>}</td></tr>
                  <tr><th scope="row">Valid from</th><td>{c.notBefore ? longDate(c.notBefore) : <span className="muted">None yet</span>}</td></tr>
                  <tr><th scope="row">Expires</th><td>{c.expiresAt ? <>{longDate(c.expiresAt)} <span className="muted">({relativeTime(c.expiresAt)})</span></> : <span className="muted">None yet</span>}</td></tr>
                  {c.source === "acme" && <tr><th scope="row">Renewal</th><td>{c.renewAt ? longDate(c.renewAt) : <span className="muted">After the first certificate</span>}<span className="cell-sub">After two thirds of the lifetime of the certificate.</span></td></tr>}
                  <tr><th scope="row">SHA-256</th><td style={{ overflowWrap: "anywhere" }}>{c.fingerprint ? <code>{c.fingerprint}</code> : <span className="muted">None yet</span>}</td></tr>
                </tbody>
              </table>
            </div>
          </Card>
          <Card title="Used by">
            <TableWrap>
              <table>
                <thead><tr><th scope="col">Domain</th><th scope="col">Website</th><th scope="col">Server</th></tr></thead>
                <tbody>
                  {c.usedBy.map((u) => (
                    <tr key={`${u.serverName}-${u.domain}`}>
                      <td>{u.domain}</td>
                      <td>{u.websiteId ? <Link to={`/websites/${u.websiteId}/domains`}>{u.websiteName}</Link> : <span className="muted">None</span>}</td>
                      <td className="muted" style={{ whiteSpace: "nowrap" }}>{u.serverName}</td>
                    </tr>
                  ))}
                  {c.usedBy.length === 0 && <tr><td colSpan={3} className="muted">No domain uses the certificate now. A domain of a website of the customer that the certificate is valid for gets it.</td></tr>}
                </tbody>
              </table>
            </TableWrap>
          </Card>
        </div>

        {c.certificatePem && (
          <Card className="mt" title="Certificate chain">
            <div className="card-body">
              <p className="muted" style={{ marginBottom: 10 }}>The certificate and the intermediate certificates in PEM format. The panel never shows the private key.</p>
              <CopyField value={c.certificatePem} label="Certificate chain in PEM format" />
            </div>
          </Card>
        )}
      </main>
      {replacing && <UploadDialog replace={c} onClose={() => setReplacing(false)} />}
      {removing && <RemoveDialog certificate={c} onClose={() => setRemoving(false)} onRemove={() => void remove.run(c.id).then((r) => { if (r.ok) navigate("/certificates"); })} />}
    </>
  );
}
