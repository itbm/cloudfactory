import { useState } from "react";
import { useAction, useStore } from "../api/hooks";
import { contextCustomers, inContext } from "../api/selectors";
import { createNetwork, deleteNetwork, saveNetwork } from "../api/store";
import type { PrivateNetwork, State } from "../api/types";
import { Topbar } from "../components/Layout";
import { Card, Field, FormError, Modal, PageHeader, RowMenu, Status, TableWrap } from "../components/ui";
import { engineName, WEBSITE_KIND } from "../lib/labels";

/**
 * The private networks of the customers, like a VPC. Each website in a network connects to
 * each other member of the network by its network name, on the same server and on a different
 * server. Each customer has the network "default". The plan of the customer gives the number
 * of named networks.
 */
export function NetworksPage() {
  const state = useStore();
  const customers = contextCustomers(state);
  const networks = state.networks.filter((n) => inContext(state, n.customerId));
  const [creating, setCreating] = useState(false);
  const [editing, setEditing] = useState<PrivateNetwork | null>(null);
  const [removing, setRemoving] = useState<PrivateNetwork | null>(null);
  const remove = useAction(deleteNetwork);
  const customer = (id: string) => state.customers.find((c) => c.id === id);
  const showCustomer = customers.length > 1 || networks.some((n) => n.customerId !== customers[0]?.id);
  // With one customer, the card tells how many named networks its plan allows.
  const only = customers.length === 1 ? customers[0] : undefined;
  const allowed = only ? state.plans.find((p) => p.id === only.planId)?.networks ?? 0 : undefined;
  const named = (customerId: string) => state.networks.filter((n) => n.customerId === customerId && !n.isDefault).length;
  // A user with one customer and a plan with no named networks has no button.
  const canCreate = customers.length > 0 && (allowed === undefined || allowed > 0);

  return (
    <>
      <Topbar />
      <main id="main" className="content content-wide">
        <PageHeader
          title="Private networks"
          description="A private network connects websites and database servers, on one server or on many servers. The traffic between two servers is encrypted. Nothing in a private network is open to the internet."
          actions={canCreate && <button type="button" className="btn btn-primary" onClick={() => setCreating(true)}>New network</button>}
        />
        <div className="stack">
          <Card title="Networks">
            <div className="card-body" style={{ paddingBottom: 12 }}>
              <p className="muted">
                A website connects to each other member of each network that it is in. Use the network name of the member, for example <code>shop-db.internal</code>. Members of different networks cannot connect.
                {only && allowed !== undefined && (allowed > 0
                  ? <> {only.name} has {named(only.id)} of {allowed} named networks.</>
                  : <> The plan of {only.name} has only the network "default".</>)}
              </p>
              <FormError error={remove.error} />
            </div>
            <TableWrap>
              <table>
                <thead>
                  <tr>
                    <th scope="col">Network</th>
                    <th scope="col">Websites</th>
                    <th scope="col">Database servers</th>
                    <th scope="col"><span className="visually-hidden">Actions</span></th>
                  </tr>
                </thead>
                <tbody>
                  {networks.map((n) => (
                    <tr key={n.id}>
                      <td>
                        <span className="cell-title">{n.name}</span>
                        {n.isDefault && <Status tone="neutral">Default</Status>}
                        {showCustomer && <span className="cell-sub">{customer(n.customerId)?.name ?? "Removed customer"}</span>}
                      </td>
                      <td><Members names={websiteNames(state, n)} none="No websites" /></td>
                      <td><Members names={databaseServerNames(state, n)} none="No database servers" /></td>
                      <td className="cell-actions">
                        <RowMenu label={`Actions for the network ${n.name}`} actions={[
                          { label: n.isDefault ? "Change members" : "Change", onSelect: () => setEditing(n) },
                          ...(n.isDefault ? [] : [{ label: "Remove", danger: true, onSelect: () => setRemoving(n) }]),
                        ]} />
                      </td>
                    </tr>
                  ))}
                  {networks.length === 0 && <tr><td colSpan={4} className="muted">No networks.</td></tr>}
                </tbody>
              </table>
            </TableWrap>
          </Card>
        </div>
      </main>
      {creating && <NetworkDialog onClose={() => setCreating(false)} />}
      {editing && <NetworkDialog network={editing} onClose={() => setEditing(null)} />}
      {removing && (
        <Modal
          title={`Remove network ${removing.name}?`}
          onClose={() => setRemoving(null)}
          footer={<><button type="button" className="btn" onClick={() => setRemoving(null)}>Cancel</button><button type="button" className="btn btn-danger-solid" onClick={() => { const id = removing.id; setRemoving(null); void remove.run(id); }}>Remove network</button></>}
        >
          <p>The websites and the database servers of the network stay. They lose the connections that only this network gives. The connections of their other networks continue.</p>
        </Modal>
      )}
    </>
  );
}

const websiteNames = (state: State, n: PrivateNetwork) => n.websiteIds.map((id) => state.websites.find((w) => w.id === id)).filter((w) => w !== undefined).map((w) => w.networkHost ?? w.name);
const databaseServerNames = (state: State, n: PrivateNetwork) => n.databaseServerIds.map((id) => state.databaseServers.find((d) => d.id === id)).filter((d) => d !== undefined).map((d) => d.networkHost ?? d.name);

