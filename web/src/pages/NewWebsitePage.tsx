import { useState } from "react";
import { Link, useNavigate } from "react-router-dom";
import { useAction, useStore } from "../api/hooks";
import { ApiError, createWebsite, makeCustomer } from "../api/store";
import type { WordPressAdmin } from "../api/types";
import { contextCustomers } from "../api/selectors";
import type { RuntimeImage, WebsiteKind } from "../api/types";
import { Topbar } from "../components/Layout";
import { Card, Field, FormError, PageHeader } from "../components/ui";
import { bytes } from "../lib/format";
import { AdminCredentials, emptyInstall, WordPressFields } from "./website/WordPressTab";
import { PHP_FAMILIES } from "../lib/labels";

/** The value of the customer field for a customer that does not exist yet. */
const NEW_CUSTOMER = "new";

/** The kinds of a website. A private website and a worker have no public address. */
const KINDS: { id: WebsiteKind; title: string; text: string }[] = [
  { id: "public", title: "Public website", text: "Visitors open it on its domain. The panel gets a TLS certificate." },
  { id: "private", title: "Private website", text: "No domain and no public address. Only the other websites of the customer can connect to it, by its network name." },
  { id: "worker", title: "Background worker", text: "Runs one command, for example a queue worker. It serves no HTTP. It can connect to the websites and the databases of the customer." },
];

const APPLICATIONS = ["WordPress", "WooCommerce", "Laravel", "Symfony", "Custom PHP", "Express", "Next.js", "Node.js app", "Flask", "Django", "FastAPI", "Python app", "Static site"];

/** The usual image family for each application. */
const FAMILY: Record<string, RuntimeImage["family"]> = {
  "Static site": "edge", "Custom PHP": "apache-php-fpm",
  Express: "node", "Next.js": "node", "Node.js app": "node",
  Flask: "python", Django: "python", FastAPI: "python", "Python app": "python",
};

/**
 * The start command and the install command of each Node.js or Python application. An
 * application that is not here gets the defaults of the image. The container gives the port in
 * the variable PORT.
 */
const PRESETS: Record<string, { command?: string; install?: string }> = {
  "Next.js": { command: "npm start", install: "npm install && npm run build" },
  Flask: { command: "gunicorn --bind 0.0.0.0:$PORT app:app" },
  Django: { command: "gunicorn --bind 0.0.0.0:$PORT mysite.wsgi" },
  FastAPI: { command: "uvicorn main:app --host 0.0.0.0 --port $PORT" },
};

/**
 * The runtimes of one kind, for example all the versions of Node.js. An own image is a group of
 * one. The images of a runtime from a manifest of the agent, for example Go, are one group.
 */
interface RuntimeGroup { key: string; title: string; images: RuntimeImage[] }

const GROUP_TITLE: Partial<Record<RuntimeImage["family"], string>> = {
  frankenphp: "FrankenPHP", "apache-php-fpm": "Apache + PHP-FPM", node: "Node.js", python: "Python", edge: "Static files",
};

/** The version of the language of an image, for example "PHP 8.5" or "24". */
const versionOf = (i: RuntimeImage) => (i.phpVersion ? `PHP ${i.phpVersion}` : i.version ?? i.name);

/** Puts the images in groups, so that the page shows one option for each runtime and then its versions. */
export function runtimeGroups(images: RuntimeImage[]): RuntimeGroup[] {
  const groups: RuntimeGroup[] = [];
  for (const i of images) {
    const key = i.custom || !(GROUP_TITLE[i.family] || i.pack) ? i.id : i.family;
    const group = groups.find((g) => g.key === key);
    if (group) group.images.push(i);
    else groups.push({ key, title: key === i.id ? i.name : GROUP_TITLE[i.family] ?? runtimeTitle(i), images: [i] });
  }
  // The order of GROUP_TITLE, then the own images.
  const order = Object.keys(GROUP_TITLE);
  const rank = (g: RuntimeGroup) => (order.includes(g.key) ? order.indexOf(g.key) : order.length);
  groups.sort((a, b) => rank(a) - rank(b));
  for (const g of groups) g.images.sort((a, b) => versionOf(a).localeCompare(versionOf(b), undefined, { numeric: true }));
  return groups;
}

