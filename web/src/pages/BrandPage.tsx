// White label. A reseller sets its brand, its panel domain, its name server host names and its
// mail host name on this page. The administrator sets the default brand in Settings, with the
// same form (BrandForm).

import { useEffect, useState } from "react";
import { useAction, useStore } from "../api/hooks";
import { ApiError, defaultBrand, saveDefaultBrand, saveResellerBrand } from "../api/store";
import type { Brand, BrandFields, Customer } from "../api/types";
import { BrandMark, BUILT_IN, brandColours } from "../components/Brand";
import { Topbar } from "../components/Layout";
import { Card, Field, FormError, PageHeader, Status } from "../components/ui";
import { longDate, relativeTime } from "../lib/format";
import { CopyField } from "./website/AccessTab";

/** The largest logo, as the API checks it. */
const LOGO_BYTES = 256 * 1024;
const LOGO_TYPES = ["image/png", "image/jpeg", "image/webp"];

/** The contrast of white text on a colour (WCAG 2.2). The API needs 4.5:1 or more. */
export function contrastWithWhite(hex: string): number {
  const channel = (i: number) => {
    const c = parseInt(hex.slice(i, i + 2), 16) / 255;
    return c <= 0.04045 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4;
  };
  const l = 0.2126 * channel(1) + 0.7152 * channel(3) + 0.0722 * channel(5);
  return 1.05 / (l + 0.05);
}

const HEX = /^#[0-9a-fA-F]{6}$/;

type BrandChange = { [K in keyof BrandFields]?: BrandFields[K] | null };

/**
 * The name, colour, logo and support email of a brand, with a preview. "fallback" gives the
 * values that the panel shows for an empty field.
 */