/** The number of members, and the network names of the first members. */
function Members({ names, none }: { names: string[]; none: string }) {
  if (names.length === 0) return <span className="muted">{none}</span>;
  const first = names.slice(0, 4);
  return (
    <>
      {names.length}
      <span className="cell-sub">{first.join(", ")}{names.length > first.length && ` and ${names.length - first.length} more`}</span>
    </>
  );
}

/** Makes a named network, or changes the name and the members of a network. */
function NetworkDialog({ network, onClose }: { network?: PrivateNetwork; onClose: () => void }) {
  const state = useStore();
  const customers = contextCustomers(state);
  const [customerId, setCustomerId] = useState(() => network?.customerId ?? customers.find((c) => c.id === state.session.contextId)?.id ?? customers[0]?.id ?? "");
  const [name, setName] = useState(network?.name ?? "");
  const [websiteIds, setWebsiteIds] = useState<string[]>(network?.websiteIds ?? []);
  const [databaseServerIds, setDatabaseServerIds] = useState<string[]>(network?.databaseServerIds ?? []);
  const create = useAction(createNetwork);
  const save = useAction(saveNetwork);
  const error = create.error ?? save.error;
  const customer = customers.find((c) => c.id === customerId);
  const websites = state.websites.filter((w) => w.customerId === customerId);
  const databaseServers = state.databaseServers.filter((d) => d.customerId === customerId);
  const own = state.networks.filter((n) => n.customerId === customerId && !n.isDefault).length;
  const allowed = state.plans.find((p) => p.id === customer?.planId)?.networks;
  const full = !network && allowed !== undefined && own >= allowed;
  const toggle = (list: string[], id: string, on: boolean) => (on ? [...list, id] : list.filter((x) => x !== id));

  return (
    <Modal
      wide
      title={network ? `Network ${network.name}` : "New network"}
      onClose={onClose}
      footer={<><button type="button" className="btn" onClick={onClose}>Cancel</button><button type="submit" form="network" className="btn btn-primary" disabled={create.busy || save.busy}>{network ? "Save network" : "Create network"}</button></>}
    >
      <form id="network" noValidate onSubmit={async (e) => {
        e.preventDefault();
        const members = { websiteIds, databaseServerIds };
        const r = network ? await save.run(network.id, network.isDefault ? undefined : name, members) : await create.run(name, customerId, members);
        if (r.ok) onClose();
      }}>
        <FormError error={error} shown={["name", "customerId"]} />
        {!network && customers.length > 1 && (
          <Field label="Customer" error={error?.fields.customerId}>
            {(p) => (
              <select {...p} className="select" value={customerId} onChange={(e) => { setCustomerId(e.target.value); setWebsiteIds([]); setDatabaseServerIds([]); }}>
                {customers.map((c) => <option key={c.id} value={c.id}>{c.name}</option>)}
              </select>
            )}
          </Field>
        )}
        {!network && customer && allowed !== undefined && (
          <p className={full ? "text-danger small" : "muted small"} style={{ margin: "8px 0 14px" }}>
            {customer.name} has {own} of {allowed} named networks.{full && " The plan of the customer allows no more."}
          </p>
        )}
        {!network?.isDefault && (
          <Field label="Name" hint="Lower-case letters, digits and hyphens, for example backend." error={error?.fields.name}>
            {(p) => <input {...p} className="input" value={name} onChange={(e) => setName(e.target.value)} placeholder="backend" autoCapitalize="none" spellCheck={false} maxLength={40} />}
          </Field>
        )}
        <fieldset className="checkbox-list checkbox-list-wide" style={{ marginTop: 14 }}>
          <legend>Websites</legend>
          {websites.map((w) => (
            <label key={w.id} className="check">
              <input type="checkbox" checked={websiteIds.includes(w.id)} onChange={(e) => setWebsiteIds(toggle(websiteIds, w.id, e.target.checked))} />
              <span>{w.name}{w.kind !== "public" && <span className="muted"> · {WEBSITE_KIND[w.kind]}</span>}{w.networkHost && <> · <code>{w.networkHost}</code></>}</span>
            </label>
          ))}
          {websites.length === 0 && <p className="muted small">The customer has no websites.</p>}
        </fieldset>
        {error?.fields.websiteIds && <p className="text-danger small">{error.fields.websiteIds}</p>}
        <fieldset className="checkbox-list checkbox-list-wide" style={{ marginTop: 14 }}>
          <legend>Database servers</legend>
          {databaseServers.map((d) => (
            <label key={d.id} className="check">
              <input type="checkbox" checked={databaseServerIds.includes(d.id)} onChange={(e) => setDatabaseServerIds(toggle(databaseServerIds, d.id, e.target.checked))} />
              <span>{d.name}<span className="muted"> · {engineName(state.databaseEngines, d.engine)}</span>{d.networkHost && <> · <code>{d.networkHost}:{d.networkPort}</code></>}</span>
            </label>
          ))}
          {databaseServers.length === 0 && <p className="muted small">The customer has no database servers.</p>}
        </fieldset>
        {error?.fields.databaseServerIds && <p className="text-danger small">{error.fields.databaseServerIds}</p>}
        <p className="muted small" style={{ marginTop: 14 }}>
          A website or a database server can be in more than one network. The panel sends the change to the servers immediately. No website restarts.
        </p>
      </form>
    </Modal>
  );
}
