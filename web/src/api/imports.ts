// The import of an account from cPanel, DirectAdmin, Plesk, HestiaCP or a different Cloudfactory
// panel (docs/imports.md). Only an administrator imports an account. The page loads this data when it opens, so it is not in the
// state of the store.

import * as live from "./live";
import { ApiError } from "./store";

export type ImportStatus = "uploading" | "fetching" | "ready" | "starting" | "running" | "done" | "failed";
export type ImportItemState = "waiting" | "running" | "done" | "failed" | "skipped";
export type ImportItemKind =
  | "website" | "domain" | "database" | "databaseUsers" | "cronJob" | "files" | "dump"
  | "mailDomain" | "mailbox" | "mailAlias" | "messages" | "dnsZone";

export interface Import {
  id: string;
  status: ImportStatus;
  source: "upload" | "url";
  fileName: string | null;
  sizeBytes: number;
  receivedBytes: number;
  panel: "cpanel" | "directadmin" | "plesk" | "hestia" | "cloudfactory" | null;
  panelName: string | null;
  account: string | null;
  customerId: string;
  customerName: string | null;
  serverId: string;
  serverName: string | null;
  error: string | null;
  counts: { websites: number; databases: number; mailboxes: number; dnsZones: number } | null;
  progress: { total: number; done: number; failed: number };
  createdAt: string;
  startedAt: string | null;
  finishedAt: string | null;
}

export interface PlanWebsite {
  name: string;
  domains: { name: string; kind: "primary" | "alias" | "redirect" }[];
  phpVersion: string | null;
  imageId: string | null;
  imageName: string | null;
  diskBytes: number;
  files: boolean;
  databases: { name: string; bytes: number }[];
  databaseUsers: number;
  cronJobs: number;
  wordpress: boolean;
  conflict: string | null;
}

export interface PlanMailDomain { name: string; mailboxes: number; quotaBytes: number; aliases: number; messages: number; bytes: number; newPasswords: number; conflict: string | null }
export interface PlanDnsZone { name: string; exists: boolean; conflict: string | null }

export interface ImportPlan {
  websites: PlanWebsite[];
  mailDomains: PlanMailDomain[];
  /** The free mail space in the plan of the customer. */
  mailSpace: { mailboxes: number; bytes: number } | null;
  dnsZones: PlanDnsZone[];
  images: { id: string; name: string; phpVersion: string }[];
}

export interface ImportItem { id: string; kind: ImportItemKind; name: string; state: ImportItemState; error: string | null; detail: string | null; jobId: string | null }

export interface ImportDetail extends Import {
  customer: { name: string; email: string } | null;
  warnings: { item: string; message: string }[];
  plan: ImportPlan | null;
  items: ImportItem[];
}

export interface ImportSelection {
  websites: { name: string; imageId?: string | null }[];
  mailDomains: string[];
  dnsZones: string[];
  rewriteWordPress: boolean;
}

/** The largest part of an upload. The panel accepts it with the usual settings of PHP. */
export const CHUNK_BYTES = 4 * 1024 ** 2;

async function call<T>(fn: () => Promise<T>): Promise<T> {
  try {
    return await fn();
  } catch (e) {
    if (e instanceof live.LiveError) throw new ApiError(e.message, e.fields);
    throw e;
  }
}

const path = (id: string) => `/imports/${encodeURIComponent(id)}`;

export const readImports = () => call(() => live.request<{ data: Import[] }>("GET", "/imports")).then((r) => r.data);

export const readImport = (id: string) => call(() => live.request<ImportDetail>("GET", path(id)));

export type NewImport = { customerId: string; serverId: string } & ({ url: string } | { fileName: string; sizeBytes: number });

export const createImport = (input: NewImport) => call(() => live.request<{ import: Import }>("POST", "/imports", input)).then((r) => r.import);

export const startImport = (id: string, selection: ImportSelection) => call(() => live.request<ImportDetail>("POST", `${path(id)}/start`, selection));

export const removeImport = (id: string) => call(() => live.request<void>("DELETE", path(id)));

const pause = (ms: number) => new Promise((resolve) => setTimeout(resolve, ms));

/**
 * Sends the file in parts, from the number of bytes that the panel has. A part that failed
 * goes again up to 3 times. When the panel has a different number of bytes, for example after
 * a connection error, the upload continues from that number.
 */
export async function uploadArchive(id: string, file: Blob, from: number, onProgress: (received: number) => void, stopped: () => boolean = () => false): Promise<void> {
  let offset = from;
  let tries = 0;
  while (offset < file.size && !stopped()) {
    try {
      const r = await live.sendBytes<{ receivedBytes: number }>("PUT", `${path(id)}/archive?offset=${offset}`, file.slice(offset, offset + CHUNK_BYTES));
      offset = r.receivedBytes;
      tries = 0;
    } catch (e) {
      const received = e instanceof live.LiveError ? (e.body as { receivedBytes?: unknown } | undefined)?.receivedBytes : undefined;
      if (e instanceof live.LiveError && e.status === 409 && typeof received === "number") {
        offset = received;
      } else if (e instanceof live.LiveError && e.status !== 0 && e.status < 500) {
        throw new ApiError(e.message, e.fields);
      } else if (++tries > 3) {
        throw e instanceof live.LiveError ? new ApiError(e.message, e.fields) : e;
      } else {
        await pause(1000 * tries);
      }
    }
    onProgress(offset);
  }
}
