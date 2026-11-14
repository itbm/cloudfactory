import { useState, type ReactNode } from "react";
import { Link } from "react-router-dom";
import { useAction, useStore } from "../api/hooks";
import { createDatabase, createDatabaseUser, databaseServersFor, deleteDatabase, deleteDatabaseUser, partialDetails, setDatabaseUser, sharedDatabasesMessage, type GrantInput } from "../api/store";
import type { DatabaseEngine, DatabasePrivileges, DatabaseUser } from "../api/types";
import { Card, ConfirmButton, Field, FormError, Modal } from "../components/ui";
import { bytes, count } from "../lib/format";
import { DATABASE_HOST, DATABASE_PORT, ENGINE_NAME as ENGINE, jobActive } from "../lib/labels";

const ENGINE_RELEASE: Record<DatabaseEngine, string> = { mariadb: "MariaDB 12.3", postgresql: "PostgreSQL 18", valkey: "Valkey 9.1" };
const PRIVILEGES: Record<DatabasePrivileges, string> = { readonly: "Read only", readwrite: "Read and write" };
/** A database user has an SQL engine. The user of a Valkey database comes with the database. */
const SQL_ENGINES: DatabaseEngine[] = ["mariadb", "postgresql"];
const ALL_ENGINES: DatabaseEngine[] = [...SQL_ENGINES, "valkey"];

/**
 * Databases and database users (FR-501, FR-502). With websiteId, it shows one website only.
 * CF-01: a user can have access to more than one database, with a permission for each database.
 */
