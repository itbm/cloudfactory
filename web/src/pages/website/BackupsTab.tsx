import type { Website } from "../../api/types";
import { BackupsPanel } from "../../features/BackupsPanel";

export function BackupsTab({ website }: { website: Website }) {
  return <BackupsPanel websiteId={website.id} />;
}
