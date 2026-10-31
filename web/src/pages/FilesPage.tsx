import { useState } from "react";
import { useStore } from "../api/hooks";
import { visibleWebsites } from "../api/selectors";
import { Topbar } from "../components/Layout";
import { Field, PageHeader } from "../components/ui";
import { FileManager } from "../features/FileManager";

export function FilesPage() {
  const state = useStore();
  const sites = visibleWebsites(state);
  const [site, setSite] = useState(sites[0]?.id ?? "");
  return (
    <>
      <Topbar />
      <main id="main" className="content">
        <PageHeader
          title="Files"
          description="The files of each website. You can also use SFTP. Access is limited to the container of the website."
          actions={
            <Field label="Website">
              {(p) => (
                <select {...p} className="select" value={site} onChange={(e) => setSite(e.target.value)}>
                  {sites.map((w) => <option key={w.id} value={w.id}>{w.name}</option>)}
                </select>
              )}
            </Field>
          }
        />
        {site && <FileManager key={site} websiteId={site} />}
      </main>
    </>
  );
}
