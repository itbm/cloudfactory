import { useState } from "react";
import { Link } from "react-router-dom";
import { useStore } from "../api/hooks";
import { visibleWebsites } from "../api/selectors";
import { Topbar } from "../components/Layout";
import { Empty, Field, PageHeader } from "../components/ui";
import { FileManager } from "../features/FileManager";

/** The files of each website that the user can see, one website at a time. */
export function FilesPage() {
  const state = useStore();
  const sites = visibleWebsites(state).filter((w) => w.status !== "failed");
  const [selected, setSelected] = useState("");
  const website = sites.find((w) => w.id === selected) ?? sites[0];
  return (
    <>
      <Topbar />
      <main id="main" className="content">
        <PageHeader
          title="Files"
          description="See and change the files of a website. You can also use SFTP."
          actions={sites.length > 0 && (
            <Field label="Website">
              {(p) => (
                <select {...p} className="select" value={website?.id ?? ""} onChange={(e) => setSelected(e.target.value)}>
                  {sites.map((w) => <option key={w.id} value={w.id}>{w.name}</option>)}
                </select>
              )}
            </Field>
          )}
        />
        {website ? <FileManager key={website.id} website={website} /> : (
          <Empty title="No websites">
            <p>The file manager shows the files of a website. <Link to="/websites/new">Make a website</Link>.</p>
          </Empty>
        )}
      </main>
    </>
  );
}
