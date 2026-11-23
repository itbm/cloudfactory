import { useState } from "react";
import { useAction, useLoad, useStore } from "../../api/hooks";
import { type ApiError, listFirewallEvents, readSecurity, setSecurity } from "../../api/store";
import type { FirewallEvent, FirewallMode, Website, WebsiteSecurity, WebsiteSecurityChange } from "../../api/types";
import { Card, ConfirmButton, Field, FormError, JobRow, Modal, Status, TableWrap } from "../../components/ui";
import { relativeTime } from "../../lib/format";
import { jobActive } from "../../lib/labels";

/** The title of the job that applies a change (SecurityController::update). */
const JOB_TITLE = "Set website protection";

const MODES: [FirewallMode, string, string][] = [
  ["off", "Off", "The firewall does not examine the requests."],
  ["detect", "Detect", "The firewall records attacks and blocks nothing. Use this mode first."],
  ["block", "Block", "The firewall stops attacks with the status 403 Forbidden."],
];

/** The sign-in paths of WordPress (WebsiteSecurity::WORDPRESS_SIGN_IN_PATHS). */
const WORDPRESS_SIGN_IN_PATHS = ["/wp-login.php", "/xmlrpc.php"];

/** The number of firewall events that the tab shows before "Show all". */
const FIRST_EVENTS = 5;

/** The largest numbers of the API (InputRules). */
const MAX_PATH_USERS = 10;
const MAX_PROTECTED_PATHS = 20;

type Save = (change: WebsiteSecurityChange) => Promise<unknown>;

/** The values in a text field: one on each line, or with commas or spaces between them. */
const valuesOf = (text: string) => text.split(/[\s,]+/).filter(Boolean);

/**
 * The message of the API for a list field. A message for one value of the list (for example
 * "ipRules.allow.2") starts with that value.
 */
function errorOf(error: ApiError | null, key: string, values: (string | number)[] = []): string | undefined {
  if (!error) return undefined;
  const hit = Object.keys(error.fields).find((k) => k === key || k.startsWith(`${key}.`));
  if (!hit) return undefined;
  const index = Number(hit.slice(key.length + 1));
  return hit !== key && Number.isInteger(index) && values[index] !== undefined ? `${values[index]}: ${error.fields[hit]}` : error.fields[hit];
}

/**
 * The protection of a website in the edge proxy: the firewall, the sign-in limit, the IP address
 * rules and the password-protected paths. The panel saves a change at once, and a job applies
 * it to the edge proxy. The website does not restart.
 */
export function SecurityTab({ website }: { website: Website }) {
  const state = useStore();
  const id = website.id;
  const security = useLoad(() => readSecurity(id), [id]);
  const last = state.jobs.find((j) => j.websiteId === id && j.title === JOB_TITLE);
  const save: Save = (change) => setSecurity(id, change).then(security.set);

  if (!security.data) {
    return (
      <Card title="Protection">
        <div className="card-body">{security.error ? <FormError error={security.error} /> : <p className="muted">Loading…</p>}</div>
      </Card>
    );
  }
  const s = security.data;
  return (
    <div className="stack">
      {last && (jobActive(last) || last.state === "failed") && (
        <Card title="Change of the protection">
          <div className="card-body"><ul className="job-list"><JobRow job={last} /></ul></div>
        </Card>
      )}
      <div className="grid-2">
        <FirewallCard key={JSON.stringify(s.firewall)} security={s} save={save} />
        <SignInLimitCard key={JSON.stringify(s.signInLimit)} security={s} save={save} />
      </div>
      {s.firewall.mode !== "off" && <FirewallEventsCard website={website} security={s} save={save} />}
      <div className="grid-2">
        <IpRulesCard key={JSON.stringify(s.ipRules)} security={s} save={save} />
        <ProtectedPathsCard security={s} save={save} />
      </div>
    </div>
  );
}