export function DatabasesPanel({ websiteId }: { websiteId?: string }) {
  const state = useStore();
  const sites = state.websites;
  const dbs = state.databases.filter((d) => !websiteId || d.websiteId === websiteId);
  const users = state.databaseUsers.filter((u) => !websiteId || u.websiteId === websiteId);
  const [dialog, setDialog] = useState<null | "db" | "user" | DatabaseUser>(null);
  const [engine, setEngine] = useState<DatabaseEngine>("mariadb");
  const [name, setName] = useState("");
  const [site, setSite] = useState(websiteId ?? sites[0]?.id ?? "");
  const [withUser, setWithUser] = useState(true);
  // The database server of a new database or a new user. "" lets the panel select it.
  const [dbServer, setDbServer] = useState("");
  const [grants, setGrants] = useState<GrantInput[]>([]);
  const [password, setPassword] = useState<{ user: string; value: string; valkey?: boolean } | null>(null);
  const createDb = useAction(createDatabase);
  const createUser = useAction(createDatabaseUser);
  const changeUser = useAction(setDatabaseUser);
  const siteName = (id: string) => sites.find((w) => w.id === id)?.name ?? "Removed website";
  const serverName = (id?: string) => state.databaseServers.find((x) => x.id === id)?.name;
  // The database servers that the selected website can use: those of its customer on its server.
  const website = sites.find((w) => w.id === site);
  const usable = website?.sharedDatabases ? [] : databaseServersFor(state, website, engine);
  const prefix = (id: string) => `${sites.find((w) => w.id === id)?.unixUser ?? ""}_`;
  // The reason when a website cannot have a Valkey database: an agent of an earlier release
  // cannot operate Valkey, and the shared database containers of an earlier release have none.
  // When the panel does not know the server, the API makes the check.
  const noValkey = (id: string) => {
    const website = sites.find((w) => w.id === id);
    const server = state.servers.find((s) => s.id === website?.serverId);
    if (server?.databaseEngines && !server.databaseEngines.includes("valkey")) return `The agent on the server ${server.name} cannot operate Valkey. Update the agent of the server.`;
    return website?.sharedDatabases ? sharedDatabasesMessage(server?.name ?? "of the website") : undefined;
  };
  const pending = state.jobs.filter((j) => jobActive(j) && j.title.startsWith("Create ") && j.title.includes("database") && (!websiteId || j.websiteId === websiteId));

  const close = () => {
    setDialog(null);
    setEngine("mariadb");
    setName("");
    setDbServer("");
    setGrants([]);
    setWithUser(true);
    createDb.clear();
    createUser.clear();
    changeUser.clear();
  };
  const edit = (u: DatabaseUser) => {
    setGrants(u.grants.map((g) => ({ databaseId: g.databaseId, privileges: g.privileges })));
    setDialog(u);
  };

  return (
    <div className="stack">
      <Card
        title="Databases"
        actions={<button type="button" className="btn btn-primary" onClick={() => setDialog("db")}>New database</button>}
      >
        <div className="table-wrap">
          <table>
            <thead>
              <tr><th scope="col">Name</th><th scope="col">Engine</th>{!websiteId && <th scope="col">Website</th>}<th scope="col">Users</th><th scope="col">Size</th><th scope="col">Host</th><th scope="col"><span className="visually-hidden">Actions</span></th></tr>
            </thead>
            <tbody>
              {dbs.map((d) => {
                const access = state.databaseUsers.flatMap((u) => u.grants.filter((g) => g.databaseId === d.id).map((g) => ({ user: u.name, privileges: g.privileges })));
                return (
                  <tr key={d.id}>
                    <td><code>{d.name}</code></td>
                    <td>{ENGINE[d.engine]}</td>
                    {!websiteId && <td><Link to={`/websites/${d.websiteId}/databases`}>{siteName(d.websiteId)}</Link></td>}
                    <td>
                      {access.length
                        ? access.map((a) => <span key={a.user} className="grant"><code>{a.user}</code> <span className={`badge ${a.privileges === "readonly" ? "badge-neutral" : "badge-info"}`}>{PRIVILEGES[a.privileges]}</span></span>)
                        : <span className="muted">No users</span>}
                    </td>
                    <td>{d.engine === "valkey" ? (d.keys === undefined ? <span className="muted">No report</span> : `${count(d.keys)} ${d.keys === 1 ? "key" : "keys"}`) : bytes(d.sizeBytes)}</td>
                    <td>
                      <code>{d.host ?? DATABASE_HOST[d.engine]}:{d.port ?? DATABASE_PORT[d.engine]}</code>
                      <span className="cell-sub">{d.databaseServerId ? serverName(d.databaseServerId) ?? "Database server" : "Shared container of an earlier release"}</span>
                      {d.engine === "valkey" && <span className="cell-sub">{d.number === undefined ? "The number comes when the job is complete" : <>Database number <strong>{d.number}</strong></>}</span>}
                    </td>
                    <td className="cell-actions" style={{ width: 120 }}>
                      <ConfirmButton className="btn btn-sm btn-danger" title={`Remove database ${d.name}?`} confirm="Remove database" message={d.engine === "valkey" ? <p>The data and the user of the database are removed now. A backup has no Valkey data, so you cannot restore it.</p> : <p>The data is removed now. The users of this database lose access to it. You can restore it from a backup for the retention period.</p>} onConfirm={() => deleteDatabase(d.id)}>Remove</ConfirmButton>
                    </td>
                  </tr>
                );
              })}
              {pending.map((j) => (
                <tr key={j.id}><td colSpan={7} className="muted">{j.title}: {j.steps[Math.min(j.step, j.steps.length - 1)]}…</td></tr>
              ))}
              {dbs.length === 0 && pending.length === 0 && <tr><td colSpan={7} className="muted">No databases.</td></tr>}
            </tbody>
          </table>
        </div>
        {!websiteId && partialDetails() && <p className="card-body muted small">There are many websites, so this list shows only the websites that you opened. Open a website to see its databases.</p>}
        {dbs.some((d) => d.engine === "valkey") && (
          <p className="card-body muted small" style={{ paddingTop: 12 }}>
            To connect to a Valkey database, an app uses the host name and the port in the Host column, the user name, the password and the database number. Valkey is compatible with Redis: for PHP, turn on the extension <code>redis</code> on the Runtime tab. All the databases of a Valkey database server share its memory: when the memory is full, Valkey removes the keys that were not used for the longest time. A backup has no Valkey data.
          </p>
        )}
      </Card>
      <Card title="Database users" actions={<button type="button" className="btn" onClick={() => setDialog("user")}>New user</button>}>
        <div className="card-body" style={{ paddingBottom: 0 }}>
          <FormError error={changeUser.error} />
        </div>
        <div className="table-wrap">
          <table>
            <thead>
              <tr><th scope="col">User</th><th scope="col">Engine</th><th scope="col">Permissions</th><th scope="col">Can connect from</th><th scope="col"><span className="visually-hidden">Actions</span></th></tr>
            </thead>
            <tbody>
              {users.map((u) => (
                <tr key={u.id}>
                  <td><code>{u.name}</code>{!websiteId && <span className="cell-sub">{siteName(u.websiteId)}</span>}</td>
                  <td>{ENGINE[u.engine]}{u.databaseServerId && <span className="cell-sub">{serverName(u.databaseServerId) ?? "Database server"}</span>}</td>
                  <td>
                    {u.grants.length
                      ? u.grants.map((g) => <span key={g.databaseId} className="grant"><code>{g.database}</code> <span className={`badge ${g.privileges === "readonly" ? "badge-neutral" : "badge-info"}`}>{PRIVILEGES[g.privileges]}</span></span>)
                      : <span className="muted">No databases</span>}
                  </td>
                  <td>
                    {u.engine === "valkey" ? "Website container only" : (
                      <>
                        <label className="visually-hidden" htmlFor={`access-${u.id}`}>Can connect from</label>
                        <select id={`access-${u.id}`} className="select" style={{ maxWidth: 210 }} value={u.access} onChange={(e) => void changeUser.run(u.id, { access: e.target.value as "website" | "anywhere" })}>
                          <option value="website">Website container only</option>
                          <option value="anywhere">Anywhere, with TLS</option>
                        </select>
                      </>
                    )}
                  </td>
                  <td className="cell-actions">
                    <div className="btn-group" style={{ justifyContent: "flex-end", flexWrap: "nowrap" }}>
                      {u.engine !== "valkey" && <button type="button" className="btn btn-sm" onClick={() => edit(u)}>Permissions</button>}
                      <ConfirmButton className="btn btn-sm" danger={false} title={`Make a new password for ${u.name}?`} confirm="Make new password" message={<p>Apps that use the old password cannot connect.{u.engine === "valkey" && " Valkey closes the connections that are open with the old password."}</p>} onConfirm={async () => {
                        const r = await changeUser.run(u.id, { resetPassword: true });
                        if (r.ok && r.value) setPassword({ user: u.name, value: r.value });
                      }}>New password</ConfirmButton>
                      {u.engine !== "valkey" && <ConfirmButton className="btn btn-sm btn-danger" title={`Remove user ${u.name}?`} confirm="Remove user" message={<p>Apps that use this user cannot connect.</p>} onConfirm={() => deleteDatabaseUser(u.id)}>Remove</ConfirmButton>}
                    </div>
                  </td>
                </tr>
              ))}
              {users.length === 0 && <tr><td colSpan={5} className="muted">No users.</td></tr>}
            </tbody>
          </table>
        </div>
        <p className="card-body muted small" style={{ paddingTop: 12 }}>Each customer has its own database servers. By default, only the websites of the customer on the same server can connect to them, and each connection needs a user name and a password. A user is in one database server, and can have access only to the databases of its website in that database server.{users.some((u) => u.engine === "valkey") && " The user of a Valkey database has access to that database only. To remove the user, remove the database."}</p>
      </Card>

      {dialog === "db" && (
        <Modal
          title="New database"
          onClose={close}
          footer={<><button type="button" className="btn" onClick={close}>Cancel</button><button type="submit" form="new-db" className="btn btn-primary">Create database</button></>}
        >
          <form id="new-db" noValidate onSubmit={async (e) => {
            e.preventDefault();
            const full = prefix(site) + name.trim();
            const result = await createDb.run(site, engine, name, engine === "valkey" || withUser, dbServer || undefined);
            if (result.ok) {
              close();
              if (result.value) setPassword({ user: full, value: result.value, valkey: engine === "valkey" });
            }
          }}>
            <FormError error={createDb.error} />
            <DbFields engines={ALL_ENGINES} noValkey={noValkey(site)} engine={engine} setEngine={(e) => { setEngine(e); setDbServer(""); }} name={name} setName={setName} site={site} setSite={(s) => { setSite(s); setDbServer(""); if (engine === "valkey" && noValkey(s)) setEngine("mariadb"); }} showSite={!websiteId} prefix={prefix(site)} error={createDb.error?.fields.name} engineError={createDb.error?.fields.engine}>
              {!website?.sharedDatabases && (
                <div style={{ marginTop: 14 }}>
                  <Field
                    label="Database server"
                    hint={usable.length === 0
                      ? <>The customer has no {ENGINE[engine]} database server on the server of this website. The panel makes one with the plan of the customer.</>
                      : <>The websites of the customer on the same server can connect to it. {websiteId ? <>To make more database servers, go to the <Link to="/databases">Databases</Link> page.</> : "To make more database servers, select New database server on this page."}</>}
                    error={createDb.error?.fields.databaseServerId}
                  >
                    {(p) => (
                      <select {...p} className="select" value={dbServer} onChange={(e) => setDbServer(e.target.value)}>
                        <option value="">{usable.length === 0 ? `New ${ENGINE[engine]} database server` : "Automatic: the first database server with space"}</option>
                        {usable.map((x) => <option key={x.id} value={x.id} disabled={x.databases >= x.maxDatabases}>{x.name} · port {x.port} · {x.databases} of {x.maxDatabases} databases</option>)}
                      </select>
                    )}
                  </Field>
                </div>
              )}
            </DbFields>
            {engine === "valkey" ? (
              <p className="muted small" style={{ marginTop: 12 }}>Valkey keeps keys and values in memory, for a cache, sessions and queues. It is compatible with Redis. The database gets a number, and a user with the same name as the database. An app connects with the user name, the password and the number.</p>
            ) : (
              <label className="check" style={{ marginTop: 12 }}>
                <input type="checkbox" checked={withUser} onChange={(e) => setWithUser(e.target.checked)} />
                Also make a user with the same name, with read and write access
              </label>
            )}
          </form>
        </Modal>
      )}
      {dialog === "user" && (
        <Modal
          title="New database user"
          onClose={close}
          footer={<><button type="button" className="btn" onClick={close}>Cancel</button><button type="submit" form="new-user" className="btn btn-primary">Create user</button></>}
        >
          <form id="new-user" noValidate onSubmit={async (e) => {
            e.preventDefault();
            const full = prefix(site) + name.trim();
            const result = await createUser.run(site, engine, name, grants, usable.length > 1 ? dbServer || usable[0].id : undefined);
            if (result.ok) { close(); setPassword({ user: full, value: result.value }); }
          }}>
            <FormError error={createUser.error} />
            <DbFields engines={SQL_ENGINES} engine={engine} setEngine={(e) => { setEngine(e); setDbServer(""); setGrants([]); }} name={name} setName={setName} site={site} setSite={(s) => { setSite(s); setDbServer(""); setGrants([]); }} showSite={!websiteId} prefix={prefix(site)} error={createUser.error?.fields.name} engineError={createUser.error?.fields.engine}>
              {usable.length > 1 && (
                <div style={{ marginTop: 14 }}>
                  <Field label="Database server" hint="A user is in one database server. It can have access only to the databases in it." error={createUser.error?.fields.databaseServerId}>
                    {(p) => (
                      <select {...p} className="select" value={dbServer || usable[0].id} onChange={(e) => { setDbServer(e.target.value); setGrants([]); }}>
                        {usable.map((x) => <option key={x.id} value={x.id}>{x.name} · port {x.port}</option>)}
                      </select>
                    )}
                  </Field>
                </div>
              )}
            </DbFields>
            <GrantEditor websiteId={site} engine={engine} databaseServerId={usable.length > 1 ? dbServer || usable[0].id : undefined} grants={grants} setGrants={setGrants} />
          </form>
        </Modal>
      )}
      {dialog && typeof dialog === "object" && (
        <Modal
          title={`Permissions of ${dialog.name}`}
          onClose={close}
          footer={<><button type="button" className="btn" onClick={close}>Cancel</button><button type="submit" form="edit-user" className="btn btn-primary">Save permissions</button></>}
        >
          <form id="edit-user" noValidate onSubmit={async (e) => {
            e.preventDefault();
            if ((await changeUser.run(dialog.id, { grants })).ok) close();
          }}>
            <FormError error={changeUser.error} />
            <GrantEditor websiteId={dialog.websiteId} engine={dialog.engine} databaseServerId={dialog.databaseServerId} grants={grants} setGrants={setGrants} />
          </form>
        </Modal>
      )}
      {password && (
        <Modal title="Copy the password" onClose={() => setPassword(null)} footer={<button type="button" className="btn btn-primary" onClick={() => setPassword(null)}>I copied the password</button>}>
          <p>The password of <code>{password.user}</code>. The panel shows it one time only.</p>
          <div className="copy-field">
            <code>{password.value}</code>
            <button type="button" className="btn" onClick={() => void navigator.clipboard?.writeText(password.value)}>Copy</button>
          </div>
          {password.valkey && <p className="muted small" style={{ marginTop: 12 }}>The user name is the name of the database. When the job is complete, the list of databases shows the database number.</p>}
        </Modal>
      )}
    </div>
  );
}

