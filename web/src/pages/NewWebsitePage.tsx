import { useState } from "react";
import { Link, useNavigate } from "react-router-dom";
import { useAction, useStore } from "../api/hooks";
import { createWebsite } from "../api/store";
import { Topbar } from "../components/Layout";
import { Card, Field, FormError, PageHeader } from "../components/ui";
import { bytes } from "../lib/format";

const APPLICATIONS = ["WordPress", "WooCommerce", "Laravel", "Symfony", "Custom PHP", "Node.js app", "Static site"];

export function NewWebsitePage() {
  const state = useStore();
  const navigate = useNavigate();
  const runtimes = state.images.filter((i) => ["frankenphp", "apache-php-fpm", "node", "custom", "edge"].includes(i.family));
  const webServers = state.servers.filter((s) => s.roles.includes("web"));
  const [name, setName] = useState("");
  const [application, setApplication] = useState("WordPress");
  const [imageId, setImageId] = useState("img_fp85");
  const [planId, setPlanId] = useState("plan_standard");
  const [serverId, setServerId] = useState(webServers[0]?.id ?? "");
  const customerId = state.session.contextId === "platform" ? "cus_acme" : state.session.contextId;
  const action = useAction(createWebsite);

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    const result = await action.run({ name, application, imageId, planId, serverId, customerId });
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
                    if (e.target.value === "Static site") setImageId("img_caddy");
                    else if (e.target.value === "Node.js app") setImageId("img_node24");
                    else if (e.target.value === "Custom PHP") setImageId("img_ap84");
                    else setImageId("img_fp85");
                  }}>
                    {APPLICATIONS.map((a) => <option key={a}>{a}</option>)}
                  </select>
                )}
              </Field>
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
              <div style={{ marginTop: 16 }}>
                <Field label="Server">
                  {(p) => (
                    <select {...p} className="select" value={serverId} onChange={(e) => setServerId(e.target.value)}>
                      {webServers.map((s) => <option key={s.id} value={s.id}>{s.name} ({s.location}) · CPU {s.cpuPercent}%</option>)}
                    </select>
                  )}
                </Field>
              </div>
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
