import { Check, CircleDashed, Minus } from "lucide-react";
import { Link } from "react-router-dom";
import { useStore } from "../api/hooks";
import type { SetupItem } from "../api/types";
import { Topbar } from "../components/Layout";
import { Empty, Meter, PageHeader } from "../components/ui";

const STATE: Record<SetupItem["state"], { label: string; badge: string }> = {
  open: { label: "Open", badge: "badge-warning" },
  done: { label: "Done", badge: "badge-success" },
  not_applicable: { label: "Does not apply", badge: "badge-neutral" },
};

function Item({ item }: { item: SetupItem }) {
  const state = STATE[item.state];
  const open = item.state === "open";
  return (
    <li className={`card setup-item setup-${item.state}`}>
      <span className="setup-mark" aria-hidden="true">
        {item.state === "done" ? <Check size={18} /> : item.state === "open" ? <CircleDashed size={18} /> : <Minus size={18} />}
      </span>
      <div className="setup-text">
        <h2>{item.title} <span className={`badge ${state.badge}`}>{state.label}</span></h2>
        {/* The effect is the reason to do the item. A done item does not need it. */}
        {open && <p><strong>While this is open:</strong> {item.effect}</p>}
        <p className="muted">{item.detail}</p>
      </div>
      {item.state !== "done" && <Link className={open ? "btn btn-primary" : "btn"} to={item.action.target}>{item.action.label}</Link>}
    </li>
  );
}

/**
 * The setup checklist of the first administrator. A new panel operates before the items are
 * complete, so the page shows the effect of each open item in one place.
 */
export function SetupPage() {
  const setup = useStore().setup;
  const items = setup?.items ?? [];
  const counted = items.filter((i) => i.state !== "not_applicable");
  const done = counted.filter((i) => i.state === "done").length;
  return (
    <>
      <Topbar placeholder="Search servers, images, customers and jobs" />
      <main id="main" className="content" style={{ maxWidth: 920 }}>
        <PageHeader title="Finish setup" description="The panel operates now. These items make it safe to use for customers. Each open item shows what can go wrong until you complete it." />
        {!setup ? (
          <Empty title="The panel could not read the setup checklist"><p>The panel API of this release has no checklist, or it did not answer. Try again in a moment.</p></Empty>
        ) : (
          <>
            <section className="card setup-progress" aria-label="Progress">
              <p><strong>{setup.complete ? "Setup is complete." : `${done} of ${counted.length} items are done.`}</strong> {setup.complete ? "The panel checks these items again each time you open this page." : `${setup.open} ${setup.open === 1 ? "item is" : "items are"} open.`}</p>
              <Meter value={counted.length ? done / counted.length : 1} label={`Setup: ${done} of ${counted.length} items done`} />
            </section>
            <ul className="setup-list">
              {/* Open items first: they are the work. */}
              {[...items].sort((a, b) => Number(b.state === "open") - Number(a.state === "open")).map((item) => <Item key={item.id} item={item} />)}
            </ul>
          </>
        )}
      </main>
    </>
  );
}
