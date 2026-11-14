import { useCallback, useEffect, useRef, useState, useSyncExternalStore } from "react";
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

/**
 * Reads data that is not in the state of the store, for example the files of a website. The
 * data loads again when a value of "deps" changes, and when the caller calls reload.
 */
export function useLoad<T>(load: () => Promise<T>, deps: readonly unknown[]) {
  const [result, setResult] = useState<{ data?: T; error: ApiError | null; loading: boolean }>({ error: null, loading: true });
  const [turn, setTurn] = useState(0);
  const fn = useRef(load);
  useEffect(() => { fn.current = load; });
  useEffect(() => {
    let current = true;
    setResult((r) => ({ ...r, loading: true }));
    fn.current().then(
      (data) => { if (current) setResult({ data, error: null, loading: false }); },
      (e: unknown) => { if (current) setResult({ error: e instanceof ApiError ? e : new ApiError((e as Error).message), loading: false }); },
    );
    return () => { current = false; };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [...deps, turn]);
  const reload = useCallback(() => setTurn((t) => t + 1), []);
  const set = useCallback((data: T) => setResult({ data, error: null, loading: false }), []);
  return { ...result, reload, set };
}
