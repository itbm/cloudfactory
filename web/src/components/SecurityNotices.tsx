import { useState } from "react";
import { Link } from "react-router-dom";
import { useStore } from "../api/hooks";
import type { SecurityNotice } from "../api/types";
import { relativeTime } from "../lib/format";

/** The browser keeps the time of the newest notice that the user closed. */
const SEEN = "cloudfactory.securityNotices.seen";

function readSeen(): string {
  try {
    return localStorage.getItem(SEEN) ?? "";
  } catch {
    return "";
  }
}

/** The audit actions of the API (AuthController::ADMINISTRATOR_CHANGES) as sentences. */
function sentence(n: SecurityNotice): string {
  switch (n.action) {
    case "Created administrator": return `${n.actor} added the administrator ${n.target}`;
    case "Reset password of administrator": return `${n.actor} reset the password of the administrator ${n.target}`;
    case "Reset password and removed TOTP of administrator": return `${n.actor} reset the password and removed TOTP of the administrator ${n.target}`;
    case "Deleted administrator": return `${n.actor} deleted the administrator ${n.target}`;
    default: return `${n.actor}: ${n.action} ${n.target}`;
  }
}

/**
 * Second audit, finding F09: each administrator sees the changes that a different person made
 * to the administrator accounts. A person who took one account cannot change the other
 * accounts with no notice.
 */
export function SecurityNotices() {
  const state = useStore();
  const [seen, setSeen] = useState(readSeen);
  const notices = state.securityNotices.filter((n) => n.at > seen);
  if (notices.length === 0) return null;
  const close = () => {
    const newest = notices.reduce((a, n) => (n.at > a ? n.at : a), seen);
    setSeen(newest);
    try {
      localStorage.setItem(SEEN, newest);
    } catch {
      // Without storage, the notice comes back after the next page load, for 7 days.
    }
  };
  return (
    <div className="alert alert-warning alert-stack load-error security-notices" role="status">
      <p><strong>Changes to the administrator accounts.</strong> A different person made {notices.length === 1 ? "this change" : "these changes"} in the last 7 days:</p>
      <ul className="notice-list">
        {notices.map((n) => <li key={`${n.at} ${n.action} ${n.target}`}>{sentence(n)}, {relativeTime(n.at)}.</li>)}
      </ul>
      <p>If you do not know about a change, reset the password of that administrator and look at the <Link to="/settings">audit log in the settings</Link>.</p>
      <div className="btn-group">
        <button type="button" className="btn btn-sm" onClick={close}>I know about {notices.length === 1 ? "this change" : "these changes"}</button>
      </div>
    </div>
  );
}
