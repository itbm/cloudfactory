import type { Website } from "../../api/types";
import { FileManager } from "../../features/FileManager";

export function FilesTab({ website }: { website: Website }) {
  return <FileManager websiteId={website.id} />;
}
