// The panel has two modes.
//
// - live: the panel uses the real panel API (live.ts). The production build uses this mode.
// - mock: the panel uses the in-memory API in store.ts with demonstration data. The
//   development server, the tests and the screenshots use this mode.
//
// Set VITE_API_MODE=live or VITE_API_MODE=mock to choose a mode.

const env = import.meta.env as { VITE_API_MODE?: string; PROD?: boolean };

export const LIVE = env.VITE_API_MODE ? env.VITE_API_MODE === "live" : !!env.PROD;

/**
 * Areas of the panel that the API of release 0.1 does not have yet. In live mode the panel
 * hides them. In mock mode the panel shows them with demonstration data.
 */
export const features = {
  dns: !LIVE,
  email: !LIVE,
  files: !LIVE,
  cron: !LIVE,
  deploys: !LIVE,
  access: !LIVE,
  logs: !LIVE,
  passkeys: !LIVE,
  system: !LIVE,
  alerts: !LIVE,
  branding: !LIVE,
};