export function BrandForm({ saved, fallback, onSave, description }: {
  saved: BrandFields;
  fallback: { name: string; colour: string; supportEmail: string | null };
  onSave: (change: BrandChange) => Promise<{ ok: boolean }>;
  description: string;
}) {
  const [name, setName] = useState(saved.name ?? "");
  const [colour, setColour] = useState(saved.colour ?? "");
  const [logo, setLogo] = useState<string | null>(saved.logo ?? null);
  const [email, setEmail] = useState(saved.supportEmail ?? "");
  const [logoError, setLogoError] = useState<string | null>(null);
  const [error, setError] = useState<ApiError | null>(null);
  const [busy, setBusy] = useState(false);
  const [done, setDone] = useState(false);

  const shownColour = HEX.test(colour) ? colour : fallback.colour;
  const contrast = HEX.test(colour) ? contrastWithWhite(colour) : null;
  const preview: Brand = { name: name.trim() || fallback.name, colour: shownColour, logo, supportEmail: email || fallback.supportEmail, builtIn: false };

  const readLogo = (file: File | undefined) => {
    setLogoError(null);
    if (!file) return;
    if (!LOGO_TYPES.includes(file.type)) { setLogoError("Select a PNG, JPEG or WebP image. SVG files are not allowed."); return; }
    if (file.size > LOGO_BYTES) { setLogoError("Select an image of 256 KB or less."); return; }
    const reader = new FileReader();
    reader.onload = () => setLogo(String(reader.result));
    reader.readAsDataURL(file);
  };

  const save = async () => {
    setBusy(true); setError(null); setDone(false);
    const change: BrandChange = { name: name.trim() || null, colour: colour.trim() || null, logo, supportEmail: email.trim() || null };
    try {
      await onSave(change);
      setDone(true);
    } catch (e) {
      setError(e instanceof ApiError ? e : new ApiError(String(e)));
    } finally {
      setBusy(false);
    }
  };

  return (
    <Card title="Name, colour and logo">
      <form className="card-body" noValidate onSubmit={(e) => { e.preventDefault(); void save(); }}>
        <p className="muted" style={{ marginBottom: 16 }}>{description}</p>
        <FormError error={error} shown={["name", "colour", "logo", "supportEmail", "branding.name", "branding.colour", "branding.logo", "branding.supportEmail"]} />
        <div className="brand-form">
          <div>
            <Field label="Name" hint={`Shown in the menu, on the sign-in page and in the browser tab. Empty: ${fallback.name}.`} error={error?.fields.name ?? error?.fields["branding.name"]}>
              {(p) => <input {...p} className="input" maxLength={100} value={name} placeholder={fallback.name} onChange={(e) => setName(e.target.value)} />}
            </Field>
            <Field
              label="Colour"
              hint={contrast !== null && contrast < 4.5 ? undefined : "The colour of the buttons and links. White text on it must be easy to read."}
              error={error?.fields.colour ?? error?.fields["branding.colour"] ?? (contrast !== null && contrast < 4.5 ? `Select a darker colour. White text on this colour has a contrast of ${contrast.toFixed(1)}:1, and it must be at least 4.5:1.` : undefined)}
            >
              {(p) => (
                <div className="colour-input">
                  <input type="color" aria-label="Select a colour" value={shownColour} onChange={(e) => setColour(e.target.value)} />
                  <input {...p} className="input mono" value={colour} placeholder={fallback.colour} maxLength={7} onChange={(e) => setColour(e.target.value.trim())} />
                </div>
              )}
            </Field>
            <Field label="Logo" hint="PNG, JPEG or WebP, 256 KB or less. A square image of 64 by 64 pixels or more looks best." error={logoError ?? error?.fields.logo ?? error?.fields["branding.logo"]}>
              {(p) => (
                <div className="logo-input">
                  {logo ? <img src={logo} alt="The current logo" /> : <span className="muted">No logo. The menu shows the first letter of the name.</span>}
                  <input {...p} type="file" accept={LOGO_TYPES.join(",")} onChange={(e) => readLogo(e.target.files?.[0])} />
                  {logo && <button type="button" className="btn" onClick={() => setLogo(null)}>Remove logo</button>}
                </div>
              )}
            </Field>
            <Field label="Support email" hint={fallback.supportEmail ? `Your customers see a help link to it in the menu. Empty: ${fallback.supportEmail}.` : "Your customers see a help link to it in the menu."} error={error?.fields.supportEmail ?? error?.fields["branding.supportEmail"]}>
              {(p) => <input {...p} className="input" type="email" value={email} placeholder={fallback.supportEmail ?? "support@example.com"} onChange={(e) => setEmail(e.target.value)} />}
            </Field>
          </div>
          <div>
            <p className="label-like">Preview</p>
            <div className="brand-preview-panel" style={brandColours(shownColour) as React.CSSProperties}>
              <div className="brand-preview-nav"><span className="brand"><BrandMark brand={preview} /></span></div>
              <div className="brand-preview-page">
                <p style={{ fontWeight: 600, marginBottom: 8 }}>Sign in to {preview.name}</p>
                <span className="btn btn-primary" aria-hidden="true">Sign in</span>
                <p style={{ marginTop: 10 }}><a href="#preview" onClick={(e) => e.preventDefault()}>A link</a></p>
              </div>
            </div>
          </div>
        </div>
        <div className="btn-group" style={{ marginTop: 16 }}>
          <button type="submit" className="btn btn-primary" disabled={busy || logoError !== null || (contrast !== null && contrast < 4.5)}>{busy ? "Saving…" : "Save"}</button>
          {done && <span role="status" className="muted">Saved.</span>}
        </div>
      </form>
    </Card>
  );
}

/** Settings, Brand: the default brand of the panel. Only an administrator. */
export function DefaultBrandSettings() {
  const [saved, setSaved] = useState<BrandFields | null>(null);
  const [error, setError] = useState<string | null>(null);
  useEffect(() => {
    defaultBrand().then(setSaved).catch((e) => setError(e instanceof Error ? e.message : String(e)));
  }, []);
  if (error) return <div className="alert alert-danger" role="alert"><p>{error}</p></div>;
  if (!saved) return <p className="muted" role="status">Loading…</p>;
  // The API gives the built-in values for the fields that the administrator did not set.
  const own: BrandFields = {
    name: saved.name === BUILT_IN.name ? undefined : saved.name,
    colour: saved.colour === BUILT_IN.colour ? undefined : saved.colour,
    logo: saved.logo ?? undefined,
    supportEmail: saved.supportEmail ?? undefined,
  };
  return (
    <div className="stack">
      <BrandForm
        saved={own}
        fallback={{ name: BUILT_IN.name, colour: BUILT_IN.colour, supportEmail: null }}
        description="The administrators and the customers of the panel see this brand. Each reseller can set its own brand: its customers see only the brand of the reseller."
        onSave={async (change) => { setSaved(await saveDefaultBrand(change)); return { ok: true }; }}
      />
    </div>
  );
}

