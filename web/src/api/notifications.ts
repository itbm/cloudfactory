// Email notifications: the choices of the signed-in user, the emails that the panel sent, and
// for administrators and resellers the SMTP server and the templates. The pages load this data
// when they open, so it is not in the state of the store.

import * as live from "./live";
import { ApiError } from "./store";

export type NotificationEvent =
  | "job.failed" | "backup.failed" | "certificate.expiring" | "account.suspended"
  | "account.restored" | "signin.new_device" | "usage.alert" | "server.offline" | "deploy.attention";

export interface NotificationPreference {
  event: NotificationEvent;
  label: string;
  description: string;
  email: boolean;
  required: boolean;
}

export type DeliveryState = "queued" | "sent" | "failed" | "skipped";

export interface NotificationDelivery {
  id: string;
  event: NotificationEvent;
  label: string;
  recipient: string;
  subject: string;
  state: DeliveryState;
  error: string | null;
  attempts: number;
  sentBy: "panel" | "reseller";
  createdAt: string;
  sentAt: string | null;
  body?: string;
}

export type MailEncryption = "tls" | "starttls" | "none";

export interface MailServer {
  configured: boolean;
  enabled: boolean;
  host: string | null;
  port: number | null;
  encryption: MailEncryption | null;
  username: string | null;
  hasPassword: boolean;
  fromAddress: string | null;
  fromName: string | null;
  replyTo: string | null;
  testedAt: string | null;
  testError: string | null;
  panelFallback: boolean | null;
}

export interface MailServerInput {
  enabled: boolean;
  host: string;
  port: number;
  encryption: MailEncryption;
  username: string;
  /** Undefined keeps the password. An empty value removes it. */
  password?: string;
  fromAddress: string;
  fromName: string;
  replyTo: string;
}

export interface NotificationTemplate {
  event: NotificationEvent;
  label: string;
  description: string;
  subject: string;
  body: string;
  changed: boolean;
  originalSubject: string;
  originalBody: string;
  variables: string[];
}

export interface TemplatePreview { subject: string; text: string; html: string }

async function call<T>(fn: () => Promise<T>): Promise<T> {
  try {
    return await fn();
  } catch (e) {
    if (e instanceof live.LiveError) throw new ApiError(e.message, e.fields);
    throw e;
  }
}

export const readPreferences = () => call(() => live.request<{ data: NotificationPreference[] }>("GET", "/me/notification-preferences")).then((r) => r.data);

export const setPreferences = (events: Partial<Record<NotificationEvent, boolean>>) =>
  call(() => live.request<{ data: NotificationPreference[] }>("PUT", "/me/notification-preferences", { events })).then((r) => r.data);

export function readDeliveries(filter: { state?: DeliveryState; event?: NotificationEvent; perPage?: number } = {}) {
  const q = new URLSearchParams();
  if (filter.state) q.set("state", filter.state);
  if (filter.event) q.set("event", filter.event);
  q.set("perPage", String(filter.perPage ?? 50));
  return call(() => live.request<{ data: NotificationDelivery[]; meta: { total: number } }>("GET", `/notifications?${q.toString()}`));
}

export const readDelivery = (id: string) => call(() => live.request<NotificationDelivery>("GET", `/notifications/${encodeURIComponent(id)}`));

export const retryDelivery = (id: string) => call(() => live.request<NotificationDelivery>("POST", `/notifications/${encodeURIComponent(id)}/retry`));

export const readMailServer = () => call(() => live.request<MailServer>("GET", "/mail-server"));

export const saveMailServer = (input: MailServerInput) => call(() => live.request<MailServer>("PUT", "/mail-server", input));

export const removeMailServer = () => call(() => live.request<void>("DELETE", "/mail-server"));

export const sendTestEmail = (to?: string) =>
  call(() => live.request<{ sent: boolean; to: string; error: string | null; server: MailServer }>("POST", "/mail-server/test", to ? { to } : {}));

export const readTemplates = () => call(() => live.request<{ data: NotificationTemplate[] }>("GET", "/notification-templates")).then((r) => r.data);

export const saveTemplate = (event: NotificationEvent, subject: string, body: string) =>
  call(() => live.request<NotificationTemplate>("PUT", `/notification-templates/${event}`, { subject, body }));

export const resetTemplate = (event: NotificationEvent) => call(() => live.request<NotificationTemplate>("DELETE", `/notification-templates/${event}`));

export const previewTemplate = (event: NotificationEvent, subject: string, body: string) =>
  call(() => live.request<TemplatePreview>("POST", `/notification-templates/${event}/preview`, { subject, body }));