function FirewallCard({ security, save }: { security: WebsiteSecurity; save: Save }) {
  const [mode, setMode] = useState(security.firewall.mode);
  const [rules, setRules] = useState(security.firewall.excludedRules.join("\n"));
  const [paths, setPaths] = useState(security.firewall.skippedPaths.join("\n"));
  const ruleValues = valuesOf(rules).map((r) => (/^\d+$/.test(r) ? Number(r) : r));
  const pathValues = valuesOf(paths);
  const action = useAction(() => save({ firewall: { mode, excludedRules: ruleValues as number[], skippedPaths: pathValues } }));
  return (
    <Card title="Firewall">
      <form className="card-body" noValidate onSubmit={(e) => { e.preventDefault(); void action.run(); }}>
        <FormError error={action.error} shown={Object.keys(action.error?.fields ?? {}).filter((k) => k.startsWith("firewall."))} />
        <fieldset className="radio-cards" style={{ gridTemplateColumns: "minmax(0, 1fr)" }}>
          <legend>Mode</legend>
          {MODES.map(([value, label, hint]) => (
            <label key={value} className="radio-card">
              <input type="radio" name="firewall-mode" checked={mode === value} onChange={() => setMode(value)} />
              <span><strong>{label}</strong><span>{hint}</span></span>
            </label>
          ))}
        </fieldset>
        <div className="form-row" style={{ marginTop: 16 }}>
          <Field label="Rules that are off" hint="The IDs of the rules, one on each line. For example 942100." error={errorOf(action.error, "firewall.excludedRules", ruleValues)}>
            {(p) => <textarea {...p} className="textarea" rows={3} value={rules} onChange={(e) => setRules(e.target.value)} spellCheck={false} />}
          </Field>
          <Field label="Paths with no firewall" hint="For example /wp-admin/. A path that ends with / includes all paths in it." error={errorOf(action.error, "firewall.skippedPaths", pathValues)}>
            {(p) => <textarea {...p} className="textarea" rows={3} value={paths} onChange={(e) => setPaths(e.target.value)} spellCheck={false} autoCapitalize="none" />}
          </Field>
        </div>
        <p className="muted small" style={{ marginTop: 12 }}>
          The firewall is Coraza with the OWASP Core Rule Set, paranoia level 1. It examines the address, the headers and the first 1 MB of the body of each request. It does not examine the responses.
        </p>
        <button type="submit" className="btn btn-primary" style={{ marginTop: 14 }} disabled={action.busy}>{action.busy ? "Saving…" : "Save firewall"}</button>
      </form>
    </Card>
  );
}

function SignInLimitCard({ security, save }: { security: WebsiteSecurity; save: Save }) {
  const [limit, setLimit] = useState(String(security.signInLimit.requestsPerMinute));
  const [paths, setPaths] = useState((security.signInLimit.paths.length ? security.signInLimit.paths : WORDPRESS_SIGN_IN_PATHS).join("\n"));
  const pathValues = valuesOf(paths);
  const action = useAction(() => save({ signInLimit: { requestsPerMinute: Number(limit) || 0, paths: pathValues } }));
  return (
    <Card title="Sign-in limit">
      <form className="card-body" noValidate onSubmit={(e) => { e.preventDefault(); void action.run(); }}>
        <FormError error={action.error} shown={Object.keys(action.error?.fields ?? {}).filter((k) => k.startsWith("signInLimit."))} />
        <p className="muted" style={{ marginBottom: 14 }}>
          The limit stops attacks that try many passwords. The edge proxy counts the POST requests to the sign-in paths from each IP address. After the limit, it answers with the status 429 Too Many Requests until the minute ends.
        </p>
        <Field label="Requests in one minute" hint="Enter 0 for no limit, or a number from 3 to 600. For example 10." error={errorOf(action.error, "signInLimit.requestsPerMinute")}>
          {(p) => <input {...p} className="input" type="number" min={0} max={600} value={limit} onChange={(e) => setLimit(e.target.value)} style={{ maxWidth: 160 }} />}
        </Field>
        <Field label="Sign-in paths" hint={<>One on each line. <button type="button" className="btn-link" style={{ padding: 0, minHeight: 0 }} onClick={() => setPaths(WORDPRESS_SIGN_IN_PATHS.join("\n"))}>Use the paths of WordPress</button></>} error={errorOf(action.error, "signInLimit.paths", pathValues)}>
          {(p) => <textarea {...p} className="textarea" rows={3} value={paths} onChange={(e) => setPaths(e.target.value)} spellCheck={false} autoCapitalize="none" />}
        </Field>
        <button type="submit" className="btn btn-primary" style={{ marginTop: 14 }} disabled={action.busy}>{action.busy ? "Saving…" : "Save limit"}</button>
      </form>
    </Card>
  );
}