/**
 * One row for each database of the website with the same engine: no access, read only, or read
 * and write. With a database server, it shows only the databases in that database server.
 */
function GrantEditor({ websiteId, engine, databaseServerId, grants, setGrants }: { websiteId: string; engine: DatabaseEngine; databaseServerId?: string; grants: GrantInput[]; setGrants: (g: GrantInput[]) => void }) {
  const state = useStore();
  const dbs = state.databases.filter((d) => d.websiteId === websiteId && d.engine === engine && (!databaseServerId || d.databaseServerId === databaseServerId));
  const set = (databaseId: string, value: DatabasePrivileges | "") => {
    const rest = grants.filter((g) => g.databaseId !== databaseId);
    setGrants(value ? [...rest, { databaseId, privileges: value }] : rest);
  };
  return (
    <fieldset className="grant-list" style={{ marginTop: 14 }}>
      <legend>Permissions</legend>
      {dbs.length === 0 && <p className="muted small">This website has no {ENGINE[engine]} databases{databaseServerId ? " in this database server" : ""}.</p>}
      {dbs.map((d) => (
        <div key={d.id} className="grant-row">
          <label htmlFor={`grant-${d.id}`}><code>{d.name}</code></label>
          <select id={`grant-${d.id}`} className="select" value={grants.find((g) => g.databaseId === d.id)?.privileges ?? ""} onChange={(e) => set(d.id, e.target.value as DatabasePrivileges | "")}>
            <option value="">No access</option>
            <option value="readonly">{PRIVILEGES.readonly}</option>
            <option value="readwrite">{PRIVILEGES.readwrite}</option>
          </select>
        </div>
      ))}
      <p className="hint" style={{ marginTop: 8 }}>Read only: SELECT and SHOW VIEW. Read and write: all permissions on the database.</p>
    </fieldset>
  );
}

