import { useState } from "react";
import { useAction, useStore } from "../../api/hooks";
import { setLimits, setWebsitePlan } from "../../api/store";
import type { Limits, Website } from "../../api/types";
import { Card, Field, FormError } from "../../components/ui";
import { GB, MB } from "../../lib/format";

/** Resource limits of the website (FR-202). Only administrators can change them. */
export function SettingsTab({ website }: { website: Website }) {
  const state = useStore();
  const isAdmin = state.session.user.role === "administrator";
  const [form, setForm] = useState({
    vcpus: String(website.limits.vcpus),
    memory: String(website.limits.memoryBytes / MB),
    disk: String(website.limits.diskBytes / GB),
    io: String(website.limits.diskIoBytesPerSecond / MB),
    processes: String(website.limits.processes),
  });
  const save = useAction(async () => {
    const limits: Limits = {
      vcpus: Number(form.vcpus),
      memoryBytes: Number(form.memory) * MB,
      diskBytes: Number(form.disk) * GB,
      diskIoBytesPerSecond: Number(form.io) * MB,
      processes: Number(form.processes),
    };
    return setLimits(website.id, limits);
  });
  const fields: [keyof typeof form, string][] = [
    ["vcpus", "CPU (vCPU)"],
    ["memory", "Memory (MB)"],
    ["disk", "Disk (GB)"],
    ["io", "Disk I/O (MB/s)"],
    ["processes", "Processes"],
  ];
  return (
    <div className="grid-2">
      <Card title="Resource limits">
        <form className="card-body" noValidate onSubmit={(e) => { e.preventDefault(); void save.run(); }}>
          <FormError error={save.error} />
          <p className="muted" style={{ marginBottom: 14 }}>cgroup v2 applies these limits to the container of the website. The plan sets the default values.</p>
          <div className="form-row">
            {fields.map(([k, label]) => (
              <Field key={k} label={label}>
                {(p) => <input {...p} className="input" type="number" min={0} step="any" disabled={!isAdmin} value={form[k]} onChange={(e) => setForm({ ...form, [k]: e.target.value })} />}
              </Field>
            ))}
          </div>
          <button type="submit" className="btn btn-primary" style={{ marginTop: 14 }} disabled={!isAdmin}>Save limits</button>
        </form>
      </Card>
      <div className="stack">
      <Card title="Plan">
        <div className="card-body">
          <Field label="Plan" hint="A new plan also sets new resource limits.">
            {(p) => (
              <select {...p} className="select" value={website.planId} onChange={(e) => {
                void setWebsitePlan(website.id, e.target.value);
                const plan = state.plans.find((x) => x.id === e.target.value)!;
                setForm({ vcpus: String(plan.limits.vcpus), memory: String(plan.limits.memoryBytes / MB), disk: String(plan.limits.diskBytes / GB), io: String(plan.limits.diskIoBytesPerSecond / MB), processes: String(plan.limits.processes) });
              }}>
                {state.plans.map((plan) => <option key={plan.id} value={plan.id}>{plan.name}</option>)}
              </select>
            )}
          </Field>
        </div>
      </Card>
      <Card title="Isolation">
        <div className="card-body">
          <table className="kv">
            <tbody>
              <tr><th scope="row">Unix user</th><td><code>{website.unixUser}</code></td></tr>
              <tr><th scope="row">Sandbox</th><td>gVisor (runsc), rootless Podman</td></tr>
              <tr><th scope="row">Network</th><td>Own container network <code>net-{website.unixUser}</code>. No host networking.</td></tr>
              <tr><th scope="row">Security</th><td>seccomp, no-new-privileges, user namespaces</td></tr>
              <tr><th scope="row">Static files</th><td>The edge proxy serves them directly, read-only.</td></tr>
            </tbody>
          </table>
        </div>
      </Card>
      </div>
    </div>
  );
}
