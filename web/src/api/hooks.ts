import { useCallback, useState, useSyncExternalStore } from "react";
import { ApiError, getState, subscribe } from "./store";
import type { State } from "./types";

/** Reads the panel state. The component renders again on each change, so job progress shows live. */
export function useStore(): State {
  return useSyncExternalStore(subscribe, getState, getState);
}

export type ActionResult<R> = { ok: true; value: R } | { ok: false; error: ApiError };

/** Runs an API call and keeps its error for the form. */
export function useAction<A extends unknown[], R>(fn: (...args: A) => R | Promise<R>) {
  const [error, setError] = useState<ApiError | null>(null);
  const [busy, setBusy] = useState(false);
  const run = useCallback(
    async (...args: A): Promise<ActionResult<R>> => {
      setError(null);
      setBusy(true);
      try {
        return { ok: true, value: await fn(...args) };
      } catch (e) {
        const error = e instanceof ApiError ? e : new ApiError((e as Error).message);
        setError(error);
        return { ok: false, error };
      } finally {
        setBusy(false);
      }
    },
    [fn],
  );
  return { run, error, busy, clear: () => setError(null) };
}