/** The fields of a new database or a new user. The children come after the engine, before the name. */
function DbFields(props: { engines: DatabaseEngine[]; noValkey?: string; engine: DatabaseEngine; setEngine: (e: DatabaseEngine) => void; name: string; setName: (n: string) => void; site: string; setSite: (s: string) => void; showSite: boolean; prefix: string; error?: string; engineError?: string; children?: ReactNode }) {
  const state = useStore();
  return (
    <>
      {props.showSite && (
        <Field label="Website">
          {(p) => (
            <select {...p} className="select" value={props.site} onChange={(e) => props.setSite(e.target.value)}>
              {state.websites.map((w) => <option key={w.id} value={w.id}>{w.name}</option>)}
            </select>
          )}
        </Field>
      )}
      <fieldset className="radio-cards" style={{ marginTop: 14, gridTemplateColumns: "repeat(auto-fit, minmax(140px, 1fr))" }}>
        <legend>Engine</legend>
        {props.engines.map((e) => {
          const disabled = e === "valkey" && !!props.noValkey;
          return (
            <label className={`radio-card${disabled ? " radio-card-disabled" : ""}`} key={e}>
              <input type="radio" name="engine" checked={props.engine === e} disabled={disabled} onChange={() => props.setEngine(e)} />
              <span><strong>{ENGINE[e]}</strong><span>{ENGINE_RELEASE[e]}</span></span>
            </label>
          );
        })}
      </fieldset>
      {props.engines.includes("valkey") && props.noValkey && <p className="muted small" style={{ marginTop: 6 }}>{props.noValkey}</p>}
      {props.engineError && <p className="text-danger small" role="alert" style={{ marginTop: 6 }}>{props.engineError}</p>}
      {props.children}
      <div style={{ marginTop: 14 }}>
        <Field label="Name" hint={<>Each name starts with <code>{props.prefix}</code>, the Unix user of the website. Use lower-case letters, digits and underscores.</>} error={props.error}>
          {(p) => (
            <div className="input-prefix">
              <span aria-hidden="true">{props.prefix}</span>
              <input {...p} className="input" value={props.name} onChange={(e) => props.setName(e.target.value)} autoCapitalize="none" spellCheck={false} />
            </div>
          )}
        </Field>
      </div>
    </>
  );
}
