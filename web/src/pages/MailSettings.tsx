import { useState } from "react";
import { useAction } from "../api/hooks";
import { setServerMailSettings } from "../api/store";
import type { MailSettings, Server } from "../api/types";
import { Field, FormError, Modal } from "../components/ui";

/** The ports of a relay. 465 uses TLS from the start. The other ports use STARTTLS. */
const RELAY_PORTS: { port: number; label: string }[] = [
  { port: 587, label: "587 (STARTTLS)" },
  { port: 465, label: "465 (TLS)" },
  { port: 2525, label: "2525 (STARTTLS)" },
  { port: 25, label: "25 (STARTTLS)" },
];

const score = (n: number) => String(+n.toFixed(1));

/** The spam filter and the outgoing mail of a mail server, as text. */
export function spamText(s: MailSettings): string {
  if (!s.spamFilter) return "Off. The mail server delivers all mail to the inbox.";
  return `On. Junk folder at score ${score(s.spamScore)}.${s.rejectScore === null ? "" : ` Refused at score ${score(s.rejectScore)}.`}`;
}

export function relayText(s: MailSettings): string {
  if (!s.relay) return "Directly to the mail server of each recipient (port 25).";
  return `Through the relay ${s.relay.host}:${s.relay.port}${s.relay.username ? `, as ${s.relay.username}` : ", with no sign-in"}.`;
}

/**
 * The spam filter and the outbound relay of the mail server of a server. Only an
 * administrator sees the page of a server.
 */
export function MailSettingsPanel({ server, busy }: { server: Server; busy: boolean }) {
  const [editing, setEditing] = useState(false);
  const settings = server.mail?.settings;
  if (!settings) return null;
  return (
    <div style={{ marginTop: 18 }}>
      <div style={{ display: "flex", alignItems: "center", justifyContent: "space-between", gap: 12 }}>
        <h3 style={{ margin: 0 }}>Spam filter and outgoing mail</h3>
        <button type="button" className="btn btn-sm" disabled={busy} onClick={() => setEditing(true)}>Change</button>
      </div>
      <table className="kv" style={{ marginTop: 6 }}>
        <tbody>
          <tr><th scope="row">Spam filter</th><td>{spamText(settings)}</td></tr>
          <tr><th scope="row">Outgoing mail</th><td>{relayText(settings)}</td></tr>
        </tbody>
      </table>
      {editing && <MailSettingsDialog server={server} settings={settings} onClose={() => setEditing(false)} />}
    </div>
  );
}