/** The title of a runtime from a manifest: the name of its image without the version, for example "Go". */
const runtimeTitle = (i: RuntimeImage) => i.name.replace(/\s+v?\d[\w.-]*$/, "") || i.family;

/**
 * True for an image that runs the start command of the website: Node.js, Python, or a runtime
 * from a pack. An image of a PHP pack is a PHP image: it serves the PHP files of the website.
 */
export const isAppImage = (i: RuntimeImage | undefined) => !!i && !i.custom && (i.family === "node" || i.family === "python" || (!!i.pack && !PHP_FAMILIES.includes(i.family)));

/** The image of a family with the newest PHP version that still has support, or the first image. */
export function defaultImage(images: RuntimeImage[], family: RuntimeImage["family"]): string {
  const ofFamily = images.filter((i) => i.family === family);
  const pool = ofFamily.length ? ofFamily : images.filter((i) => i.family === "frankenphp");
  // Compare as numbers, so that PHP 8.10 is newer than 8.5 and Python 3.14 is newer than 3.9.
  const sorted = [...pool].sort((a, b) => (b.phpVersion ?? b.version ?? "").localeCompare(a.phpVersion ?? a.version ?? "", undefined, { numeric: true }));
  return (sorted.find((i) => !i.supportEndsAt || new Date(i.supportEndsAt) > new Date()) ?? sorted[0] ?? images[0])?.id ?? "";
}

