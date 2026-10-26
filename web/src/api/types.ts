// Types for the panel API. They follow the schemas in openapi/openapi.yaml.
// Replace this file with the generated client when the panel API exists.

export type Role = "administrator" | "reseller" | "owner";

export interface User {
  id: string;
  name: string;
  email: string;
  role: Role;
}

export interface AccountContext {
  id: string;
  name: string;
  role: Role;
}

export interface Usage {
  used: number;
  limit: number;
}

export type WebsiteStatus = "running" | "stopped" | "starting" | "stopping" | "failed";

export type RuntimeKind = "frankenphp" | "apache-php-fpm" | "oci" | "static";

export type PhpVersion = "8.2" | "8.3" | "8.4" | "8.5";

export interface Runtime {
  kind: RuntimeKind;
  imageId: string;
  phpVersion?: PhpVersion;
  workerMode: boolean;
  extensions: string[];
  phpIni: Record<string, string>;
  /** The previous image, kept so that the owner can go back in one action (FR-303). */
  previous?: { imageId: string; phpVersion?: PhpVersion; tag: string };
}

export type CertificateState = "valid" | "renewed" | "pending" | "failed" | "not_needed";

export interface Certificate {
  state: CertificateState;
  expiresAt?: string;
  renewedAt?: string;
}

export interface LastChange {
  at: string;
  summary: string;
  /** Git ref or image tag, shown in monospace. */
  ref?: string;
}

export type DomainKind = "primary" | "redirect" | "alias";

export interface Domain {
  name: string;
  kind: DomainKind;
  certificate: Certificate;
  dnsPointsHere: boolean;
}

export interface Release {
  id: string;
  ref: string;
  message: string;
  at: string;
  state: JobState;
  durationSeconds: number;
  live: boolean;
  /** The deploy ran a database migration, so no automatic rollback (FR-1009). */
  migration: boolean;
}

export interface GitSource {
  repository: string;
  branch: string;
  trigger: "webhook" | "schedule";
  scheduleMinutes: number;
  buildCommands: string[];
  deployKey: string;
  secrets: string[];
}

export interface PreviewSite {
  url: string;
  title: string;
}

export interface Limits {
  vcpus: number;
  memoryBytes: number;
  diskBytes: number;
  diskIoBytesPerSecond: number;
  processes: number;
}

export interface ResourceUse {
  cpuPercent: number;
  memoryBytes: number;
  diskBytes: number;
  processes: number;
  diskIoBytesPerSecond: number;
}

export interface CronJob {
  id: string;
  schedule: string;
  command: string;
  enabled: boolean;
  lastRunAt?: string;
  lastResult?: "succeeded" | "failed";
}

export interface SshKey {
  id: string;
  name: string;
  fingerprint: string;
  addedAt: string;
}

export interface Website {
  id: string;
  name: string;
  application: string;
  customerId: string;
  serverId: string;
  unixUser: string;
  createdAt: string;
  status: WebsiteStatus;
  runtime: Runtime;
  planId: string;
  limits: Limits;
  use: ResourceUse;
  domains: Domain[];
  git?: GitSource;
  releases: Release[];
  previews: PreviewSite[];
  cron: CronJob[];
  sshKeys: SshKey[];
  sftpPasswordEnabled: boolean;
  shellEnabled: boolean;
  lastChange: LastChange;
  /** A staging copy points to its source website (FR-205). */
  stagingOf?: string;
  managedByTerraform: boolean;
  /** The image tag that the website uses. When it is not set, the website uses the running tag of its image. */
  imageTag?: string;
  /** The full image reference with its tag, in live mode. */
  imageReference?: string;
  updateScheduledAt?: string;
}

export type JobState = "queued" | "running" | "succeeded" | "failed";

export interface Job {
  id: string;
  title: string;
  /** Inline code, for example a commit hash. */
  ref?: string;
  state: JobState;
  steps: string[];
  step: number;
  error?: string;
  createdAt: string;
  finishedAt?: string;
  websiteId?: string;
  serverId?: string;
}

export interface ActivityItem {
  id: string;
  text: string;
  ref?: string;
  at: string;
}

export type Tone = "success" | "info" | "warning" | "danger" | "neutral";

export interface Notification {
  id: string;
  tone: Tone;
  text: string;
  at: string;
  read: boolean;
  href?: string;
}

export interface AttentionItem {
  id: string;
  tone: "warning" | "info";
  subject: string;
  text: string;
  action: string;
  href: string;
}

export type StageState = "done" | "in_progress" | "waiting";

export interface RolloutStage {
  name: string;
  websites: number;
  state: StageState;
  note?: string;
}

export interface Rollout {
  id: string;
  imageId: string;
  title: string;
  fromTag: string;
  toTag: string;
  severity: "security" | "feature";
  state: "running" | "paused" | "rolled_back" | "done";
  startedAt: string;
  window: string;
  websites: number;
  servers: number;
  stages: RolloutStage[];
  healthy: number;
  rolledBack: number;
  rollbacks: { website: string; reason: string }[];
  checks: { label: string; passed: boolean | null }[];
}

export type UpdatePolicy = "automatic" | "security_automatic" | "manual";

export interface RuntimeImage {
  id: string;
  name: string;
  family: "frankenphp" | "apache-php-fpm" | "node" | "static" | "edge" | "mail" | "dns" | "database" | "custom";
  reference: string;
  running: string;
  available?: string;
  availableNote?: "rolling_out" | "queued";
  phpVersion?: PhpVersion;
  supportEndsAt?: string;
  websites: string;
  policy: UpdatePolicy;
  policyNote?: string;
  /** Images that an administrator added (FR-307). */
  custom: boolean;
  signatureVerified: boolean;
  digest: string;
}

