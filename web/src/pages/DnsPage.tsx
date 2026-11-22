import { useEffect, useRef, useState } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { useAction, useStore } from "../api/hooks";
import { contextCustomers, inContext, visibleWebsites } from "../api/selectors";
import { checkDnsZone, createDnsZone, deleteDnsRecord, deleteDnsZone, exportDnsZone, importDnsZone, saveDnsRecord, setDnssec, showDnsZone, syncDnsZone, type DnsImportResult } from "../api/store";
import type { DnsRecord, DnsRecordType, DnsZone, State } from "../api/types";
import { Topbar } from "../components/Layout";
import { Card, Empty, Field, FormError, JobError, Modal, PageHeader, RowMenu, Status, TableWrap } from "../components/ui";
import { relativeTime } from "../lib/format";
import { DNS_DELEGATION, DNS_RECORD_TYPES, DNS_ZONE_STATUS, jobActive, ttlText } from "../lib/labels";
import { AccountsCard, delegationText, DNSSEC_ALGORITHMS, DS_DIGESTS, MoveDialog, ProviderCard, usableAccounts } from "./DnsProviders";
import { SecondariesCard, TransfersCard } from "./DnsTransfers";
import { CopyField } from "./website/AccessTab";

/**
 * DNS hosting. A customer makes a zone for a domain, and then gives the name servers of the
 * panel to the registrar of the domain. The panel sends each change to each DNS server. A zone
 * can also use an external DNS provider, for example Cloudflare (DnsProviders).
 */
export function DnsPage() {
  const { id } = useParams();
  return id ? <ZonePage id={id} /> : <ZoneList />;
}

/** The zone that has the domain: the zone with the longest name that the domain is in. */
function zoneOf(zones: DnsZone[], domain: string, customerId: string): DnsZone | undefined {
  return zones
    .filter((z) => z.customerId === customerId && z.status !== "removing" && (domain === z.name || domain.endsWith(`.${z.name}`)))
    .sort((a, b) => b.name.length - a.name.length)[0];
}

/** The step of the job that sends the zone to a DNS server, or the failed job of the zone. */
function ZoneJob({ state, zone }: { state: State; zone: DnsZone }) {
  const job = state.jobs.find((j) => j.dnsZoneId === zone.id && jobActive(j));
  const failed = zone.status === "failed" ? state.jobs.find((j) => j.dnsZoneId === zone.id && j.state === "failed") : undefined;
  return (
    <>
      {job && <span className="cell-sub">{job.steps[Math.min(job.step, job.steps.length - 1)]}…</span>}
      {failed && <JobError job={failed} />}
    </>
  );
}

function NoDnsServer({ admin }: { admin: boolean }) {
  return (
    <div className="alert alert-warning alert-stack" role="status" style={{ marginBottom: 20 }}>
      <p><strong>This panel has no DNS server.</strong>{" "}
        {admin
          ? <>To host DNS zones, give the role DNS and a name server host name to a server on the <Link to="/servers">Servers</Link> page. Use two servers, because most registrars want two name servers.</>
          : "You cannot make a DNS zone now. Speak to your provider."}
      </p>
    </div>
  );
}

