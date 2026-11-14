// Live mode: the progress of a rollout comes from the panel API only.
import { expect, it, vi } from "vitest";
import type { Rollout, State } from "./types";

vi.mock("./mode", async (original) => ({ ...(await original<typeof import("./mode")>()), LIVE: true }));
vi.mock("./live", async (original) => ({
  ...(await original<typeof import("./live")>()),
  hasSession: () => true,
  // The rollout as the panel API gives it: one website, and the agent has not finished the update.
  load: async (state: State): Promise<State> => ({ ...state, session: { ...state.session, signedIn: true, loading: false }, rollout }),
}));

const rollout: Rollout = {
  id: "r1", imageId: "i1", title: "FrankenPHP · PHP 8.5 0.1.9", fromTag: "0.1.7", toTag: "0.1.9", severity: "security", state: "running",
  startedAt: "2026-11-13T23:37:01Z", window: "", websites: 1, servers: 1, healthy: 0, rolledBack: 0, rollbacks: [],
  stages: [
    { name: "Canary", websites: 1, state: "in_progress" },
    { name: "10%", websites: 1, state: "waiting" },
    { name: "50%", websites: 1, state: "waiting" },
    { name: "Everyone", websites: 1, state: "waiting" },
  ],
  checks: [{ label: "Signature verified", passed: true }, { label: "Health checks pass", passed: null }],
};

it("does not move a real rollout forward in the browser", async () => {
  const { _rolloutTick, getState, refresh } = await import("./store");
  await refresh();
  expect(getState().rollout).toEqual(rollout);
  // Before, the tenth call had the error "Cannot set properties of undefined (setting 'passed')".
  for (let i = 0; i < 12; i++) _rolloutTick();
  expect(getState().rollout).toEqual(rollout);
});
