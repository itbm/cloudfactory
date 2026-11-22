import { useState } from "react";
import { useAction, useStore } from "../api/hooks";
import { moveWebsite } from "../api/store";
import type { Website } from "../api/types";
import { Field, FormError, Modal } from "./ui";

/**
 * Moves a website to a different server (administrators only). The files go through the
 * encrypted tunnel between the two servers. The website keeps its domains, its databases, its
 * SFTP keys, its cron jobs and its network name.
 */
export function MoveWebsiteDialog({ website, onClose }: { website: Website; onClose: () => void }) {
  const state = useStore();
  const targets = state.servers.filter((s) => s.id !== website.serverId && s.roles.includes("web")).sort((a, b) => a.name.localeCompare(b.name));
  const [serverId, setServerId] = useState(() => targets.find((s) => s.state === "healthy")?.id ?? targets[0]?.id ?? "");
  const move = useAction(moveWebsite);
  const from = state.servers.find((s) => s.id === website.serverId)?.name ?? "its server";
  const to = state.servers.find((s) => s.id === serverId);
  // The database servers of the website that are not on the new server. The website connects
  // to them through a network after the move.
  const serverIds = new Set(state.databases.filter((d) => d.websiteId === website.id && d.databaseServerId).map((d) => d.databaseServerId));
  const remote = state.databaseServers.filter((s) => serverIds.has(s.id) && s.serverId !== serverId).sort((a, b) => a.name.localeCompare(b.name));

  return (
    <Modal
      wide
      title={`Move ${website.name}`}
      onClose={onClose}
      footer={<><button type="button" className="btn" onClick={onClose}>Cancel</button><button type="submit" form="move-website" className="btn btn-primary" disabled={move.busy || !serverId}>Move website</button></>}
    >
      <form id="move-website" noValidate onSubmit={async (e) => {
        e.preventDefault();
        const r = await move.run(website.id, serverId);
        if (r.ok) onClose();
      }}>
        <FormError error={move.error} shown={["serverId"]} />
        <p>
          The panel moves the website from {from} to the new server. The files go through the encrypted tunnel between the two servers. The website keeps its domains, its databases, its SFTP keys, its cron jobs and its network name.
        </p>
        <Field label="New server" error={move.error?.fields.serverId}>
          {(p) => (
            <select {...p} className="select" value={serverId} onChange={(e) => setServerId(e.target.value)}>
              {targets.map((s) => <option key={s.id} value={s.id}>{s.name}{s.state !== "healthy" ? ` · ${s.state}` : ""}</option>)}
            </select>
          )}
        </Field>
        {targets.length === 0 && <p className="muted small">There is no other server with the web role.</p>}
        <div className="alert alert-warning alert-stack" style={{ marginTop: 14 }}>
          <p><strong>Before you move it</strong></p>
          <ul className="notice-list">
            <li>The website does not operate while its files move. The time depends on the size of the files: about one minute for each 5 GB on a 1 Gbit/s connection. A stopped website stays stopped.</li>
            {website.kind === "public" && (
              <li>
                The panel changes the address records of the domains in its DNS zones. For a domain at a different DNS service, change the A and AAAA records to the addresses of the new server
                {to && to.addresses.length > 0 ? <>: {to.addresses.map((a, i) => <span key={a}>{i > 0 && ", "}<code>{a}</code></span>)}</> : ""}. Set a short TTL some hours before the move.
              </li>
            )}
            {remote.length > 0 && (
              <li>
                The databases stay where they are. After the move, the website connects to {remote.length === 1 ? "this database server" : "these database servers"} with the network name: {remote.map((s, i) => <span key={s.id}>{i > 0 && ", "}<code>{s.networkHost ?? s.host}:{s.networkPort ?? s.port}</code></span>)}. An app that uses <code>mariadb.internal</code>, <code>postgres.internal</code> or <code>valkey.internal</code> loses the connection. Change it to the network name first.
              </li>
            )}
            {website.sftp?.enabled && <li>The SFTP host changes to the host of the new server.</li>}
          </ul>
        </div>
      </form>
    </Modal>
  );
}
