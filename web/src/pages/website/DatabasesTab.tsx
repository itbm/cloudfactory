import type { Website } from "../../api/types";
import { DatabasesPanel } from "../../features/DatabasesPanel";

export function DatabasesTab({ website }: { website: Website }) {
  return <DatabasesPanel websiteId={website.id} />;
}
