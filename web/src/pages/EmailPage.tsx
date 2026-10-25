import { Topbar } from "../components/Layout";
import { PageHeader } from "../components/ui";
import { MailPanel } from "../features/MailPanel";

export function EmailPage() {
  return (
    <>
      <Topbar />
      <main id="main" className="content">
        <PageHeader title="Email" description="Mailboxes with SMTP, IMAP and JMAP on Stalwart. The panel shows a warning when a mailbox is 90% full." />
        <MailPanel />
      </main>
    </>
  );
}
