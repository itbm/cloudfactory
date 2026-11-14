import type { Website } from "../../api/types";
import { FileManager } from "../../features/FileManager";

/** The file manager of the website. A new website shows its files from the top directory. */
export function FilesTab({ website }: { website: Website }) {
  if (website.status === "failed") return <p className="muted">This website has no files, because the panel could not make it.</p>;
  return <FileManager key={website.id} website={website} />;
}