/** The page of a reseller: its brand, its panel domain, its name servers and its mail host. */
export function BrandPage() {
  const state = useStore();
  // A reseller sees its own record in the list of customers.
  const me = state.customers.find((c) => c.role === "reseller");
  return (
    <>
      <Topbar />
      <main id="main" className="content content-wide">
        <PageHeader title="Brand" description="Your customers see only your brand: your name, logo and colour, your panel domain and your name servers." />
        {!me ? <p className="muted" role="status">Loading…</p> : <ResellerBrand reseller={me} />}
      </main>
    </>
  );
}

function ResellerBrand({ reseller }: { reseller: Customer }) {
  const b = reseller.branding ?? {};
  return (
    <div className="stack">
      <BrandForm
        saved={b}
        fallback={{ name: reseller.name, colour: BUILT_IN.colour, supportEmail: reseller.email }}
        description="You and your customers see this brand in the panel. An empty field has the value in grey."
        onSave={async (change) => { await saveResellerBrand(reseller.id, change); return { ok: true }; }}
      />
      <PanelDomainCard reseller={reseller} />
      {(reseller.brandStatus?.panelNameServers.length ?? 0) > 0 && <NameServersCard reseller={reseller} />}
      <MailHostCard reseller={reseller} />
    </div>
  );
}

/** A TXT record that the error message of the API gives, as a copy field. */
function ProofHint({ error }: { error?: string }) {
  const m = error?.match(/TXT record (\S+) with the value (\S+?),/);
  if (!error) return null;
  return (
    <div className="alert alert-warning alert-stack" role="alert">
      <p>{error}</p>
      {m && <><CopyField label="Name of the TXT record" value={m[1]} /><CopyField label="Value of the TXT record" value={m[2]} /></>}
    </div>
  );
}

function PanelDomainCard({ reseller }: { reseller: Customer }) {
  const current = reseller.branding?.panelDomain ?? "";
  const [domain, setDomain] = useState(current);
  const save = useAction((d: string | null) => saveResellerBrand(reseller.id, { panelDomain: d }));
  const cert = reseller.brandStatus?.certificate;
  const panel = reseller.brandStatus?.panelDomain ?? "the panel domain";
  const fieldError = save.error?.fields["branding.panelDomain"];
  const proof = fieldError?.startsWith("Prove") ? fieldError : undefined;
  return (
    <Card title="Panel domain">
      <form className="card-body" noValidate onSubmit={(e) => { e.preventDefault(); void save.run(domain.trim().toLowerCase() || null); }}>
        <p className="muted" style={{ marginBottom: 16 }}>
          Your customers can sign in at your own domain, for example <code>panel.your-agency.com</code>. Point the domain to <code>{panel}</code> with a CNAME record. The panel then gets a TLS certificate for it from Let's Encrypt.
        </p>
        <FormError error={save.error} shown={["branding.panelDomain"]} />
        <ProofHint error={proof} />
        <Field label="Domain" error={proof ? undefined : fieldError}>
          {(p) => <input {...p} className="input mono" value={domain} placeholder="panel.your-agency.com" onChange={(e) => setDomain(e.target.value)} />}
        </Field>
        {cert && (
          <table className="kv" style={{ marginBottom: 12 }}>
            <tbody>
              <tr><th scope="row">Address</th><td><a href={`https://${cert.domain}`}>https://{cert.domain}</a></td></tr>
              <tr>
                <th scope="row">Certificate</th>
                <td>
                  <Status tone={cert.state === "valid" ? "success" : cert.state === "expired" ? "danger" : "warning"}>
                    {cert.state === "valid" ? "Valid" : cert.state === "expired" ? "Expired" : "Waiting for the certificate"}
                  </Status>
                  {cert.method === "internal" && <span className="muted"> From the panel CA, because the domain is not public.</span>}
                </td>
              </tr>
              {cert.expiresAt && <tr><th scope="row">Expires</th><td>{longDate(cert.expiresAt)} <span className="muted">The panel renews it automatically.</span></td></tr>}
              {cert.checkedAt && <tr><th scope="row">Last check</th><td>{relativeTime(cert.checkedAt)}</td></tr>}
              {cert.state !== "valid" && cert.error && <tr><th scope="row">Problem</th><td>{cert.error}</td></tr>}
            </tbody>
          </table>
        )}
        <div className="btn-group">
          <button type="submit" className="btn btn-primary" disabled={save.busy || domain.trim().toLowerCase() === current}>{save.busy ? "Saving…" : "Save"}</button>
          {current && <button type="button" className="btn" disabled={save.busy} onClick={() => { setDomain(""); void save.run(null); }}>Remove domain</button>}
        </div>
      </form>
    </Card>
  );
}