function FirewallEventsCard({ website, security, save }: { website: Website; security: WebsiteSecurity; save: Save }) {
  const mode = security.firewall.mode;
  const events = useLoad(() => listFirewallEvents(website.id), [website.id, mode]);
  const list: FirewallEvent[] = events.data ?? [];
  const [all, setAll] = useState(false);
  const shown = all ? list : list.slice(0, FIRST_EVENTS);
  const excluded = security.firewall.excludedRules;
  const turnOff = useAction((rule: number) => save({ firewall: { mode, excludedRules: [...excluded, rule] } }));
  return (
    <Card
      title="Firewall events"
      actions={<button type="button" className="btn btn-sm" disabled={events.loading} onClick={events.reload}>{events.loading ? "Loading…" : "Refresh"}</button>}
    >
      {(events.error ?? turnOff.error) && <div className="card-body" style={{ paddingBottom: 0 }}><FormError error={events.error ?? turnOff.error} /></div>}
      <TableWrap>
        <table aria-busy={events.loading}>
          <thead><tr><th scope="col">Time</th><th scope="col">Client</th><th scope="col">Request</th><th scope="col">Result</th><th scope="col">Rules</th></tr></thead>
          <tbody>
            {shown.map((e, i) => (
              <tr key={`${e.at}-${i}`}>
                <td className="nowrap">{e.at ? <time dateTime={e.at} title={e.at}>{relativeTime(e.at)}</time> : <span className="muted">Unknown</span>}</td>
                <td><code>{e.clientIp}</code></td>
                <td>
                  <code style={{ wordBreak: "break-all" }}>{e.uri}</code>
                  {e.host && <span className="cell-sub">{e.host}</span>}
                </td>
                <td className="nowrap">
                  <Status tone={e.action === "blocked" ? "danger" : "warning"}>{e.action === "blocked" ? "Blocked" : "Detected"}</Status>
                  <span className="cell-sub">Score {e.score}</span>
                </td>
                <td>
                  <ul className="plain-list" style={{ listStyle: "none", padding: 0 }}>
                    {e.rules.map((r) => (
                      <li key={r.id} style={{ marginBottom: 6 }}>
                        <code>{r.id}</code> {r.message}
                        {r.data && <span className="cell-sub mono" style={{ wordBreak: "break-all" }}>{r.data}</span>}
                        {!excluded.includes(r.id) && (
                          <ConfirmButton
                            className="btn-link small"
                            danger={false}
                            title={`Turn off rule ${r.id}?`}
                            confirm="Turn off"
                            disabled={turnOff.busy}
                            message={<p>The firewall stops using rule <code>{r.id}</code> for this website. Do this only when the rule blocks a request that the website needs. The other rules stay on.</p>}
                            onConfirm={() => turnOff.run(r.id)}
                          >
                            Turn off this rule<span className="visually-hidden"> {r.id}</span>
                          </ConfirmButton>
                        )}
                      </li>
                    ))}
                  </ul>
                </td>
              </tr>
            ))}
            {list.length === 0 && <tr><td colSpan={5} className="muted">{events.loading ? "Loading…" : "The firewall found no attacks."}</td></tr>}
          </tbody>
        </table>
      </TableWrap>
      {list.length > shown.length && (
        <div className="card-body" style={{ paddingBottom: 0 }}>
          <button type="button" className="btn btn-sm" onClick={() => setAll(true)}>Show all {list.length} events</button>
        </div>
      )}
      <p className="card-body muted small" style={{ paddingTop: 12 }}>
        These are the last requests that the firewall {mode === "block" ? "blocked" : "found"}. Each request has a score, and the firewall blocks a request with a score of 5 or more. Before you select Block, make sure that no event is a request that the website needs.
      </p>
    </Card>
  );
}