function MailSettingsDialog({ server, settings, onClose }: { server: Server; settings: MailSettings; onClose: () => void }) {
  const [spamFilter, setSpamFilter] = useState(settings.spamFilter);
  const [spamScore, setSpamScore] = useState(score(settings.spamScore));
  const [reject, setReject] = useState(settings.rejectScore !== null);
  const [rejectScore, setRejectScore] = useState(score(settings.rejectScore ?? 15));
  const [useRelay, setUseRelay] = useState(settings.relay !== null);
  const [host, setHost] = useState(settings.relay?.host ?? "");
  const [port, setPort] = useState(settings.relay?.port ?? 587);
  const [username, setUsername] = useState(settings.relay?.username ?? "");
  const [password, setPassword] = useState("");
  const save = useAction(setServerMailSettings);
  const keepsPassword = settings.relay?.hasPassword === true && username.trim() === (settings.relay?.username ?? "");
  const fields = save.error?.fields ?? {};
  return (
    <Modal
      title="Spam filter and outgoing mail"
      onClose={onClose}
      wide
      footer={<><button type="button" className="btn" onClick={onClose}>Cancel</button><button type="submit" form="mail-settings" className="btn btn-primary" disabled={save.busy}>Save settings</button></>}
    >
      <form id="mail-settings" noValidate onSubmit={async (e) => {
        e.preventDefault();
        const user = username.trim();
        const r = await save.run(server.id, {
          spamFilter,
          spamScore: +spamScore,
          rejectScore: reject ? +rejectScore : null,
          relay: useRelay ? {
            host: host.trim().toLowerCase(),
            port,
            username: user === "" ? null : user,
            // An empty field keeps the password of the same user.
            ...(password !== "" || !keepsPassword ? { password: user === "" ? null : password } : {}),
          } : null,
        });
        if (r.ok) onClose();
      }}>
        <FormError error={save.error} shown={["spamFilter", "spamScore", "rejectScore", "relay.host", "relay.port", "relay.username", "relay.password"]} />
        <fieldset className="form-section" style={{ marginTop: 0 }}>
          <legend>Spam filter</legend>
          <label className="check" style={{ display: "flex", gap: 8, alignItems: "flex-start" }}>
            <input type="checkbox" checked={spamFilter} onChange={(e) => setSpamFilter(e.target.checked)} style={{ marginTop: 3, flex: "none" }} />
            <span>
              <strong>Filter spam</strong>
              <span className="muted small" style={{ display: "block" }}>The mail server gives each new message a spam score. The rules of the filter come with the image of the mail server.</span>
            </span>
          </label>
          {spamFilter && (
            <div className="form-grid" style={{ marginTop: 14 }}>
              <Field label="Junk folder at score" hint="A message with this score or more goes to the junk folder of the mailbox. A lower score finds more spam, and also more good mail. The default is 5." error={fields.spamScore}>
                {(p) => <input {...p} className="input" type="number" min={1} max={50} step={0.5} value={spamScore} onChange={(e) => setSpamScore(e.target.value)} />}
              </Field>
              <div>
                <label className="check" style={{ display: "flex", gap: 8, alignItems: "center", marginTop: 4 }}>
                  <input type="checkbox" checked={reject} onChange={(e) => setReject(e.target.checked)} />
                  <span>Refuse a message with a high score</span>
                </label>
                {reject && (
                  <div style={{ marginTop: 8 }}>
                    <Field label="Refuse at score" hint="The mail server refuses the message, and the sender gets an error. Nobody can find a refused message later." error={fields.rejectScore}>
                      {(p) => <input {...p} className="input" type="number" min={2} max={100} step={0.5} value={rejectScore} onChange={(e) => setRejectScore(e.target.value)} />}
                    </Field>
                  </div>
                )}
              </div>
            </div>
          )}
        </fieldset>
        <fieldset className="form-section">
          <legend>Outgoing mail</legend>
          <label className="check" style={{ display: "flex", gap: 8, alignItems: "flex-start" }}>
            <input type="radio" name="relay" checked={!useRelay} onChange={() => setUseRelay(false)} style={{ marginTop: 3, flex: "none" }} />
            <span>
              <strong>Send directly</strong>
              <span className="muted small" style={{ display: "block" }}>The mail server sends each message to the mail server of the recipient on port 25.</span>
            </span>
          </label>
          <label className="check" style={{ display: "flex", gap: 8, alignItems: "flex-start", marginTop: 10 }}>
            <input type="radio" name="relay" checked={useRelay} onChange={() => setUseRelay(true)} style={{ marginTop: 3, flex: "none" }} />
            <span>
              <strong>Send through a relay</strong>
              <span className="muted small" style={{ display: "block" }}>Use a relay when the provider of the server blocks port 25, or when the address of the server has a bad reputation. Mail for the domains of this server does not go through the relay.</span>
            </span>
          </label>
          {useRelay && (
            <div className="form-grid" style={{ marginTop: 14 }}>
              <Field label="Host name of the relay" hint="It must have a public address." error={fields["relay.host"]}>
                {(p) => <input {...p} className="input" value={host} onChange={(e) => setHost(e.target.value)} autoCapitalize="none" spellCheck={false} placeholder="smtp.relay.example.net" />}
              </Field>
              <Field label="Port" error={fields["relay.port"]}>
                {(p) => (
                  <select {...p} className="input" value={port} onChange={(e) => setPort(+e.target.value)}>
                    {RELAY_PORTS.map((r) => <option key={r.port} value={r.port}>{r.label}</option>)}
                  </select>
                )}
              </Field>
              <Field label="User name" hint="Leave empty when the relay accepts the mail with no sign-in." error={fields["relay.username"]}>
                {(p) => <input {...p} className="input" value={username} onChange={(e) => setUsername(e.target.value)} autoCapitalize="none" autoComplete="off" spellCheck={false} />}
              </Field>
              <Field label="Password" hint={keepsPassword ? "Leave empty to keep the password." : "The panel keeps the password encrypted, and does not show it again."} error={fields["relay.password"]}>
                {(p) => <input {...p} className="input" type="password" autoComplete="new-password" value={password} disabled={username.trim() === ""} onChange={(e) => setPassword(e.target.value)} placeholder={keepsPassword ? "••••••••" : ""} />}
              </Field>
            </div>
          )}
        </fieldset>
      </form>
    </Modal>
  );
}
