import { useState } from "react";
import { Link, useNavigate } from "react-router-dom";
import { useAction, useStore } from "../api/hooks";
import { ApiError, createWebsite, makeCustomer } from "../api/store";
import { contextCustomers } from "../api/selectors";
import type { RuntimeImage } from "../api/types";
import { Topbar } from "../components/Layout";
import { Card, Field, FormError, PageHeader } from "../components/ui";
import { bytes } from "../lib/format";

/** The value of the customer field for a customer that does not exist yet. */
const NEW_CUSTOMER = "new";

const APPLICATIONS = ["WordPress", "WooCommerce", "Laravel", "Symfony", "Custom PHP", "Node.js app", "Static site"];

/** The usual image family for each application. */
const FAMILY: Record<string, RuntimeImage["family"]> = { "Static site": "edge", "Node.js app": "node", "Custom PHP": "apache-php-fpm" };

/** The image of a family with the newest PHP version that still has support, or the first image. */
export function defaultImage(images: RuntimeImage[], family: RuntimeImage["family"]): string {
  const ofFamily = images.filter((i) => i.family === family);
  const pool = ofFamily.length ? ofFamily : images.filter((i) => i.family === "frankenphp");
  // Compare as numbers, so that PHP 8.10 is newer than 8.5.
  const sorted = [...pool].sort((a, b) => (b.phpVersion ?? "").localeCompare(a.phpVersion ?? "", undefined, { numeric: true }));
  return (sorted.find((i) => !i.supportEndsAt || new Date(i.supportEndsAt) > new Date()) ?? sorted[0] ?? images[0])?.id ?? "";
}

export function NewWebsitePage() {
  const state = useStore();
  const navigate = useNavigate();
  const runtimes = state.images.filter((i) => ["frankenphp", "apache-php-fpm", "node", "custom", "edge"].includes(i.family));
  const webServers = state.servers.filter((s) => s.roles.includes("web"));
  const [name, setName] = useState("");
  const [application, setApplication] = useState("WordPress");
  const platform = state.session.contextId === "platform";
  // CF-07: only an administrator can select the plan here. Other users get the plan of the account.
  const choosePlan = platform || state.session.user.role === "administrator";
  const [imageId, setImageId] = useState(() => defaultImage(runtimes, "frankenphp"));
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
    return createWebsite({ name, application, imageId, planId, serverId, customerId: owner });
  });

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    const result = await action.run();
    if (result.ok) navigate(`/websites/${result.value.websiteId}`);
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
        <PageHeader title="New website" description="The panel makes a Unix user, a container network and a container in the gVisor sandbox. Then it gets a TLS certificate. This takes less than 60 seconds." />
        <form onSubmit={submit} noValidate>
          <Card title="Website">
            <div className="card-body">
              <FormError error={action.error} />
              <Field label="Primary domain" hint="You can add redirects and aliases later." error={err.name}>
                {(p) => <input {...p} className="input" value={name} onChange={(e) => setName(e.target.value)} placeholder="example.com" autoComplete="off" autoCapitalize="none" spellCheck={false} />}
              </Field>
              <Field label="Application">
                {(p) => (
                  <select {...p} className="select" value={application} onChange={(e) => {
                    setApplication(e.target.value);
                    setImageId(defaultImage(runtimes, FAMILY[e.target.value] ?? "frankenphp"));
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
              <fieldset className="radio-cards">
                <legend className="visually-hidden">Runtime image</legend>
                {runtimes.map((i) => (
                  <label key={i.id} className="radio-card">
                    <input type="radio" name="runtime" value={i.id} checked={imageId === i.id} onChange={() => setImageId(i.id)} />
                    <span>
                      <strong>{i.name}</strong>
                      <span>
                        {i.family === "apache-php-fpm" ? "For websites that use .htaccess files." : i.family === "edge" ? "Static files. No container code." : i.custom ? "Your own OCI image." : `Version ${i.running}.`}
                        {i.supportEndsAt && <span className="note-warning" style={{ display: "block" }}>Support ends {new Date(i.supportEndsAt).toLocaleDateString("en-GB", { month: "short", year: "numeric" })}</span>}
                      </span>
                    </span>
                  </label>
                ))}
              </fieldset>
              <p className="hint" style={{ marginTop: 10 }}>Each image is signed and scanned. The container runs in gVisor. There is no option for a different runtime.</p>
            </div>
          </Card>
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
            <button type="submit" className="btn btn-primary" disabled={action.busy}>Create website</button>
          </div>
        </form>
      </main>
    </>
  );
}