function IpRulesCard({ security, save }: { security: WebsiteSecurity; save: Save }) {
  const [allow, setAllow] = useState(security.ipRules.allow.join("\n"));
  const [block, setBlock] = useState(security.ipRules.block.join("\n"));
  const allowValues = valuesOf(allow);
  const blockValues = valuesOf(block);
  const action = useAction(() => save({ ipRules: { allow: allowValues, block: blockValues } }));
  return (
    <Card title="IP addresses">
      <form className="card-body" noValidate onSubmit={(e) => { e.preventDefault(); void action.run(); }}>
        <FormError error={action.error} shown={Object.keys(action.error?.fields ?? {}).filter((k) => k.startsWith("ipRules."))} />
        <Field label="Blocked addresses" hint="Addresses and ranges, one on each line. For example 198.51.100.7 or 203.0.113.0/24. The edge proxy refuses them with the status 403 Forbidden." error={errorOf(action.error, "ipRules.block", blockValues)}>
          {(p) => <textarea {...p} className="textarea" rows={4} value={block} onChange={(e) => setBlock(e.target.value)} spellCheck={false} />}
        </Field>
        <Field label="Allowed addresses" hint="Keep this list empty to allow all addresses. When the list has an address, the edge proxy refuses all other addresses." error={errorOf(action.error, "ipRules.allow", allowValues)}>
          {(p) => <textarea {...p} className="textarea" rows={4} value={allow} onChange={(e) => setAllow(e.target.value)} spellCheck={false} />}
        </Field>
        <p className="muted small" style={{ marginTop: 12 }}>
          The edge proxy uses the address that connects to it. When the website is behind a CDN or a proxy, for example Cloudflare, all requests come from the addresses of the CDN.
        </p>
        <button type="submit" className="btn btn-primary" style={{ marginTop: 14 }} disabled={action.busy}>{action.busy ? "Saving…" : "Save addresses"}</button>
      </form>
    </Card>
  );
}

type PathForm = { index: number; path: string; users: { name: string; password: string; known: boolean }[] };

function ProtectedPathsCard({ security, save }: { security: WebsiteSecurity; save: Save }) {
  const paths = security.protectedPaths;
  const [editing, setEditing] = useState<PathForm | null>(null);
  // A user with no password keeps the password that the panel has.
  const current = () => paths.map((p) => ({ path: p.path, users: p.users.map((u) => ({ name: u.name })) }));
  const remove = useAction((path: string) => save({ protectedPaths: current().filter((p) => p.path !== path) }));
  return (
    <Card
      title="Password-protected paths"
      actions={<button type="button" className="btn btn-sm" disabled={paths.length >= MAX_PROTECTED_PATHS} onClick={() => setEditing({ index: paths.length, path: "/", users: [{ name: "", password: "", known: false }] })}>Add a path</button>}
    >
      {remove.error && <div className="card-body" style={{ paddingBottom: 0 }}><FormError error={remove.error} /></div>}
      <TableWrap>
        <table>
          <thead><tr><th scope="col">Path</th><th scope="col">Users</th><th scope="col"><span className="visually-hidden">Actions</span></th></tr></thead>
          <tbody>
            {paths.map((p, i) => (
              <tr key={p.path}>
                <td><code>{p.path}</code></td>
                <td>{p.users.map((u) => u.name).join(", ")}</td>
                <td>
                  <div className="btn-group" style={{ justifyContent: "flex-end" }}>
                    <button type="button" className="btn btn-sm" onClick={() => setEditing({ index: i, path: p.path, users: p.users.map((u) => ({ name: u.name, password: "", known: true })) })}>Change<span className="visually-hidden"> {p.path}</span></button>
                    <ConfirmButton className="btn btn-sm btn-danger" title="Remove the password?" confirm="Remove" disabled={remove.busy} message={<p>All visitors can open <code>{p.path}</code> again.</p>} onConfirm={() => remove.run(p.path)}>Remove<span className="visually-hidden"> {p.path}</span></ConfirmButton>
                  </div>
                </td>
              </tr>
            ))}
            {paths.length === 0 && <tr><td colSpan={3} className="muted">No path has a password.</td></tr>}
          </tbody>
        </table>
      </TableWrap>
      <p className="card-body muted small" style={{ paddingTop: 12 }}>
        The browser asks for a user name and a password before it shows the path. Use this for a staging copy or an admin area. Use HTTPS only: the browser sends the password with each request.
      </p>
      {editing && <PathModal form={editing} security={security} save={save} onClose={() => setEditing(null)} />}
    </Card>
  );
}