export function NewWebsitePage() {
  const state = useStore();
  const navigate = useNavigate();
  const runtimes = state.images.filter((i) => ["frankenphp", "apache-php-fpm", "node", "python", "custom", "edge"].includes(i.family) || (i.pack && !i.custom));
  const webServers = state.servers.filter((s) => s.roles.includes("web"));
  const [name, setName] = useState("");
  const [kind, setKind] = useState<WebsiteKind>("public");
  const [command, setCommand] = useState("");
  const [application, setApplication] = useState("WordPress");
  const platform = state.session.contextId === "platform";
  // CF-07: only an administrator can select the plan here. Other users get the plan of the account.
  const choosePlan = platform || state.session.user.role === "administrator";
  const [imageId, setImageId] = useState(() => defaultImage(runtimes, "frankenphp"));
  const image = runtimes.find((i) => i.id === imageId);
  const groups = runtimeGroups(runtimes);
  const group = groups.find((g) => g.images.some((i) => i.id === imageId));
  const app = isAppImage(image);
  // The commands of a Node.js or Python website. Null: the preset of the application or the default of the image.
  const [appCommand, setAppCommand] = useState<string | null>(null);
  const [install, setInstall] = useState<string | null>(null);
  // The presets are for Node.js and Python. A runtime from a manifest has the commands of its image.
  const preset = image?.pack ? undefined : PRESETS[application];
  const startValue = appCommand ?? preset?.command ?? image?.defaults?.command ?? "";
  const installValue = install ?? preset?.install ?? image?.defaults?.install ?? "";
  // Batch 3, issue 2: a reseller makes websites for itself and for its customers.
  const customers = contextCustomers(state);
  // A website always has a customer. An administrator can make the customer here, so that a
  // new panel with no customers does not need a different page first.
  const [customerId, setCustomerId] = useState(platform ? customers.find((c) => c.role !== "reseller")?.id ?? customers[0]?.id ?? NEW_CUSTOMER : state.session.contextId);
  const [customer, setCustomer] = useState({ name: "", email: "" });
  const newCustomer = platform && customerId === NEW_CUSTOMER;
  const reseller = state.session.user.role === "reseller";
  const [planId, setPlanId] = useState(() => state.customers.find((c) => c.id === customerId)?.planId ?? state.plans[0]?.id ?? "");
  const [serverId, setServerId] = useState(webServers[0]?.id ?? "");
  // FR-206: the panel installs WordPress in a new public PHP website.
  const wordpressApp = application === "WordPress" || application === "WooCommerce";
  const php = image?.family === "frankenphp" || image?.family === "apache-php-fpm";
  const canInstall = kind === "public" && wordpressApp && php;
  const [installWordPress, setInstallWordPress] = useState(true);
  const [wordpress, setWordpress] = useState(emptyInstall);
  const [created, setCreated] = useState<{ websiteId: string; admin: WordPressAdmin } | null>(null);
  const action = useAction(async () => {
    let owner = customerId;
    if (newCustomer) {
      try {
        owner = (await makeCustomer({ name: customer.name, email: customer.email, planId, role: "owner" })).customerId;
      } catch (e) {
        // The website has a "name" field also, so the fields of the customer get different names.
        if (e instanceof ApiError) throw new ApiError(e.message, { ...(e.fields.name ? { customerName: e.fields.name } : {}), ...(e.fields.email ? { customerEmail: e.fields.email } : {}), ...(e.fields.planId ? { planId: e.fields.planId } : {}) });
        throw e;
      }
      // If the website has an error, the next attempt uses this customer and does not make a second one.
      setCustomerId(owner);
    }
    return createWebsite({
      name, application, imageId, planId, serverId, customerId: owner, kind, command: app ? startValue : command, installCommand: app ? installValue : undefined, app,
      wordpress: canInstall && installWordPress ? wordpress : undefined,
    });
  });

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    const result = await action.run();
    if (!result.ok) return;
    // The API gives the password of the WordPress administrator one time.
    if (result.value.wordpress) setCreated({ websiteId: result.value.websiteId, admin: result.value.wordpress });
    else navigate(`/websites/${result.value.websiteId}`);
  };

  const err = action.error?.fields ?? {};

  return (
    <>
      <Topbar />
      <main id="main" className="content" style={{ maxWidth: 880 }}>
        <ol className="breadcrumbs">
          <li><Link to="/websites">Websites</Link></li>
          <li aria-current="page">New website</li>
        </ol>
        <PageHeader title="New website" description={`The panel makes a Unix user, a container network and a container in the gVisor sandbox. ${kind === "public" ? "Then it gets a TLS certificate. " : ""}This takes less than 60 seconds.`} />
        <form onSubmit={submit} noValidate>
          <Card title="Website">
            <div className="card-body">
              <FormError error={action.error} />
              <fieldset className="radio-cards" style={{ marginBottom: 14 }}>
                <legend>Type</legend>
                {KINDS.map((k) => (
                  <label key={k.id} className="radio-card">
                    <input type="radio" name="kind" value={k.id} checked={kind === k.id} onChange={() => setKind(k.id)} />
                    <span>
                      <strong>{k.title}</strong>
                      <span>{k.text}</span>
                    </span>
                  </label>
                ))}
              </fieldset>
              {err.kind && <p className="field-error" role="alert">{err.kind}</p>}
              {kind === "public" ? (
                <Field label="Primary domain" hint="You can add redirects and aliases later." error={err.name}>
                  {(p) => <input {...p} className="input" value={name} onChange={(e) => setName(e.target.value)} placeholder="example.com" autoComplete="off" autoCapitalize="none" spellCheck={false} />}
                </Field>
              ) : (
                <Field label="Name" hint={<>Lower-case letters, digits and <code>-</code>. The panel makes the network name from the first 8 characters and 4 random characters, for example <code>orders-a1b2.internal</code>.</>} error={err.name}>
                  {(p) => <input {...p} className="input" value={name} onChange={(e) => setName(e.target.value)} placeholder={kind === "worker" ? "queue" : "orders"} autoComplete="off" autoCapitalize="none" spellCheck={false} />}
                </Field>
              )}
              {kind === "worker" && !app && (
                <Field label="Start command" hint={<>One command line. It runs in the container of the worker, in the gVisor sandbox, in the directory <code>/app</code>. When the command stops, the panel starts it again.</>} error={err.command}>
                  {(p) => <input {...p} className="input mono" value={command} onChange={(e) => setCommand(e.target.value)} placeholder="php artisan queue:work" autoComplete="off" autoCapitalize="none" spellCheck={false} />}
                </Field>
              )}
              <Field label="Application">
                {(p) => (
                  <select {...p} className="select" value={application} onChange={(e) => {
                    setApplication(e.target.value);
                    setImageId(defaultImage(runtimes, FAMILY[e.target.value] ?? "frankenphp"));
                    setAppCommand(null);
                    setInstall(null);
                  }}>
                    {APPLICATIONS.map((a) => <option key={a}>{a}</option>)}
                  </select>
                )}
              </Field>
              {(customers.length > 1 || platform) && (
                <Field label="Customer" hint={platform ? "Each website belongs to a customer. The plan of the customer sets the limits." : undefined} error={err.customerId}>
                  {(p) => (
                    <select {...p} className="select" value={customerId} onChange={(e) => {
                      setCustomerId(e.target.value);
                      const plan = state.customers.find((c) => c.id === e.target.value)?.planId;
                      if (plan) setPlanId(plan);
                    }}>
                      {customers.map((c) => <option key={c.id} value={c.id}>{c.id === state.session.contextId && !platform ? `${c.name} (this account)` : c.name}</option>)}
                      {platform && <option value={NEW_CUSTOMER}>New customer…</option>}
                    </select>
                  )}
                </Field>
              )}
              {newCustomer && (
                <div className="form-row" style={{ marginTop: 14 }}>
                  <Field label="Name of the new customer" error={err.customerName}>
                    {(p) => <input {...p} className="input" value={customer.name} onChange={(e) => setCustomer({ ...customer, name: e.target.value })} autoComplete="off" />}
                  </Field>
                  <Field label="Email of the new customer" hint="The contact address. To let the customer sign in, add a user on the Customers page." error={err.customerEmail}>
                    {(p) => <input {...p} className="input" type="email" value={customer.email} onChange={(e) => setCustomer({ ...customer, email: e.target.value })} autoComplete="off" />}
                  </Field>
                </div>
              )}
            </div>
          </Card>
          <Card title="Runtime">
            <div className="card-body">
              <div className="runtime-picker">
                <fieldset className="radio-cards radio-cards-compact">
                  <legend className="visually-hidden">Runtime</legend>
                  {groups.map((g) => (
                    <label key={g.key} className="radio-card">
                      <input type="radio" name="runtime" checked={g.key === group?.key} onChange={() => setImageId(defaultImage(g.images, g.images[0].family))} />
                      <strong>{g.title}</strong>
                    </label>
                  ))}
                </fieldset>
                {group && group.images.length > 1 && (
                  <fieldset className="radio-cards radio-cards-compact">
                    <legend>Version</legend>
                    {group.images.map((i) => (
                      <label key={i.id} className="radio-card">
                        <input type="radio" name="version" value={i.id} checked={imageId === i.id} onChange={() => setImageId(i.id)} aria-label={i.name} />
                        <strong>{versionOf(i)}</strong>
                      </label>
                    ))}
                  </fieldset>
                )}
              </div>
              {image?.supportEndsAt && <p className="hint" style={{ marginTop: 10 }}>Security support for {image.name} ends in {new Date(image.supportEndsAt).toLocaleDateString("en-GB", { month: "long", year: "numeric" })}.</p>}
              {image?.pack && PHP_FAMILIES.includes(image.family) && <p className="hint">This version of PHP comes from the pack {image.pack.name}. Use it to move a website that needs it, then change to a newer version.</p>}
              {app && (
                <div style={{ marginTop: 16 }}>
                  <Field label="Start command" hint={<>One command line. It runs in <code>/app</code>, in the gVisor sandbox. {kind === "worker" ? "" : <>The app must listen on the port in the variable <code>PORT</code> (8080). </>}When the command stops, the panel starts it again.</>} error={err.command}>
                    {(p) => <input {...p} className="input mono" value={startValue} onChange={(e) => setAppCommand(e.target.value)} autoComplete="off" autoCapitalize="none" spellCheck={false} />}
                  </Field>
                  <Field label="Install command" hint={<>Runs in a build container before the app starts, after each upload with <strong>Install dependencies</strong> and before each Git deploy. Leave it empty for no install step.</>} error={err.installCommand}>
                    {(p) => <input {...p} className="input mono" value={installValue} onChange={(e) => setInstall(e.target.value)} autoComplete="off" autoCapitalize="none" spellCheck={false} />}
                  </Field>
                </div>
              )}
              <p className="hint" style={{ marginTop: 10 }}>Each image is signed and scanned. The container runs in gVisor.</p>
            </div>
          </Card>
          {canInstall && (
            <Card title="WordPress">
              <div className="card-body">
                <label className="check">
                  <input type="checkbox" checked={installWordPress} onChange={(e) => setInstallWordPress(e.target.checked)} />
                  <span>Install WordPress</span>
                </label>
                <p className="hint" style={{ marginBottom: installWordPress ? 14 : 0 }}>
                  {installWordPress
                    ? <>The panel makes a MariaDB database and a user, and installs the latest WordPress. Then it updates WordPress with a restore point and a health check.{application === "WooCommerce" ? <> To add the shop, install the WooCommerce plugin in WordPress, or run <code>plugin install woocommerce --activate</code> on the WordPress tab.</> : null}</>
                    : "Upload WordPress with SFTP. Then select Find WordPress on the WordPress tab."}
                </p>
                {installWordPress && <WordPressFields value={wordpress} onChange={setWordpress} errors={err} prefix="wordpress." />}
              </div>
            </Card>
          )}
          <Card title="Plan and server">
            <div className="card-body">
              {choosePlan ? (
              <fieldset className="radio-cards">
                <legend>Plan</legend>
                {state.plans.map((p) => (
                  <label key={p.id} className="radio-card">
                    <input type="radio" name="plan" value={p.id} checked={planId === p.id} onChange={() => setPlanId(p.id)} />
                    <span>
                      <strong>{p.name}</strong>
                      <span>{p.limits.vcpus} vCPU · {bytes(p.limits.memoryBytes)} memory · {bytes(p.limits.diskBytes)} disk · {p.limits.processes} processes</span>
                    </span>
                  </label>
                ))}
              </fieldset>
              ) : (
                // CF-07: a website gets the plan of its customer. A reseller changes the plan of a customer on the Customers page.
                <p>Plan: <strong>{state.plans.find((p) => p.id === planId)?.name ?? "The plan of the customer"}</strong>.{" "}
                  <span className="muted">{reseller && customerId !== state.session.contextId ? <>To change it, change the plan of the customer on the <Link to="/customers">Customers</Link> page.</> : "Only your provider can change the plan of your account."}</span>
                </p>
              )}
              {webServers.length > 0 && <div style={{ marginTop: 16 }}>
                <Field label="Server" error={err.serverId}>
                  {(p) => (
                    <select {...p} className="select" value={serverId} onChange={(e) => setServerId(e.target.value)}>
                      {webServers.map((s) => <option key={s.id} value={s.id}>{s.name}{s.location ? ` (${s.location})` : ""} · CPU {s.cpuPercent}%</option>)}
                    </select>
                  )}
                </Field>
              </div>}
            </div>
          </Card>
          <div className="btn-group" style={{ marginTop: 24, justifyContent: "flex-end" }}>
            <Link className="btn" to="/websites">Cancel</Link>
            <button type="submit" className="btn btn-primary" disabled={action.busy}>{kind === "worker" ? "Create worker" : "Create website"}</button>
          </div>
        </form>
        {created && <AdminCredentials admin={created.admin} closeLabel="Open the website" onClose={() => navigate(`/websites/${created.websiteId}/wordpress`)} />}
      </main>
    </>
  );
}
