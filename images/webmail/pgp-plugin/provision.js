// CloudFactory addition to the PGP plugin "PGP True End-to-End" (GPL-3.0-or-later, as the
// plugin). The build of the webmail image puts this code in the plugin with patch.mjs.
//
// The panel makes the OpenPGP keys of a mailbox, and protects each private key with the
// password of the mailbox. The agent writes the keys to the file OpenPGP/private-key.asc in
// the files of the mailbox. When the browser has no key for the mailbox, this code asks for
// the password of the mailbox one time, reads the file with that password through JMAP on the
// same host name, adds the keys to the plugin and unlocks them. The user uploads no file.
async function cfProvisionKeys(api) {
  if (await getDefaultKeyRecord()) return false;
  let self;
  try {
    self = await api.user.getSelf();
  } catch {
    return false;
  }
  const address = self && self.email;
  if (!address) return false;
  // The password is asked only for a mailbox with keys of the panel: the panel adds its public
  // key to the mail server with the description "CloudFactory". The plugin reads the public
  // keys with the session of the webmail.
  // The keys can be not ready just after the start of the webmail: the code tries again.
  let panelKeys = [];
  for (let attempt = 0; attempt < 5 && panelKeys.length === 0; attempt++) {
    if (attempt > 0) await new Promise((resolve) => setTimeout(resolve, 2000));
    try {
      const all = (await api.crypto.getPublicKeys()) || [];
      panelKeys = all.filter((k) => k && k.description === "CloudFactory");
    } catch (err) {
      api.log.info("CloudFactory: no public keys: " + (err && err.message));
    }
  }
  if (panelKeys.length === 0) return false;
  // A mailbox with a public key of the user has no key file. The plugin then does not ask again
  // for the same key.
  const marker = "cf-no-key-file:" + panelKeys.map((k) => k.id).sort().join(",");
  if (await api.storage.get(marker)) return false;
  const find = async (authorization) => {
    const session = await fetch("/jmap/session", { headers: { Authorization: authorization }, credentials: "omit" });
    if (session.status === 401) return { refused: true };
    if (!session.ok) throw new Error("JMAP session: status " + session.status);
    const s = await session.json();
    const account = (s.primaryAccounts && (s.primaryAccounts["urn:ietf:params:jmap:core"] || s.primaryAccounts["urn:ietf:params:jmap:mail"])) || Object.keys(s.accounts || {})[0];
    const using = ["urn:ietf:params:jmap:core", "urn:ietf:params:jmap:filenode"];
    const res = await fetch(s.apiUrl, {
      method: "POST",
      headers: { Authorization: authorization, "Content-Type": "application/json" },
      credentials: "omit",
      body: JSON.stringify({ using, methodCalls: [
        ["FileNode/query", { accountId: account, filter: { name: "private-key.asc" } }, "q"],
        ["FileNode/get", { accountId: account, "#ids": { resultOf: "q", name: "FileNode/query", path: "/ids" }, properties: ["id", "parentId", "name", "blobId"] }, "g"],
        ["FileNode/query", { accountId: account, filter: { name: "OpenPGP" } }, "f"]
      ] })
    });
    if (!res.ok) throw new Error("JMAP: status " + res.status);
    const answer = await res.json();
    const responses = Object.fromEntries((answer.methodResponses || []).map((r) => [r[2], r[1]]));
    const folders = new Set((responses.f && responses.f.ids) || []);
    const file = ((responses.g && responses.g.list) || []).find((n) => folders.has(n.parentId));
    if (!file || !file.blobId) return { text: "" };
    const url = s.downloadUrl.replace("{accountId}", encodeURIComponent(account)).replace("{blobId}", encodeURIComponent(file.blobId))
      .replace("{name}", "private-key.asc").replace("{type}", encodeURIComponent("application/pgp-keys"));
    const blob = await fetch(url, { headers: { Authorization: authorization }, credentials: "omit" });
    if (!blob.ok) throw new Error("JMAP download: status " + blob.status);
    return { text: await blob.text() };
  };
  // Without the password, the file cannot be read: first ask whether the mailbox has keys.
  let message = "Your mailbox has OpenPGP keys for encrypted mail. Type the password of your mailbox (" + address + ") to use them in this browser.";
  for (let attempt = 0; attempt < 3; attempt++) {
    const result = await api.ui.prompt({
      title: "Use your encryption keys",
      message,
      fields: [{ name: "password", label: "Password of the mailbox", type: "password", required: true }]
    });
    if (!result || !result.password) return false;
    const password = result.password;
    let found;
    try {
      found = await find("Basic " + btoa(unescape(encodeURIComponent(address + ":" + password))));
    } catch (err) {
      api.log.error("CloudFactory: cannot read the key file", err);
      api.toast.error("The webmail cannot read your encryption keys now. Try again later.");
      return false;
    }
    if (found.refused) {
      message = "This password is not correct. Type the password of your mailbox (" + address + ").";
      continue;
    }
    const blocks = (found.text.match(/-----BEGIN PGP PRIVATE KEY BLOCK-----[\s\S]*?-----END PGP PRIVATE KEY BLOCK-----/g)) || [];
    if (blocks.length === 0) {
      await api.storage.set(marker, true);
      return false;
    }
    // The first block is the newest key. An old key can have an old password: it is left out.
    let unlocked = false;
    for (const block of blocks) {
      let record;
      try {
        record = (await importOpenPgpPrivateKey(block, password, password)).keyRecord;
      } catch (err) {
        api.log.info("CloudFactory: a key of the file does not open with the current password");
        continue;
      }
      const first = !unlocked;
      await saveKeyRecord({ ...record, default: first, main: first });
      const session = await unlockPrivateKey(record, password);
      broadcastUnlockKey({
        id: record.id,
        unlockedPrivateKey: session.unlockedPrivateKey,
        signingKey: session.signingKey,
        decryptionKey: session.decryptionKey,
        aesKey: session.aesKey,
        hmacKey: session.hmacKey
      });
      unlocked = true;
    }
    if (!unlocked) {
      api.toast.error("Your encryption keys have an older password. Download them in the panel and add them in the settings of the plugin.");
      return false;
    }
    api.toast.success("Your encryption keys are ready in this browser.");
    api.ui.rerenderFetchedEmails();
    return true;
  }
  return false;
}