export type ServerRole = "web" | "database" | "email" | "dns" | "backup";

export type ServerState = "healthy" | "updating" | "agent_update" | "degraded" | "offline" | "rebooting" | "joining";

export type HostUpdatePolicy = "security" | "all" | "manual";

export interface PackageUpdate {
  name: string;
  from: string;
  to: string;
  security: boolean;
}

export interface ServerService {
  name: string;
  state: "running" | "stopped" | "failed";
}

export interface Server {
  id: string;
  name: string;
  location: string;
  roles: ServerRole[];
  agentVersion: string;
  cpuPercent: number;
  memoryPercent: number;
  diskPercent: number;
  state: ServerState;
  stateDetail?: string;
  os: string;
  kernel: string;
  arch: "x86-64" | "arm64";
  podmanVersion: string;
  gvisorVersion: string;
  gvisorPlatform: "systrap" | "kvm";
  bareMetal: boolean;
  updatePolicy: HostUpdatePolicy;
  maintenanceWindow: string;
  pendingUpdates: PackageUpdate[];
  lastUpdateAt: string;
  rebootRequired: boolean;
  services: ServerService[];
}

export type DnsRecordType = "A" | "AAAA" | "CNAME" | "MX" | "TXT" | "SRV" | "CAA" | "NS";

export interface DnsRecord {
  id: string;
  type: DnsRecordType;
  name: string;
  value: string;
  ttl: number;
  priority?: number;
  /** Records that the panel makes and keeps (FR-602). */
  managed?: "mail" | "website" | "ns";
}

export interface DnsZone {
  name: string;
  customerId: string;
  nameservers: string[];
  records: DnsRecord[];
  managedByTerraform: boolean;
}

export interface Mailbox {
  id: string;
  address: string;
  quotaBytes: number;
  usedBytes: number;
}

export interface MailAlias {
  id: string;
  kind: "alias" | "forwarder";
  source: string;
  destinations: string[];
}

export type DatabaseEngine = "mariadb" | "postgresql";

export interface Database {
  id: string;
  name: string;
  engine: DatabaseEngine;
  websiteId: string;
  sizeBytes: number;
  serverId: string;
}

export interface DatabaseUser {
  id: string;
  name: string;
  engine: DatabaseEngine;
  websiteId: string;
  databases: string[];
  /** FR-502: by default only the website container can connect. */
  access: "website" | "anywhere";
}

export interface FileNode {
  path: string;
  name: string;
  type: "dir" | "file";
  sizeBytes: number;
  modifiedAt: string;
  content?: string;
}

export interface Snapshot {
  id: string;
  at: string;
  kind: "nightly" | "manual";
  sizeBytes: number;
  websiteId: string;
  databases: string[];
  mailboxes: string[];
}

export interface BackupPolicy {
  daily: number;
  weekly: number;
  monthly: number;
  time: string;
  target: string;
}

export interface Plan {
  id: string;
  name: string;
  limits: Limits;
  websites: number;
  mailboxes: number;
  databases: number;
  phpExtensions: string[];
}

export interface Customer {
  id: string;
  name: string;
  email: string;
  planId: string;
  role: "reseller" | "owner";
  resellerId?: string;
  createdAt: string;
  suspended: boolean;
}

export interface Branding {
  name: string;
  colour: string;
  panelDomain: string;
  supportEmail: string;
}

export type TokenScope =
  | "websites:read" | "websites:write"
  | "dns:read" | "dns:write"
  | "email:read" | "email:write"
  | "databases:read" | "databases:write"
  | "backups:read" | "backups:write"
  | "servers:read" | "servers:write"
  | "images:read" | "images:write"
  | "customers:read" | "customers:write"
  | "audit:read";

export interface ApiToken {
  id: string;
  name: string;
  scopes: TokenScope[];
  createdAt: string;
  expiresAt: string;
  lastUsedAt?: string;
  prefix: string;
}

export interface Passkey {
  id: string;
  name: string;
  addedAt: string;
  lastUsedAt?: string;
}

export interface AuditEntry {
  id: string;
  at: string;
  actor: string;
  ip: string;
  action: string;
  target: string;
}

export interface AlertChannel {
  id: string;
  kind: "email" | "webhook";
  target: string;
  events: string[];
}

export interface SystemSettings {
  registry: string;
  registryMirror: string;
  panelDomain: string;
  timezone: string;
}

export interface SearchResult {
  kind: "website" | "domain" | "mailbox" | "job" | "server" | "image" | "customer" | "zone";
  label: string;
  detail: string;
  href: string;
}

export interface State {
  /** loading: in live mode, the panel has a session but has not loaded the data yet. */
  session: { signedIn: boolean; loading?: boolean; user: User; contextId: string };
  contexts: AccountContext[];
  websites: Website[];
  jobs: Job[];
  activity: ActivityItem[];
  notifications: Notification[];
  servers: Server[];
  images: RuntimeImage[];
  rollout?: Rollout;
  zones: DnsZone[];
  mailboxes: Mailbox[];
  aliases: MailAlias[];
  databases: Database[];
  databaseUsers: DatabaseUser[];
  files: Record<string, FileNode[]>;
  snapshots: Snapshot[];
  backupPolicy: BackupPolicy;
  plans: Plan[];
  customers: Customer[];
  branding: Branding;
  tokens: ApiToken[];
  passkeys: Passkey[];
  totpEnabled: boolean;
  audit: AuditEntry[];
  alertChannels: AlertChannel[];
  system: SystemSettings;
  joinToken: string;
}
