import { useState } from "react";
import { Link } from "react-router-dom";
import { useAction, useStore } from "../api/hooks";
import { createDatabase, createDatabaseUser, deleteDatabase, deleteDatabaseUser, setDatabaseUser } from "../api/store";
import type { DatabaseEngine } from "../api/types";
import { Card, ConfirmButton, Field, FormError, Modal } from "../components/ui";
import { bytes } from "../lib/format";

const ENGINE: Record<DatabaseEngine, string> = { mariadb: "MariaDB", postgresql: "PostgreSQL" };

/** Databases and database users (FR-501, FR-502). With websiteId, it shows one website only. */
export function DatabasesPanel({ websiteId }: { websiteId?: string }) {
  const state = useStore();
  const sites = state.websites;
  const dbs = state.databases.filter((d) => !websiteId || d.websiteId === websiteId);
  const users = state.databaseUsers.filter((u) => !websiteId || u.websiteId === websiteId);
  const [dialog, setDialog] = useState<null | "db" | "user">(null);
  const [engine, setEngine] = useState<DatabaseEngine>("mariadb");
  const [name, setName] = useState("");
  const [site, setSite] = useState(websiteId ?? sites[0]?.id ?? "");
  const [grant, setGrant] = useState<string[]>([]);
  const [password, setPassword] = useState<string | null>(null);
  const createDb = useAction(createDatabase);
  const createUser = useAction(createDatabaseUser);
  const siteName = (id: string) => sites.find((w) => w.id === id)?.name ?? "Removed website";
  const pending = state.jobs.filter((j) => j.state === "running" && j.title.startsWith("Create ") && j.title.includes("database") && (!websiteId || j.websiteId === websiteId));

  const close = () => {
    setDialog(null);
    setName("");
    setGrant([]);
    createDb.clear();
    createUser.clear();
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
              <tr><th scope="col">Name</th><th scope="col">Engine</th>{!websiteId && <th scope="col">Website</th>}<th scope="col">Size</th><th scope="col">Host</th><th scope="col"><span className="visually-hidden">Actions</span></th></tr>
            </thead>
            <tbody>
              {dbs.map((d) => (
                <tr key={d.id}>
                  <td><code>{d.name}</code></td>
                  <td>{ENGINE[d.engine]}</td>
                  {!websiteId && <td><Link to={`/websites/${d.websiteId}/databases`}>{siteName(d.websiteId)}</Link></td>}
                  <td>{bytes(d.sizeBytes)}</td>
                  <td><code>{d.engine === "mariadb" ? "mariadb.internal:3306" : "postgres.internal:5432"}</code></td>
                  <td className="cell-actions" style={{ width: 120 }}>
                    <ConfirmButton className="btn btn-sm btn-danger" title={`Remove database ${d.name}?`} confirm="Remove database" message={<p>The data is removed now. You can restore it from a backup for the retention period.</p>} onConfirm={() => deleteDatabase(d.id)}>Remove</ConfirmButton>
                  </td>
                </tr>
              ))}
              {pending.map((j) => (
                <tr key={j.id}><td colSpan={6} className="muted">{j.title}: {j.steps[Math.min(j.step, j.steps.length - 1)]}…</td></tr>
              ))}
              {dbs.length === 0 && pending.length === 0 && <tr><td colSpan={6} className="muted">No databases.</td></tr>}
            </tbody>
          </table>
        </div>
      </Card>
      <Card title="Database users" actions={<button type="button" className="btn" onClick={() => setDialog("user")}>New user</button>}>
        <div className="table-wrap">
          <table>
            <thead>
              <tr><th scope="col">User</th><th scope="col">Engine</th><th scope="col">Databases</th><th scope="col">Can connect from</th><th scope="col"><span className="visually-hidden">Actions</span></th></tr>
            </thead>
            <tbody>
              {users.map((u) => (
                <tr key={u.id}>
                  <td><code>{u.name}</code>{!websiteId && <span className="cell-sub">{siteName(u.websiteId)}</span>}</td>
                  <td>{ENGINE[u.engine]}</td>
                  <td>{u.databases.length ? u.databases.map((d) => <code key={d} style={{ marginRight: 6 }}>{d}</code>) : <span className="muted">None</span>}</td>
                  <td>
                    <label className="visually-hidden" htmlFor={`access-${u.id}`}>Can connect from</label>
                    <select id={`access-${u.id}`} className="select" value={u.access} onChange={(e) => setDatabaseUser(u.id, { access: e.target.value as "website" | "anywhere" })}>
                      <option value="website">The website container only</option>
                      <option value="anywhere">Anywhere, with TLS</option>
                    </select>
                  </td>
                  <td className="cell-actions" style={{ width: 120 }}>
                    <ConfirmButton className="btn btn-sm btn-danger" title={`Remove user ${u.name}?`} confirm="Remove user" message={<p>Apps that use this user cannot connect.</p>} onConfirm={() => deleteDatabaseUser(u.id)}>Remove</ConfirmButton>
                  </td>
                </tr>
              ))}
              {users.length === 0 && <tr><td colSpan={5} className="muted">No users.</td></tr>}
            </tbody>
          </table>
        </div>
        <p className="card-body muted small" style={{ paddingTop: 12 }}>By default, only the container of the website can connect to its databases.</p>
      </Card>

      {dialog === "db" && (
        <Modal
          title="New database"
          onClose={close}
          footer={<><button type="button" className="btn" onClick={close}>Cancel</button><button type="submit" form="new-db" className="btn btn-primary">Create database</button></>}
        >
          <form id="new-db" noValidate onSubmit={async (e) => { e.preventDefault(); if ((await createDb.run(site, engine, name)).ok) close(); }}>
            <FormError error={createDb.error} />
            <DbFields engine={engine} setEngine={setEngine} name={name} setName={setName} site={site} setSite={setSite} showSite={!websiteId} error={createDb.error?.fields.name} />
            <p className="hint" style={{ marginTop: 12 }}>The panel also makes a user with the same name that can connect from the website container only.</p>
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
            const result = await createUser.run(site, engine, name, grant);
            if (result.ok) { close(); setPassword(result.value); }
          }}>
            <FormError error={createUser.error} />
            <DbFields engine={engine} setEngine={setEngine} name={name} setName={setName} site={site} setSite={setSite} showSite={!websiteId} error={createUser.error?.fields.name} />
            <fieldset className="checkbox-list" style={{ marginTop: 14 }}>
              <legend>Databases</legend>
              {state.databases.filter((d) => d.websiteId === site && d.engine === engine).map((d) => (
                <label key={d.id} className="check">
                  <input type="checkbox" checked={grant.includes(d.name)} onChange={(e) => setGrant(e.target.checked ? [...grant, d.name] : grant.filter((x) => x !== d.name))} />
                  <code>{d.name}</code>
                </label>
              ))}
            </fieldset>
          </form>
        </Modal>
      )}
      {password && (
        <Modal title="Copy the password" onClose={() => setPassword(null)} footer={<button type="button" className="btn btn-primary" onClick={() => setPassword(null)}>I copied the password</button>}>
          <p>The panel shows this password one time only.</p>
          <div className="copy-field">
            <code>{password}</code>
            <button type="button" className="btn" onClick={() => void navigator.clipboard?.writeText(password)}>Copy</button>
          </div>
        </Modal>
      )}
    </div>
  );
}

function DbFields(props: { engine: DatabaseEngine; setEngine: (e: DatabaseEngine) => void; name: string; setName: (n: string) => void; site: string; setSite: (s: string) => void; showSite: boolean; error?: string }) {
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
      <fieldset className="radio-cards" style={{ marginTop: 14 }}>
        <legend>Engine</legend>
        {(["mariadb", "postgresql"] as const).map((e) => (
          <label className="radio-card" key={e}>
            <input type="radio" name="engine" checked={props.engine === e} onChange={() => props.setEngine(e)} />
            <span><strong>{ENGINE[e]}</strong><span>{e === "mariadb" ? "MariaDB 11.8" : "PostgreSQL 18"}</span></span>
          </label>
        ))}
      </fieldset>
      <div style={{ marginTop: 14 }}>
        <Field label="Name" hint="Start with a letter. Use lower-case letters, digits and underscores." error={props.error}>
          {(p) => <input {...p} className="input" value={props.name} onChange={(e) => props.setName(e.target.value)} autoCapitalize="none" spellCheck={false} />}
        </Field>
      </div>
    </>
  );
}