function PathModal({ form, security, save, onClose }: { form: PathForm; security: WebsiteSecurity; save: Save; onClose: () => void }) {
  const isNew = form.index >= security.protectedPaths.length;
  const [path, setPath] = useState(form.path);
  const [users, setUsers] = useState(form.users);
  const index = form.index;
  const action = useAction(async () => {
    const list: NonNullable<WebsiteSecurityChange["protectedPaths"]> = security.protectedPaths.map((p) => ({ path: p.path, users: p.users.map((u) => ({ name: u.name })) }));
    list[index] = { path: path.trim(), users: users.map((u) => ({ name: u.name.trim(), ...(u.password ? { password: u.password } : {}) })) };
    await save({ protectedPaths: list });
    onClose();
  });
  const at = `protectedPaths.${index}`;
  const change = (i: number, part: Partial<PathForm["users"][number]>) => setUsers(users.map((u, j) => (j === i ? { ...u, ...part } : u)));
  const shown = Object.keys(action.error?.fields ?? {}).filter((k) => k.startsWith(`${at}.`));
  return (
    <Modal
      wide
      title={isNew ? "Add a password-protected path" : `Change the users of ${form.path}`}
      onClose={onClose}
      footer={
        <>
          <button type="button" className="btn" onClick={onClose}>Cancel</button>
          <button type="submit" form="protected-path" className="btn btn-primary" disabled={action.busy}>{action.busy ? "Saving…" : "Save"}</button>
        </>
      }
    >
      <form id="protected-path" noValidate onSubmit={(e) => { e.preventDefault(); void action.run(); }}>
        <FormError error={action.error} shown={shown} />
        <Field label="Path" hint="For example /staging/ for the directory and all paths in it, or / for the full website." error={errorOf(action.error, `${at}.path`)}>
          {(p) => <input {...p} className="input mono" value={path} disabled={!isNew} onChange={(e) => setPath(e.target.value)} spellCheck={false} autoCapitalize="none" />}
        </Field>
        <fieldset className="form-section">
          <legend>Users</legend>
          <p className="hint">A new user needs a password of 8 to 72 characters.{users.some((u) => u.known) && " Keep the password of a user empty to keep the password."}</p>
          {users.map((u, i) => (
            <div key={i} className="form-row" style={{ gridTemplateColumns: "minmax(0, 1fr) minmax(0, 1fr) auto", alignItems: "end", marginTop: i ? 12 : 0 }}>
              <Field label="User name" error={errorOf(action.error, `${at}.users.${i}.name`)}>
                {(p) => <input {...p} className="input" value={u.name} disabled={u.known} onChange={(e) => change(i, { name: e.target.value })} autoComplete="off" spellCheck={false} autoCapitalize="none" />}
              </Field>
              <Field label={u.known ? "New password" : "Password"} error={errorOf(action.error, `${at}.users.${i}.password`)}>
                {(p) => <input {...p} className="input" type="password" value={u.password} onChange={(e) => change(i, { password: e.target.value })} autoComplete="new-password" />}
              </Field>
              <div className="field" style={{ flex: "none" }}>
                <button type="button" className="btn" disabled={users.length === 1} onClick={() => setUsers(users.filter((_, j) => j !== i))}>Remove<span className="visually-hidden"> user {u.name || i + 1}</span></button>
              </div>
            </div>
          ))}
          <button type="button" className="btn btn-sm" style={{ marginTop: 12 }} disabled={users.length >= MAX_PATH_USERS} onClick={() => setUsers([...users, { name: "", password: "", known: false }])}>Add a user</button>
        </fieldset>
      </form>
    </Modal>
  );
}