function NameServersCard({ reseller }: { reseller: Customer }) {
  const status = reseller.brandStatus!;
  const current = reseller.branding?.nameServers ?? [];
  const [names, setNames] = useState<string[]>(status.panelNameServers.map((_, i) => current[i] ?? ""));
  const save = useAction((n: string[] | null) => saveResellerBrand(reseller.id, { nameServers: n }));
  const fieldErrors = Object.entries(save.error?.fields ?? {}).filter(([k]) => k.startsWith("branding.nameServers"));
  const proof = fieldErrors.find(([, m]) => m.startsWith("Prove"))?.[1];
  const active = current.length === status.panelNameServers.length;
  return (
    <Card title="Name servers">
      <form className="card-body" noValidate onSubmit={(e) => { e.preventDefault(); const n = names.map((x) => x.trim().toLowerCase()); void save.run(n.every((x) => x === "") ? null : n); }}>
        <p className="muted" style={{ marginBottom: 16 }}>
          Give your own host names to the name servers of the panel. The DNS zones of your customers then show your names, and your customers give your names to their registrar.
          Each name needs the same A and AAAA records as the name server of the panel. When a name is in its own domain, also add glue records at the registrar of that domain.
        </p>
        {current.length > 0 && !active && (
          <div className="alert alert-warning" role="status"><p>The panel has a different number of name servers now. Your customers see the names of the panel until you give one name for each name server.</p></div>
        )}
        <FormError error={save.error} shown={fieldErrors.map(([k]) => k)} />
        <ProofHint error={proof} />
        <div className="table-wrap">
          <table className="table">
            <thead><tr><th scope="col">Your host name</th><th scope="col">Name server of the panel</th><th scope="col">Records to add</th></tr></thead>
            <tbody>
              {status.panelNameServers.map((ns, i) => {
                const error = save.error?.fields[`branding.nameServers.${i}`];
                return (
                  <tr key={ns}>
                    <td>
                      <input className="input mono" aria-label={`Your host name for ${ns}`} aria-invalid={error && !error.startsWith("Prove") ? true : undefined} value={names[i]} placeholder={`ns${i + 1}.your-agency.com`}
                        onChange={(e) => setNames(names.map((n, j) => (j === i ? e.target.value : n)))} />
                      {error && !error.startsWith("Prove") && <p className="error">{error}</p>}
                    </td>
                    <td><code>{ns}</code></td>
                    <td>{(status.nameServerAddresses[ns] ?? []).map((a) => <div key={a}><code>{a.includes(":") ? "AAAA" : "A"} {a}</code></div>)}</td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
        <div className="btn-group" style={{ marginTop: 16 }}>
          <button type="submit" className="btn btn-primary" disabled={save.busy}>{save.busy ? "Saving…" : "Save"}</button>
          {current.length > 0 && <button type="button" className="btn" disabled={save.busy} onClick={() => { setNames(status.panelNameServers.map(() => "")); void save.run(null); }}>Use the names of the panel</button>}
        </div>
      </form>
    </Card>
  );
}

function MailHostCard({ reseller }: { reseller: Customer }) {
  const current = reseller.branding?.mailHost ?? "";
  const [host, setHost] = useState(current);
  const save = useAction((h: string | null) => saveResellerBrand(reseller.id, { mailHost: h }));
  const fieldError = save.error?.fields["branding.mailHost"];
  const proof = fieldError?.startsWith("Prove") ? fieldError : undefined;
  return (
    <Card title="Mail host">
      <form className="card-body" noValidate onSubmit={(e) => { e.preventDefault(); void save.run(host.trim().toLowerCase() || null); }}>
        <p className="muted" style={{ marginBottom: 16 }}>
          The host name that your customers put in their email apps, for example <code>mail.your-agency.com</code>. Point it to the mail server of the panel with a CNAME record.
        </p>
        <FormError error={save.error} shown={["branding.mailHost"]} />
        <ProofHint error={proof} />
        <Field label="Host name" error={proof ? undefined : fieldError}>
          {(p) => <input {...p} className="input mono" value={host} placeholder="mail.your-agency.com" onChange={(e) => setHost(e.target.value)} />}
        </Field>
        <div className="btn-group">
          <button type="submit" className="btn btn-primary" disabled={save.busy || host.trim().toLowerCase() === current}>{save.busy ? "Saving…" : "Save"}</button>
          {current && <button type="button" className="btn" disabled={save.busy} onClick={() => { setHost(""); void save.run(null); }}>Remove host name</button>}
        </div>
      </form>
    </Card>
  );
}
