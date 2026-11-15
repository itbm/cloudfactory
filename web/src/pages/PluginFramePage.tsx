import { useEffect, useRef, useState } from "react";
import { useParams } from "react-router-dom";
import { useStore } from "../api/hooks";
import { ApiError, pluginPageSession } from "../api/store";
import type { PluginPageSession } from "../api/types";
import { Topbar } from "../components/Layout";
import { Empty, FormError, PageHeader } from "../components/ui";

/** The panel gives a new page token before the token of 15 minutes expires. */
const RENEW_MS = 12 * 60 * 1000;

/**
 * A page of a plugin. The page has the origin of the plugin, not of the panel, so it cannot
 * read the panel, its storage or the session token. The panel gives the page a short token of
 * the signed-in user, with the scopes of the plugin, only to the origin of the page and only
 * when the page asks for it ("cloudfactory:ready"). Refer to docs/plugins.md.
 */
export function PluginFramePage() {
  const { pluginId = "", pageId = "" } = useParams();
  const state = useStore();
  const page = state.pluginPages.find((p) => p.pluginId === pluginId && p.id === pageId);
  const frame = useRef<HTMLIFrameElement>(null);
  const session = useRef<PluginPageSession | null>(null);
  const [error, setError] = useState<ApiError | null>(null);
  const [height, setHeight] = useState(640);
  // The list of pages is new after each load. The frame keeps its session while the page is the same.
  const key = page ? `${page.pluginId}/${page.id}/${page.origin}` : "";

  useEffect(() => {
    if (!page) return;
    let stopped = false;
    session.current = null;
    const send = () => {
      const s = session.current;
      if (!s || !frame.current?.contentWindow) return;
      frame.current.contentWindow.postMessage({ type: "cloudfactory:session", version: 1, ...s, apiBase: `${location.origin}/api/v1`, panelOrigin: location.origin }, page.origin);
    };
    const renew = async () => {
      try {
        session.current = await pluginPageSession(page.pluginId, page.id);
        if (!stopped) send();
      } catch (e) {
        if (!stopped) setError(e instanceof ApiError ? e : new ApiError((e as Error).message));
      }
    };
    const onMessage = (e: MessageEvent) => {
      // Only the frame of this page, from the origin of the plugin.
      if (e.source !== frame.current?.contentWindow || e.origin !== page.origin) return;
      const data = e.data as { type?: string; height?: number } | null;
      if (data?.type === "cloudfactory:ready") {
        if (session.current) send();
        else void renew();
      }
      if (data?.type === "cloudfactory:resize" && typeof data.height === "number") {
        setHeight(Math.max(240, Math.min(6000, Math.ceil(data.height) + 4)));
      }
    };
    window.addEventListener("message", onMessage);
    const timer = window.setInterval(() => void renew(), RENEW_MS);
    return () => {
      stopped = true;
      window.removeEventListener("message", onMessage);
      window.clearInterval(timer);
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [key]);

  if (!page) {
    return (
      <>
        <Topbar />
        <main id="main" className="content">
          <Empty title="This page is not available">The plugin is off or removed, or it has no page for your role.</Empty>
        </main>
      </>
    );
  }
  return (
    <>
      <Topbar />
      <main id="main" className="content content-wide">
        <PageHeader
          title={page.title}
          description={<>A page of the plugin <strong>{page.pluginName}</strong>. It comes from <code>{page.origin}</code>, not from the panel.</>}
        />
        <FormError error={error} />
        <iframe
          ref={frame}
          className="plugin-frame"
          title={`${page.title} (${page.pluginName})`}
          src={page.url}
          style={{ height }}
          // The page has its own origin (the API refuses the origin of the panel), so its scripts
          // cannot reach the panel. It cannot open the panel in the top window.
          sandbox="allow-scripts allow-same-origin allow-forms allow-popups allow-popups-to-escape-sandbox allow-downloads"
          referrerPolicy="no-referrer"
          allow=""
        />
      </main>
    </>
  );
}
