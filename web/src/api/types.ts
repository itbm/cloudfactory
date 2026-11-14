// Types for the panel API. They follow the schemas in openapi/openapi.yaml.
// live.ts maps the answers of the API to these types.

export type Role = "administrator" | "reseller" | "owner";

export interface User {
  id: string;
  name: string;
  email: string;
  role: Role;
}

/** Audit finding B5: a user who can sign in, as an administrator or a reseller sees it. */
export interface ManagedUser {
  id: string;
  name: string;
  email: string;
  role: Role;
  /** Not set for an administrator. */
  customerId?: string;
  totpEnabled: boolean;
  /** The user has a temporary password and must set a new one at the next sign-in. */
  mustChangePassword: boolean;
  createdAt: string;
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

export type PhpVersion = "8.4" | "8.5";

export interface Runtime {
  kind: RuntimeKind;
  imageId: string;
  phpVersion?: PhpVersion;
  workerMode: boolean;
  extensions: string[];
  phpIni: Record<string, string>;
  /** The previous image, kept so that the owner can go back in one action (FR-303). */
  previous?: { imageId: string; phpVersion?: PhpVersion; tag: string };
  /** The directory in /app that the web server serves. "." is /app. The default is "public". */
  documentRoot?: string;
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

/** CF-03: the DNS record that proves that the customer controls the domain. */
export interface DomainVerification {
  type: "TXT";
  name: string;
  value: string;
}

export interface Domain {
  name: string;
  kind: DomainKind;
  certificate: Certificate;
  dnsPointsHere: boolean;
  /** Set while the domain is not verified. The edge proxy does not serve the domain yet. */
  verification?: DomainVerification;
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

export interface SftpAccess {
  enabled: boolean;
  host: string;
  port: number;
  username: string;
  /** OpenSSH public keys, one key in each item. */
  authorizedKeys: string[];
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
  /** The SFTP access from the panel API. */
  sftp?: SftpAccess;
  lastChange: LastChange;
  /** A staging copy points to its source website (FR-205). */
  stagingOf?: string;
  /**
   * True when the website uses the shared database containers of an earlier release. The other
   * websites use the database containers of their customer.
   */
  sharedDatabases?: boolean;
  managedByTerraform: boolean;
  /** The image tag that the website uses. When it is not set, the website uses the running tag of its image. */
  imageTag?: string;
  /** The full image reference with its tag. */
  imageReference?: string;
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
  /** The time when the rollout ended. A rollout in progress has none. */
  finishedAt?: string;
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
  /** The error of the last check of the registry for a new version. */
  checkError?: string;
  /** Images that an administrator added (FR-307). */
  custom: boolean;
  signatureVerified: boolean;
  digest: string;
  /** FR-307: the HTTP port and the start command of a custom image. */
  containerPort?: number;
  command?: string[];
}

export type ServerRole = "web" | "database" | "email" | "dns" | "backup";

export type ServerState = "healthy" | "updating" | "agent_update" | "degraded" | "offline" | "rebooting" | "joining";

export type HostUpdatePolicy = "security" | "all" | "manual";

export interface PackageUpdate {
  name: string;
  from: string;
  to: string;
  security: boolean;
  /** A held container tool. Only the "Update container tools" action updates it (FR-1113). */
  containerTool?: boolean;
}

export interface ServerService {
  name: string;
  state: "running" | "stopped" | "failed";
  /** A part that the server can operate without, for example the disk quotas. */
  optional?: boolean;
  /** The reason that the service does not run, and what to do. */
  detail?: string | null;
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
  /** Empty when the server installed no updates yet. */
  lastUpdateAt: string;
  rebootRequired: boolean;
  /** The panel has a newer agent for this server. */
  agentUpdateAvailable?: boolean;
  /** The host and the port that the panel uses to connect to the agent. */
  agentAddress?: string;
  /** The reason when the panel could not connect to the agent. The jobs of the server then fail. */
  connectionError?: string;
  /** This server has the panel. It cannot be removed. */
  panel?: boolean;
  /** The database engines that the agent of the server can operate. An agent of an earlier release has no Valkey. */
  databaseEngines?: DatabaseEngine[];
  /** True when the agent of the server gives each customer its own database containers. */
  databaseInstances?: boolean;
  /** The number of websites that use the shared database containers of an earlier release. */
  sharedDatabaseWebsites?: number;
  services: ServerService[];
  /** The port of the SFTP container of the websites (spec section 10). 22 for an agent of release 0.1. */
  sftpPort: number;
}

export type DatabaseEngine = "mariadb" | "postgresql" | "valkey";

export interface Database {
  id: string;
  name: string;
  engine: DatabaseEngine;
  websiteId: string;
  sizeBytes: number;
  serverId: string;
  /** Valkey only: the database number that the app selects. It is not known until the job that makes the database is complete. */
  number?: number;
  /** Valkey only: the number of keys, from the last report of the server. */
  keys?: number;
}

export type DatabasePrivileges = "readonly" | "readwrite";

/** CF-01: the permission of one database user on one database of the same website. */
export interface DatabaseGrant {
  databaseId: string;
  database: string;
  privileges: DatabasePrivileges;
}

export interface DatabaseUser {
  id: string;
  name: string;
  engine: DatabaseEngine;
  websiteId: string;
  grants: DatabaseGrant[];
  /** FR-502: by default only the website container can connect. */
  access: "website" | "anywhere";
}

export interface Snapshot {
  id: string;
  at: string;
  /** nightly: a scheduled backup ("scheduled" in the API). */
  kind: "nightly" | "manual";
  sizeBytes: number;
  websiteId: string;
  databases: string[];
}

export interface BackupPolicy {
  daily: number;
  weekly: number;
  monthly: number;
  time: string;
  target: string;
  /** The restic environment variables. The panel sends them and never reads them back. */
  credentials?: Record<string, string>;
  /** The names of the credentials that are set. */
  credentialNames?: string[];
  /** The number of manual backups that the panel keeps for each website. */
  manual?: number;
}

/** The certificate method of the panel domain. */
export type PanelCertificateMethod = "internal" | "letsencrypt";

/**
 * The TLS certificate of the panel domain. "internal": the CA of the panel signs it. A browser
 * trusts it after the user adds the root certificate of the CA. "letsencrypt": Let's Encrypt
 * gives it. The domain must be public.
 */
export interface PanelCertificate {
  domain: string;
  /** Null until the agent on the panel server reports. */
  method: PanelCertificateMethod | null;
  /** pending: the edge proxy does not have a certificate of the method yet. */
  state: "valid" | "pending" | "expired";
  expiresAt?: string;
  /** The root certificate of the panel CA in PEM format. */
  caCertificate?: string;
  caSha256?: string;
  reportedAt?: string;
  letsEncryptAvailable: boolean;
  /** The other servers that do not trust the panel CA yet. */
  serversWithoutCa: string[];
  /** The current or the last change of the method. */
  job?: Job;
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

export type TokenScope =
  | "websites:read" | "websites:write"
  | "dns:read" | "dns:write"
  | "email:read" | "email:write"
  | "databases:read" | "databases:write"
  | "backups:read" | "backups:write"
  | "tokens:read" | "tokens:write"
  | "servers:read" | "servers:write"
  | "images:read" | "images:write"
  | "customers:read" | "customers:write"
  | "users:read" | "users:write"
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

export interface AuditEntry {
  id: string;
  at: string;
  actor: string;
  ip: string;
  action: string;
  target: string;
}

export interface SearchResult {
  kind: "website" | "domain" | "job" | "server" | "image" | "customer";
  label: string;
  detail: string;
  href: string;
}

export interface State {
  /**
   * loading: the panel has a session but has not loaded the data yet.
   * mustChangePassword: the user has a temporary password. The panel shows only the form for
   * a new password.
   */
  session: {
    signedIn: boolean; loading?: boolean; mustChangePassword?: boolean; user: User; contextId: string;
    /** The error of the last load from the API. The panel then shows the data of the load before it. */
    loadError?: string;
  };
  contexts: AccountContext[];
  websites: Website[];
  jobs: Job[];
  activity: ActivityItem[];
  notifications: Notification[];
  servers: Server[];
  images: RuntimeImage[];
  rollout?: Rollout;
  databases: Database[];
  databaseUsers: DatabaseUser[];
  snapshots: Snapshot[];
  backupPolicy: BackupPolicy;
  plans: Plan[];
  customers: Customer[];
  /** The users that the signed-in user can see (audit finding B5). */
  users: ManagedUser[];
  tokens: ApiToken[];
  totpEnabled: boolean;
  audit: AuditEntry[];
  /** The host name of the panel, for the join command and the DNS hints. */
  panelDomain: string;
  panelCertificate: PanelCertificate;
  joinToken: string;
  /** The fingerprint of the panel CA for the join command. */
  joinCaSha256?: string;
  /** The installation script of the release of the panel, for the join command. */
  joinInstallUrl?: string;
}
