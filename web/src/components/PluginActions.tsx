import { useState } from "react";
import { useAction, useStore } from "../api/hooks";
import { runPluginAction } from "../api/store";
import type { PluginAction, PluginActionResult, PluginActionTarget } from "../api/types";
import { Card, FormError, Modal, type RowAction } from "./ui";

/** The actions of the plugins for one kind of page. Refer to docs/plugins.md. */
export function usePluginActions(target: PluginActionTarget): PluginAction[] {
  return useStore().pluginActions.filter((a) => a.target === target);
}

/**
 * Asks before the panel runs the action, then shows the answer of the plugin. The plugin gets a
 * token of the user for 15 minutes, so the dialog says so.
 */
export function PluginActionDialog({ action, targetId, targetName, onClose }: { action: PluginAction; targetId: string; targetName: string; onClose: () => void }) {
  const run = useAction(runPluginAction);
  const [result, setResult] = useState<PluginActionResult | null>(null);
  const footer = result
    ? <button type="button" className="btn btn-primary" onClick={onClose}>Close</button>
    : (
      <>
        <button type="button" className="btn" onClick={onClose}>Cancel</button>
        <button
          type="button"
          className="btn btn-primary"
          disabled={run.busy}
          onClick={async () => {
            const r = await run.run(action.pluginId, action.id, targetId);
            if (r.ok) setResult(r.value);
          }}
        >
          {run.busy ? "Running…" : action.title}
        </button>
      </>
    );
  return (
    <Modal title={`${action.title}: ${targetName}`} onClose={onClose} footer={footer}>
      {result ? (
        <div className="alert alert-success" role="status">
          <p>{result.message}</p>
          {result.url && <p><a href={result.url} target="_blank" rel="noopener noreferrer">Open the result in {action.pluginName}</a></p>}
        </div>
      ) : (
        <>
          <FormError error={run.error} />
          <p style={{ marginTop: 0 }}>{action.confirm ?? `Run "${action.title}" on ${targetName}?`}</p>
          <p className="muted small">The plugin <strong>{action.pluginName}</strong> does this action. It gets access with your rights and the scopes of the plugin, for 15 minutes.</p>
        </>
      )}
    </Modal>
  );
}

/** A card with a button for each action of the plugins. It is empty when no plugin has an action. */
export function PluginActionsCard({ target, targetId, targetName }: { target: PluginActionTarget; targetId: string; targetName: string }) {
  const actions = usePluginActions(target);
  const [open, setOpen] = useState<PluginAction | null>(null);
  if (!actions.length) return null;
  return (
    <Card title="Plugins">
      <div className="card-body">
        <div className="btn-group plugin-actions">
          {actions.map((a) => (
            <button key={`${a.pluginId}/${a.id}`} type="button" className="btn" onClick={() => setOpen(a)}>
              {a.title}<span className="visually-hidden"> ({a.pluginName})</span>
            </button>
          ))}
        </div>
        <p className="muted small" style={{ marginBottom: 0 }}>From {[...new Set(actions.map((a) => a.pluginName))].join(", ")}.</p>
      </div>
      {open && <PluginActionDialog action={open} targetId={targetId} targetName={targetName} onClose={() => setOpen(null)} />}
    </Card>
  );
}

/** The actions of the plugins as items of a row menu, for a table of customers. */
export function pluginRowActions(actions: PluginAction[], onSelect: (a: PluginAction) => void): RowAction[] {
  return actions.map((a) => ({ label: `${a.title} (${a.pluginName})…`, onSelect: () => onSelect(a) }));
}