function ZoneList() {
  const state = useStore();
  const navigate = useNavigate();
  const admin = state.session.user.role === "administrator";
  const customers = contextCustomers(state);
  const zones = state.dnsZones.filter((z) => inContext(state, z.customerId));
  const [creating, setCreating] = useState<string | null>(null);
  const [removing, setRemoving] = useState<DnsZone | null>(null);
  const check = useAction(checkDnsZone);
  const sync = useAction(syncDnsZone);
  const remove = useAction(deleteDnsZone);
  const customer = (id: string) => state.customers.find((c) => c.id === id);
  const showCustomer = customers.length > 1 || zones.some((z) => z.customerId !== customers[0]?.id);
  // With one customer, the card tells how many zones its plan allows.
  const only = customers.length === 1 ? customers[0] : undefined;
  const allowed = only && state.plans.find((p) => p.id === only.planId)?.dnsZones;
  const available = state.nameServers.length > 0;
  // A zone can go to the name servers of the panel, or to an external DNS provider.
  const canCreate = available || state.dnsAccounts.some((a) => a.available);
  // The domains of the websites, with the zone that has each one.
  const domains = visibleWebsites(state).flatMap((w) => w.domains.map((d) => ({ website: w, domain: d, zone: zoneOf(state.dnsZones, d.name, w.customerId) })));

  return (
    <>
      <Topbar />
      <main id="main" className="content content-wide">
        <PageHeader
          title="DNS"
          description="The DNS records of your domains. Make a zone for a domain, and then give the name servers of this panel, or of your external DNS provider, to the registrar of the domain. The panel checks each record before it saves it."
          actions={canCreate && <button type="button" className="btn btn-primary" onClick={() => setCreating("")}>New DNS zone</button>}
        />
        {!canCreate && <NoDnsServer admin={admin} />}
        <div className="stack">
          <Card title="DNS zones">
            <div className="card-body" style={{ paddingBottom: 12 }}>
              <p className="muted">
                A zone has all the records of one domain. The domain uses the zone only when its registrar has the name servers of the zone.
                {only && allowed !== undefined && <> {only.name} has {zones.filter((z) => z.customerId === only.id).length} of {allowed} DNS zones.</>}
              </p>
              <FormError error={check.error ?? sync.error ?? remove.error} />
            </div>
            <TableWrap>
              <table>
                <thead>
                  <tr>
                    <th scope="col">Zone</th>
                    <th scope="col">Records</th>
                    <th scope="col">DNS provider</th>
                    <th scope="col">Registrar</th>
                    <th scope="col">DNSSEC</th>
                    <th scope="col">Status</th>
                    <th scope="col"><span className="visually-hidden">Actions</span></th>
                  </tr>
                </thead>
                <tbody>
                  {zones.map((z) => {
                    const status = DNS_ZONE_STATUS[z.status];
                    const delegation = DNS_DELEGATION[z.delegation.state];
                    return (
                      <tr key={z.id}>
                        <td>
                          <Link className="cell-title" to={`/dns/${z.id}`}>{z.name}</Link>
                          {showCustomer && <span className="cell-sub">{customer(z.customerId)?.name ?? "Removed customer"}</span>}
                        </td>
                        <td>
                          {z.recordCount}
                          {z.websites > 0 && <span className="cell-sub nowrap">{z.websites} {z.websites === 1 ? "website" : "websites"}</span>}
                        </td>
                        <td>
                          {z.provider ? z.provider.name : "This panel"}
                          {z.provider && <span className="cell-sub">{z.provider.handover ? "Handover from this panel" : z.provider.accountName}</span>}
                        </td>
                        <td>
                          <Status tone={delegation.tone}>{delegationText(z, delegation.label)}</Status>
                          {z.delegation.state === "elsewhere" && z.delegation.nameServers.length > 0 && <span className="cell-sub">{z.delegation.nameServers.join(", ")}</span>}
                        </td>
                        <td>{z.dnssec.enabled ? <Status tone="success">On</Status> : <Status tone="neutral">Off</Status>}</td>
                        <td style={{ minWidth: 120 }}>
                          <Status tone={status.tone}>{status.label}</Status>
                          <ZoneJob state={state} zone={z} />
                        </td>
                        <td className="cell-actions">
                          {z.status !== "removing" && (
                            <RowMenu label={`Actions for the zone ${z.name}`} actions={[
                              { label: "Open", onSelect: () => navigate(`/dns/${z.id}`) },
                              { label: "Check the name servers", onSelect: () => void check.run(z.id) },
                              ...(z.status === "failed" ? [{ label: "Send again now", onSelect: () => void sync.run(z.id) }] : []),
                              { label: "Remove", danger: true, onSelect: () => setRemoving(z) },
                            ]} />
                          )}
                        </td>
                      </tr>
                    );
                  })}
                  {zones.length === 0 && <tr><td colSpan={7} className="muted">No DNS zones.{canCreate && " Make a zone for a domain that you own."}</td></tr>}
                </tbody>
              </table>
            </TableWrap>
          </Card>

          <AccountsCard />

          <SecondariesCard />

          {available && (
            <Card title="Name servers of this panel">
              <div className="card-body">
                <p className="muted" style={{ marginBottom: 12 }}>Give these name servers to the registrar of each domain that has a zone here. A change of the name servers can take up to 48 hours.</p>
                <div style={{ display: "grid", gap: 8 }}>
                  {state.nameServers.map((n, i) => <CopyField key={n} value={n} label={`Name server ${i + 1}`} />)}
                </div>
              </div>
            </Card>
          )}

          {domains.length > 0 && (
            <Card title="Domains of websites">
              <div className="card-body" style={{ paddingBottom: 12 }}>
                <p className="muted">When a domain of a website is in a zone, the panel makes the A and AAAA records of the domain. The records point to the server of the website.</p>
              </div>
              <TableWrap>
                <table>
                  <thead><tr><th scope="col">Domain</th><th scope="col">Website</th><th scope="col">DNS zone</th><th scope="col">Points to the website</th></tr></thead>
                  <tbody>
                    {domains.map(({ website, domain, zone }) => (
                      <tr key={`${website.id}-${domain.name}`}>
                        <td>{domain.name}</td>
                        <td><Link to={`/websites/${website.id}/domains`}>{website.name}</Link></td>
                        <td>
                          {zone
                            ? <Link to={`/dns/${zone.id}`}>{zone.name}</Link>
                            : canCreate
                              ? <button type="button" className="btn-link" onClick={() => setCreating(domain.name.replace(/^www\./, ""))}>Make a zone</button>
                              : <span className="muted">None</span>}
                        </td>
                        <td>{domain.dnsPointsHere ? <Status tone="success">Yes</Status> : <Status tone="warning">No</Status>}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </TableWrap>
            </Card>
          )}
        </div>
      </main>
      {creating !== null && <CreateDialog name={creating} onClose={() => setCreating(null)} />}
      {removing && <RemoveDialog zone={removing} onClose={() => setRemoving(null)} onRemove={(keep) => void remove.run(removing.id, keep)} />}
    </>
  );
}

function CreateDialog({ name: first, onClose }: { name: string; onClose: () => void }) {
  const state = useStore();
  const navigate = useNavigate();
  const customers = contextCustomers(state);
  const [customerId, setCustomerId] = useState(() => customers.find((c) => c.id === state.session.contextId)?.id ?? customers[0]?.id ?? "");
  const [name, setName] = useState(first);
  const panel = state.nameServers.length > 0;
  const accounts = usableAccounts(state, customerId);
  // "": the name servers of the panel.
  const [accountId, setAccountId] = useState(() => panel ? "" : accounts[0]?.id ?? "");
  const [importRecords, setImportRecords] = useState(true);
  const account = accounts.find((a) => a.id === accountId);
  const create = useAction(createDnsZone);
  const customer = customers.find((c) => c.id === customerId);
  const own = state.dnsZones.filter((z) => z.customerId === customerId).length;
  const allowed = state.plans.find((p) => p.id === customer?.planId)?.dnsZones;
  const full = allowed !== undefined && own >= allowed;

  return (
    <Modal
      title="New DNS zone"
      onClose={onClose}
      footer={<><button type="button" className="btn" onClick={onClose}>Cancel</button><button type="submit" form="new-zone" className="btn btn-primary" disabled={create.busy}>Create zone</button></>}
    >
      <form id="new-zone" noValidate onSubmit={async (e) => {
        e.preventDefault();
        const r = await create.run(name, customerId, account?.id, importRecords);
        if (r.ok) {
          onClose();
          navigate(`/dns/${r.value}`);
        }
      }}>
        <FormError error={create.error} shown={["name", "customerId", "accountId"]} />
        {customers.length > 1 && (
          <Field label="Customer" error={create.error?.fields.customerId}>
            {(p) => (
              <select {...p} className="select" value={customerId} onChange={(e) => {
                setCustomerId(e.target.value);
                // An account of a different customer is not for this customer.
                if (!usableAccounts(state, e.target.value).some((a) => a.id === accountId)) setAccountId(panel ? "" : usableAccounts(state, e.target.value)[0]?.id ?? "");
              }}>
                {customers.map((c) => <option key={c.id} value={c.id}>{c.name}</option>)}
              </select>
            )}
          </Field>
        )}
        {customer && allowed !== undefined && (
          <p className={full ? "text-danger small" : "muted small"} style={{ margin: "8px 0 14px" }}>
            {customer.name} has {own} of {allowed} DNS zones.{full && " The plan of the customer allows no more."}
          </p>
        )}
        <Field label="Domain" hint="The domain that you registered, for example example.com. The zone also has all the names below the domain." error={create.error?.fields.name}>
          {(p) => <input {...p} className="input" value={name} onChange={(e) => setName(e.target.value)} placeholder="example.com" autoCapitalize="none" spellCheck={false} />}
        </Field>
        {accounts.length > 0 && (
          <Field label="Name servers" hint={account ? `The zone goes to ${account.providerName}. The panel keeps the records, and sends each change to ${account.providerName}.` : undefined} error={create.error?.fields.accountId}>
            {(p) => (
              <select {...p} className="select" value={accountId} onChange={(e) => setAccountId(e.target.value)}>
                {panel && <option value="">This panel ({state.nameServers.join(", ")})</option>}
                {accounts.map((a) => <option key={a.id} value={a.id}>{a.providerName}: {a.name}</option>)}
              </select>
            )}
          </Field>
        )}
        {account && (
          <label className="check" style={{ marginTop: 10 }}><input type="checkbox" checked={importRecords} onChange={(e) => setImportRecords(e.target.checked)} />Copy the records that {account.providerName} has for the domain now</label>
        )}
        <p className="muted small" style={{ marginTop: 14 }}>
          {account
            ? `The panel makes the address records of the websites of the customer that have a domain in the zone. ${account.providerName} keeps the name server records. The domain continues to use its current DNS provider until you change the name servers at the registrar.`
            : "The panel makes the name server records of the zone, and the address records of the websites of the customer that have a domain in the zone. The domain continues to use its current DNS provider until you change the name servers at the registrar."}
        </p>
      </form>
    </Modal>
  );
}

function RemoveDialog({ zone, onClose, onRemove }: { zone: DnsZone; onClose: () => void; onRemove: (keepAtProvider: boolean) => void }) {
  const [keep, setKeep] = useState(false);
  const provider = zone.provider?.name;
  return (
    <Modal
      title={`Remove DNS zone ${zone.name}?`}
      onClose={onClose}
      footer={<><button type="button" className="btn" onClick={onClose}>Cancel</button><button type="button" className="btn btn-danger-solid" onClick={() => { onClose(); onRemove(keep); }}>Remove zone</button></>}
    >
      {provider ? (
        <>
          <p>{keep ? `The panel forgets the zone and its ${zone.recordCount} records. The zone stays at ${provider} with its records, and the panel does not change it again.` : `The panel removes the zone and its ${zone.recordCount} records from ${provider}. You cannot restore the records.`}</p>
          <label className="check" style={{ marginTop: 10 }}><input type="checkbox" checked={keep} onChange={(e) => setKeep(e.target.checked)} />Keep the zone at {provider}</label>
        </>
      ) : (
        <p>The panel removes the zone and its {zone.recordCount} records from each DNS server. You cannot restore the records.</p>
      )}
      {zone.delegation.state === "here" && !keep && (
        <p style={{ marginTop: 10 }}><strong>The domain uses the name servers of {provider ?? "this panel"}.</strong> When you remove the zone, the websites and the email of the domain stop until the domain has a different DNS provider. First set the new name servers at the registrar.</p>
      )}
      {zone.dnssec.enabled && (
        <p style={{ marginTop: 10 }}><strong>The zone has DNSSEC.</strong> First remove the DS record at the registrar. If the DS record stays, resolvers refuse the answers of the new DNS provider.</p>
      )}
    </Modal>
  );
}

function ZonePage({ id }: { id: string }) {
  const state = useStore();
  const navigate = useNavigate();
  const zone = state.dnsZones.find((z) => z.id === id && inContext(state, z.customerId));
  const exists = !!zone;
  // The list of zones has no records. The page reads the records of this zone.
  useEffect(() => { if (exists) void showDnsZone(id); }, [id, exists]);
  const [editing, setEditing] = useState<DnsRecord | "new" | null>(null);
  const [removingRecord, setRemovingRecord] = useState<DnsRecord | null>(null);
  const [removing, setRemoving] = useState(false);
  const [dnssecDialog, setDnssecDialog] = useState(false);
  const [importing, setImporting] = useState(false);
  const [moving, setMoving] = useState(false);
  const [type, setType] = useState<"all" | DnsRecordType>("all");
  const exportZone = useAction(async (zoneId: string, name: string) => saveText(`${name}.zone`, await exportDnsZone(zoneId)));
  const check = useAction(checkDnsZone);
  const sync = useAction(syncDnsZone);
  const remove = useAction(deleteDnsZone);
  const removeRecord = useAction(deleteDnsRecord);
  const dnssec = useAction(setDnssec);

  if (!zone) {
    return (
      <>
        <Topbar />
        <main id="main" className="content"><Empty title="This DNS zone does not exist"><p><Link to="/dns">Go to all DNS zones</Link></p></Empty></main>
      </>
    );
  }
  const status = DNS_ZONE_STATUS[zone.status];
  const delegation = DNS_DELEGATION[zone.delegation.state];
  const customer = state.customers.find((c) => c.id === zone.customerId);
  const all = zone.records ?? [];
  const records = all.filter((r) => type === "all" || r.type === type);
  const locked = zone.status === "removing";
  const failed = zone.status === "failed" ? state.jobs.find((j) => j.dnsZoneId === zone.id && j.state === "failed") : undefined;
  const full = (name: string) => (name === "@" ? zone.name : `${name}.${zone.name}`);
  const provider = zone.provider;
  // The account of the zone, when the user can see it. It tells if the provider can sign zones.
  const account = provider ? state.dnsAccounts.find((a) => a.id === provider.accountId) : undefined;
  const canSign = !provider || (account?.dnssec ?? true);
  const signer = provider?.name ?? "The DNS servers";
  // The registrar has the name servers of the provider, and the panel waits for its TXT record in the public DNS.
  const waitsForProvider = !!provider && zone.delegation.state === "unknown" && zone.delegation.nameServers.length > 0;

  return (
    <>
      <Topbar />
      <main id="main" className="content content-wide">
        <ol className="breadcrumbs"><li><Link to="/dns">DNS</Link></li><li aria-current="page">{zone.name}</li></ol>
        <div className="detail-header">
          <div>
            <div className="detail-title"><h1>{zone.name}</h1><Status tone={status.tone}>{status.label}</Status></div>
            <p className="detail-meta">{[customer?.name, provider ? `at ${provider.name}` : undefined, `${zone.recordCount} records`, `serial ${zone.serial}`].filter(Boolean).join(" · ")}</p>
          </div>
          {!locked && (
            <div className="btn-group">
              <button type="button" className="btn btn-danger" onClick={() => setRemoving(true)}>Remove zone</button>
              <button type="button" className="btn" disabled={exportZone.busy} onClick={() => void exportZone.run(zone.id, zone.name)}>Export</button>
              <button type="button" className="btn" onClick={() => setImporting(true)}>Import</button>
              {(state.dnsAccounts.length > 0 || provider) && <button type="button" className="btn" onClick={() => setMoving(true)}>Move</button>}
              <button type="button" className="btn btn-primary" onClick={() => setEditing("new")}>Add record</button>
            </div>
          )}
        </div>
        <FormError error={check.error ?? sync.error ?? remove.error ?? removeRecord.error ?? dnssec.error ?? exportZone.error} />

        {zone.status === "failed" && provider?.state === "failed" && (
          <div className="alert alert-danger alert-stack mt" role="alert">
            <p><strong>{provider.name} does not have the last change.</strong> {provider.name} continues to answer with the records that it had before. The panel sends the zone again by itself: first after one minute, and then after a longer time for each try. You can also send it now.</p>
            {provider.error && <div className="cause"><p>{provider.error}</p></div>}
            <p><button type="button" className="btn" disabled={sync.busy} onClick={() => void sync.run(zone.id)}>{sync.busy ? "Sending…" : "Send again now"}</button></p>
          </div>
        )}
        {zone.status === "failed" && provider?.state !== "failed" && (
          <div className="alert alert-danger alert-stack mt" role="alert">
            <p><strong>A DNS server does not have the last change.</strong> The server continues to answer with the records that it had before. The panel sends the zone again by itself: first after one minute, and then after a longer time for each try. You can also send it now.</p>
            {failed && <div className="cause"><JobError job={failed} /></div>}
            <p><button type="button" className="btn" disabled={sync.busy} onClick={() => void sync.run(zone.id)}>{sync.busy ? "Sending…" : "Send again now"}</button></p>
          </div>
        )}
        {!locked && zone.delegation.state !== "here" && (
          <div className="alert alert-info alert-stack mt" role="status">
            <p>
              <strong>{zone.delegation.state === "elsewhere" ? "The domain uses other name servers." : zone.delegation.state === "none" ? "The public DNS has no name servers for the domain." : waitsForProvider ? `The registrar has the name servers of ${provider?.name}, but ${provider?.name} does not answer with this zone yet.` : "The panel has not checked the name servers of the domain."}</strong>{" "}
              {waitsForProvider && <>The panel checks the TXT record <code>_cloudfactory-challenge.{zone.name}</code> that it sends to {provider?.name} with the zone. A different account at {provider?.name} can have the same name servers, so only this record shows that the domain uses this zone. </>}
              {zone.delegation.state === "elsewhere" && zone.delegation.nameServers.length > 0 && <>The registrar now has <code>{zone.delegation.nameServers.join(", ")}</code>. </>}
              {waitsForProvider ? "" : zone.nameServers.length > 0
                ? <>The records below have no effect until the registrar of {zone.name} has the name servers of {provider?.name ?? "this panel"}: <code>{zone.nameServers.join(", ")}</code>. </>
                : <>The records below have no effect until the registrar of {zone.name} has the name servers of {provider?.name ?? "this panel"}. {provider?.name} gives them after the first change. </>}
              {provider ? "" : "Copy the records that the domain uses now to this zone first."}
            </p>
            <p><button type="button" className="btn" disabled={check.busy} onClick={() => void check.run(zone.id)}>{check.busy ? "Checking…" : "Check again"}</button></p>
          </div>
        )}

        <div className="grid-2 mt">
          <Card title="Name servers" actions={!locked && <button type="button" className="btn btn-sm" disabled={check.busy} onClick={() => void check.run(zone.id)}>Check</button>}>
            <div className="card-body">
              <ProviderCard zone={zone} syncing={sync.busy} onSync={() => void sync.run(zone.id)} />
              <table className="kv">
                <tbody>
                  <tr>
                    <th scope="row">Registrar</th>
                    <td>
                      <Status tone={delegation.tone}>{delegationText(zone, delegation.label)}</Status>
                      {zone.delegation.checkedAt && <span className="cell-sub">Checked {relativeTime(zone.delegation.checkedAt)}</span>}
                    </td>
                  </tr>
                  {zone.servers.map((s) => (
                    <tr key={s.nameServer}>
                      <th scope="row">Server</th>
                      <td>
                        <code>{s.nameServer}</code>
                        <span className="cell-sub">
                          {s.state === "ok" ? "Has the last change" : s.state === "pending" ? "Waits for the last change" : s.state === "failed" ? "Could not load the last change" : "Removed"}
                        </span>
                      </td>
                    </tr>
                  ))}
                  {zone.servers.length === 0 && !provider && <tr><th scope="row">Server</th><td className="muted">The panel has no DNS server.</td></tr>}
                </tbody>
              </table>
            </div>
          </Card>

          <Card
            title="DNSSEC"
            actions={!locked && (canSign || zone.dnssec.enabled) && <button type="button" className="btn btn-sm" disabled={dnssec.busy} onClick={() => setDnssecDialog(true)}>{zone.dnssec.enabled ? "Turn off" : "Turn on"}</button>}
          >
            <div className="card-body">
              {zone.dnssec.enabled && !zone.dnssec.ds ? (
                <>
                  <p><Status tone="info">Waits</Status> <span className="muted">{signer} makes the key of the zone.</span></p>
                  <p className="muted" style={{ marginTop: 10 }}>The DS record for the registrar comes when the key is ready. This can take some minutes.</p>
                </>
              ) : zone.dnssec.enabled && zone.dnssec.ds ? (
                <>
                  <p><Status tone="success">On</Status> <span className="muted">{signer} {provider ? "signs" : "sign"} each answer of the zone.</span></p>
                  <p className="muted" style={{ margin: "10px 0" }}>Give this DS record to the registrar of the domain. Resolvers check the signatures only when the registrar has the record.</p>
                  <CopyField value={zone.dnssec.ds.record} label="DS record" />
                  <table className="kv" style={{ marginTop: 8 }}>
                    <tbody>
                      <tr><th scope="row">Key tag</th><td><code>{zone.dnssec.ds.keyTag}</code></td></tr>
                      <tr><th scope="row">Algorithm</th><td><code>{zone.dnssec.ds.algorithm}</code> <span className="muted">{DNSSEC_ALGORITHMS[zone.dnssec.ds.algorithm] ?? ""}</span></td></tr>
                      <tr><th scope="row">Digest type</th><td><code>{zone.dnssec.ds.digestType}</code> <span className="muted">{DS_DIGESTS[zone.dnssec.ds.digestType] ?? ""}</span></td></tr>
                      <tr><th scope="row">Digest</th><td><code>{zone.dnssec.ds.digest}</code></td></tr>
                    </tbody>
                  </table>
                </>
              ) : (
                <>
                  <p><Status tone="neutral">Off</Status></p>
                  <p className="muted" style={{ marginTop: 10 }}>
                    {!canSign
                      ? `The plugin of ${provider?.name} cannot turn on DNSSEC.`
                      : provider
                        ? `With DNSSEC, ${provider.name} signs each answer of the zone, and a resolver can check that nobody changed the answer. ${provider.name} makes the key, and the panel shows the DS record for the registrar.`
                        : "With DNSSEC, the DNS servers sign each answer of the zone, and a resolver can check that nobody changed the answer. The panel makes one key for the zone and gives you a DS record for the registrar."}
                  </p>
                </>
              )}
            </div>
          </Card>
        </div>

        <TransfersCard zone={zone} locked={locked} />

        <Card
          className="mt"
          title="Records"
          actions={
            <div className="filters" role="group" aria-label="Filter by type">
              {(["all", ...DNS_RECORD_TYPES] as const).filter((t) => t === "all" || all.some((r) => r.type === t)).map((t) => (
                <button key={t} type="button" className="chip" aria-pressed={type === t} onClick={() => setType(t)}>{t === "all" ? "All" : t}</button>
              ))}
            </div>
          }
        >
          <TableWrap>
            <table>
              <thead><tr><th scope="col">Name</th><th scope="col">Type</th><th scope="col">Value</th><th scope="col">TTL</th><th scope="col">Made by</th><th scope="col"><span className="visually-hidden">Actions</span></th></tr></thead>
              <tbody>
                {records.map((r) => (
                  <tr key={r.id}>
                    <td><code>{r.name}</code>{r.name !== "@" && <span className="cell-sub">{full(r.name)}</span>}</td>
                    <td><span className="tag">{r.type}</span></td>
                    <td style={{ maxWidth: 440, overflowWrap: "anywhere" }}>
                      <code>{r.value}</code>
                      {r.priority !== undefined && <span className="cell-sub">Priority {r.priority}</span>}
                    </td>
                    <td className="nowrap">{ttlText(r.ttl)}</td>
                    <td className="muted nowrap">{r.managed === "ns" ? (provider ? `${provider.name}: name server` : "Panel: name server") : r.managed === "website" ? "Panel: website" : "You"}</td>
                    <td className="cell-actions">
                      {!locked && r.managed !== "ns" && (
                        <RowMenu label={`Actions for the ${r.type} record ${r.name}`} actions={[
                          { label: "Change", onSelect: () => setEditing(r) },
                          { label: "Remove", danger: true, onSelect: () => setRemovingRecord(r) },
                        ]} />
                      )}
                    </td>
                  </tr>
                ))}
                {zone.records === undefined && <tr><td colSpan={6} className="muted" role="status">Loading the records…</td></tr>}
                {zone.records !== undefined && records.length === 0 && <tr><td colSpan={6} className="muted">No records.</td></tr>}
              </tbody>
            </table>
          </TableWrap>
          <p className="card-body muted small" style={{ paddingTop: 12 }}>
            <code>@</code> is the domain itself. {provider ? `The name server records of the zone come from ${provider.name}, and you cannot change them.` : "The panel keeps the name server records of the zone, and you cannot change them."} When you change a record that the panel made for a website, the record becomes yours and the panel does not change it again.
          </p>
        </Card>
      </main>

      {editing && <RecordDialog zone={zone} record={editing === "new" ? undefined : editing} onClose={() => setEditing(null)} />}
      {importing && <ImportDialog zone={zone} onClose={() => setImporting(false)} />}
      {moving && <MoveDialog zone={zone} onClose={() => setMoving(false)} />}
      {removingRecord && (
        <Modal
          title={`Remove the ${removingRecord.type} record ${removingRecord.name}?`}
          onClose={() => setRemovingRecord(null)}
          footer={<><button type="button" className="btn" onClick={() => setRemovingRecord(null)}>Cancel</button><button type="button" className="btn btn-danger-solid" onClick={() => { const r = removingRecord; setRemovingRecord(null); void removeRecord.run(zone.id, r.id); }}>Remove record</button></>}
        >
          <p>The panel removes the record <code>{full(removingRecord.name)}</code> from each DNS server.</p>
          {removingRecord.managed === "website" && <p style={{ marginTop: 10 }}>The panel made this record for a website. With no record, the domain does not point to the website.</p>}
        </Modal>
      )}
      {removing && <RemoveDialog zone={zone} onClose={() => setRemoving(false)} onRemove={(keep) => void remove.run(zone.id, keep).then((r) => { if (r.ok) navigate("/dns"); })} />}
      {dnssecDialog && (
        <Modal
          title={zone.dnssec.enabled ? `Turn off DNSSEC for ${zone.name}?` : `Turn on DNSSEC for ${zone.name}?`}
          onClose={() => setDnssecDialog(false)}
          footer={<><button type="button" className="btn" onClick={() => setDnssecDialog(false)}>Cancel</button><button type="button" className={zone.dnssec.enabled ? "btn btn-danger-solid" : "btn btn-primary"} onClick={() => { setDnssecDialog(false); void dnssec.run(zone.id, !zone.dnssec.enabled); }}>{zone.dnssec.enabled ? "Turn off DNSSEC" : "Turn on DNSSEC"}</button></>}
        >
          {zone.dnssec.enabled ? (
            <>
              <p><strong>First remove the DS record at the registrar, and wait one day.</strong> If the registrar still has the DS record, resolvers refuse all the answers of the zone, and the websites and the email of the domain stop.</p>
              <p style={{ marginTop: 10 }}>{provider ? `${provider.name} deletes` : "The panel deletes"} the key of the zone. If you turn on DNSSEC again, the zone gets a new key and a new DS record.</p>
            </>
          ) : (
            <>
              <p>{provider ? `${provider.name} makes a key for the zone, and signs the zone. Then the panel shows the DS record of ${provider.name}.` : "The panel makes a key for the zone, and the DNS servers sign the zone. Then the panel shows a DS record."}</p>
              <p style={{ marginTop: 10 }}>DNSSEC has an effect only after you give the DS record to the registrar of the domain. Until then, the zone operates as before.</p>
            </>
          )}
        </Modal>
      )}
    </>
  );
}

/** Gives the text to the browser as a file to save. */
function saveText(name: string, text: string) {
  const url = URL.createObjectURL(new Blob([text], { type: "text/plain" }));
  const a = document.createElement("a");
  a.href = url;
  a.download = name;
  a.click();
  URL.revokeObjectURL(url);
}

/** The largest zone file that the panel reads (ZoneFile::MAX_BYTES). */
const MAX_ZONE_FILE = 1024 * 1024;

/**
 * Imports the records of a zone file, for example the export of a different DNS provider. The
 * panel first shows a preview: the records that it adds, and the lines that it does not import.
 */
function ImportDialog({ zone, onClose }: { zone: DnsZone; onClose: () => void }) {
  const [text, setText] = useState("");
  const [preview, setPreview] = useState<DnsImportResult | null>(null);
  const [fileError, setFileError] = useState<string | null>(null);
  const run = useAction(importDnsZone);
  const picker = useRef<HTMLInputElement>(null);
  const plural = (n: number, one: string, many: string) => `${n} ${n === 1 ? one : many}`;

  return (
    <Modal
      wide
      title={`Import records to ${zone.name}`}
      onClose={onClose}
      footer={preview ? (
        <>
          <button type="button" className="btn" onClick={() => { setPreview(null); run.clear(); }}>Back</button>
          <button type="button" className="btn btn-primary" disabled={run.busy || preview.added.length === 0} onClick={async () => {
            const r = await run.run(zone.id, text, false);
            if (r.ok) onClose();
          }}>{preview.added.length === 0 ? "No records to import" : `Import ${plural(preview.added.length, "record", "records")}`}</button>
        </>
      ) : (
        <>
          <button type="button" className="btn" onClick={onClose}>Cancel</button>
          <button type="submit" form="dns-import" className="btn btn-primary" disabled={run.busy || text.trim() === ""}>{run.busy ? "Reading…" : "Preview"}</button>
        </>
      )}
    >
      <FormError error={run.error} shown={["zoneFile"]} />
      {!preview ? (
        <form id="dns-import" noValidate onSubmit={async (e) => {
          e.preventDefault();
          const r = await run.run(zone.id, text, true);
          if (r.ok) setPreview(r.value);
        }}>
          <p className="muted" style={{ marginBottom: 12 }}>
            Paste a zone file, for example the export of your current DNS provider or of cPanel. The panel checks each record. It adds only the records that the zone does not have, and it does not change or remove a record.
          </p>
          <Field label="Zone file" error={run.error?.fields.zoneFile ?? fileError ?? undefined}>
            {(p) => <textarea {...p} className="input mono" rows={12} value={text} onChange={(e) => setText(e.target.value)} placeholder={`$ORIGIN ${zone.name}.\nwww   3600  IN  A    203.0.113.10\n@     3600  IN  MX   10 mail.${zone.name}.`} spellCheck={false} />}
          </Field>
          <p style={{ marginTop: 10 }}>
            <button type="button" className="btn btn-sm" onClick={() => picker.current?.click()}>Open a file</button>
            <input ref={picker} type="file" hidden accept=".zone,.db,.txt,text/plain,text/dns" aria-label="Open a zone file" onChange={async (e) => {
              const file = e.target.files?.[0];
              e.target.value = "";
              if (!file) return;
              if (file.size > MAX_ZONE_FILE) {
                setFileError("The file is larger than 1 MB.");
                return;
              }
              setFileError(null);
              setText(await file.text());
            }} />
          </p>
        </form>
      ) : (
        <>
          <p role="status">
            {preview.added.length > 0 ? <strong>{plural(preview.added.length, "record", "records")} to add.</strong> : <strong>No records to add.</strong>}{" "}
            {preview.existing > 0 && <>{plural(preview.existing, "record is", "records are")} in the zone already. </>}
            {preview.skipped.length > 0 && <>The panel does not import {plural(preview.skipped.length, "line", "lines")}.</>}
          </p>
          {preview.added.length > 0 && (
            <TableWrap>
              <table>
                <caption className="visually-hidden">Records to add</caption>
                <thead><tr><th scope="col">Name</th><th scope="col">Type</th><th scope="col">Value</th><th scope="col">TTL</th></tr></thead>
                <tbody>
                  {preview.added.map((r) => (
                    <tr key={`${r.line}-${r.type}-${r.value}`}>
                      <td><code>{r.name}</code></td>
                      <td><span className="tag">{r.type}</span></td>
                      <td style={{ maxWidth: 360, overflowWrap: "anywhere" }}>
                        <code>{r.value}</code>
                        {r.priority !== undefined && <span className="cell-sub">Priority {r.priority}</span>}
                        {r.note && <span className="cell-sub">{r.note}</span>}
                      </td>
                      <td className="nowrap">{ttlText(r.ttl)}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </TableWrap>
          )}
          {preview.skipped.length > 0 && (
            <>
              <h3 className="small" style={{ margin: "16px 0 8px" }}>Lines that the panel does not import</h3>
              <TableWrap>
                <table>
                  <thead><tr><th scope="col">Line</th><th scope="col">Text and reason</th></tr></thead>
                  <tbody>
                    {preview.skipped.map((s) => (
                      <tr key={`${s.line}-${s.code}`}>
                        <td className="nowrap">{s.line}</td>
                        <td style={{ maxWidth: 480, overflowWrap: "anywhere" }}><code>{s.text}</code><span className="cell-sub">{s.reason}</span></td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </TableWrap>
            </>
          )}
        </>
      )}
    </Modal>
  );
}

const VALUE_HELP: Record<DnsRecordType, { label: string; placeholder: string; hint: string }> = {
  A: { label: "IPv4 address", placeholder: "203.0.113.10", hint: "The IPv4 address that the name points to." },
  AAAA: { label: "IPv6 address", placeholder: "2001:db8::10", hint: "The IPv6 address that the name points to." },
  CNAME: { label: "Target", placeholder: "shop.example.net", hint: "The host name that the name is an alias of. A name with a CNAME record has no other records." },
  MX: { label: "Mail server", placeholder: "mx1.example.net", hint: "The host name of the server that receives the email of the name." },
  TXT: { label: "Text", placeholder: "v=spf1 include:example.net -all", hint: "The text, with no quotation marks. The panel divides a long text into parts." },
  SRV: { label: "Weight, port and target", placeholder: "5 5060 sip.example.net", hint: "The weight, the port and the host name of the service, with a space between them." },
  CAA: { label: "Flags, tag and value", placeholder: "0 issue \"letsencrypt.org\"", hint: "The certificate authority that can make certificates for the name." },
  NS: { label: "Name server", placeholder: "ns1.example.net", hint: "The name server of a sub-domain that a different DNS provider has." },
};

const TTLS = [300, 900, 3600, 14400, 86400];

function RecordDialog({ zone, record, onClose }: { zone: DnsZone; record?: DnsRecord; onClose: () => void }) {
  const [type, setType] = useState<DnsRecordType>(record?.type ?? "A");
  const [name, setName] = useState(record?.name ?? "");
  const [value, setValue] = useState(record?.value ?? "");
  const [ttl, setTtl] = useState(record?.ttl ?? 3600);
  const [priority, setPriority] = useState(String(record?.priority ?? 10));
  const save = useAction(saveDnsRecord);
  const help = VALUE_HELP[type];
  const withPriority = type === "MX" || type === "SRV";
  const ttls = TTLS.includes(ttl) ? TTLS : [...TTLS, ttl].sort((a, b) => a - b);
  const shownName = name.trim() === "" || name.trim() === "@" ? zone.name : `${name.trim().replace(/\.$/, "")}.${zone.name}`;

  return (
    <Modal
      wide
      title={record ? `Change the ${record.type} record ${record.name}` : `Add a record to ${zone.name}`}
      onClose={onClose}
      footer={<><button type="button" className="btn" onClick={onClose}>Cancel</button><button type="submit" form="dns-record" className="btn btn-primary" disabled={save.busy}>{record ? "Save record" : "Add record"}</button></>}
    >
      <form id="dns-record" noValidate onSubmit={async (e) => {
        e.preventDefault();
        const r = await save.run(zone.id, { type, name, value, ttl, priority: withPriority ? Math.round(+priority) : undefined }, record?.id);
        if (r.ok) onClose();
      }}>
        <FormError error={save.error} shown={["type", "name", "value", "ttl", "priority"]} />
        {record?.managed === "website" && (
          <p className="muted" style={{ marginBottom: 12 }}>The panel made this record for a website. After your change, the record is yours, and the panel does not change it when the website moves to a different server.</p>
        )}
        <div className="form-row">
          {!record && (
            <Field label="Type" error={save.error?.fields.type}>
              {(p) => (
                <select {...p} className="select" value={type} onChange={(e) => setType(e.target.value as DnsRecordType)}>
                  {DNS_RECORD_TYPES.map((t) => <option key={t} value={t}>{t}</option>)}
                </select>
              )}
            </Field>
          )}
          <Field label="Name" hint={<>Empty or <code>@</code> for the domain itself. The full name is <code>{shownName}</code>.</>} error={save.error?.fields.name}>
            {(p) => <input {...p} className="input mono" value={name} onChange={(e) => setName(e.target.value)} placeholder="@" autoCapitalize="none" spellCheck={false} />}
          </Field>
        </div>
        <div style={{ marginTop: 14 }}>
          <Field label={help.label} hint={help.hint} error={save.error?.fields.value}>
            {(p) => type === "TXT"
              ? <textarea {...p} className="input mono" rows={3} value={value} onChange={(e) => setValue(e.target.value)} placeholder={help.placeholder} spellCheck={false} />
              : <input {...p} className="input mono" value={value} onChange={(e) => setValue(e.target.value)} placeholder={help.placeholder} autoCapitalize="none" spellCheck={false} />}
          </Field>
        </div>
        <div className="form-row" style={{ marginTop: 14 }}>
          {withPriority && (
            <Field label="Priority" hint="A lower number comes first." error={save.error?.fields.priority}>
              {(p) => <input {...p} className="input" type="number" min={0} max={65535} value={priority} onChange={(e) => setPriority(e.target.value)} />}
            </Field>
          )}
          <Field label="TTL" hint="The time that a resolver keeps the record." error={save.error?.fields.ttl}>
            {(p) => (
              <select {...p} className="select" value={ttl} onChange={(e) => setTtl(+e.target.value)}>
                {ttls.map((t) => <option key={t} value={t}>{ttlText(t)}</option>)}
              </select>
            )}
          </Field>
        </div>
      </form>
    </Modal>
  );
}
