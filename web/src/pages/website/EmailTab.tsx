import type { Website } from "../../api/types";
import { MailPanel } from "../../features/MailPanel";

export function EmailTab({ website }: { website: Website }) {
  return <MailPanel domains={website.domains.filter((d) => d.kind !== "redirect").map((d) => d.name)} />;
}
