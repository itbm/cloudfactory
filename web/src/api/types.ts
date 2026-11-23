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

/** "node" and "python": the container runs the start command of the website. */
/** "app" is a runtime from a manifest of the agent, for example Go. It runs the start command of the website. */
export type RuntimeKind = "frankenphp" | "apache-php-fpm" | "node" | "python" | "oci" | "static" | "app";

/** The version of PHP of an image, for example "8.5". A PHP pack adds older versions, for example "8.2" (docs/packs.md). */
export type PhpVersion = string;

export interface Runtime {
  kind: RuntimeKind;
  imageId: string;
  phpVersion?: PhpVersion;
  /** The version of Node.js or Python, for example "24" or "3.14". */
  version?: string;
  workerMode: boolean;
  extensions: string[];
  phpIni: Record<string, string>;
  /** The previous image, kept so that the owner can go back in one action (FR-303). */
  previous?: { imageId: string; phpVersion?: PhpVersion; tag: string };
  /** The directory in /app that the web server serves. "." is /app. The default is "public". */
  documentRoot?: string;
  /** The digest of the image that the website runs. Empty when the panel knows the image by tag only. */
  digest?: string;
  signatureVerified?: boolean;
}

export type CertificateState = "valid" | "renewed" | "pending" | "failed" | "not_needed";

export interface Certificate {
  state: CertificateState;
  expiresAt?: string;
  renewedAt?: string;
  /** "automatic": the edge proxy gets the certificate. Otherwise a certificate of the customer from the panel. */
  source?: CertificateSource;
  /** The certificate of the customer that the domain uses (source "upload" or "acme"). */
  certificateId?: string;
}

/** Where the certificate of a domain comes from (docs/certificates.md). */
export type CertificateSource = "automatic" | "upload" | "acme";

/**
 * issuing: the first order runs. failed: the first order failed. renewing: an order for a new
 * certificate runs, and the current certificate stays in use.
 */
export type CustomerCertificateState = "issuing" | "failed" | "renewing" | "expired" | "valid";

/** The stage of an order with DNS validation. */
export type CertificateOrderStage = "new" | "dns" | "validating" | "finalizing";

/**
 * A certificate of a customer: a certificate that the customer uploaded, or a certificate that
 * the panel gets from Let's Encrypt with DNS validation ("acme"). Each domain of a website of the
 * customer uses the best certificate that is valid for it.
 */
export interface CustomerCertificate {
  id: string;
  customerId: string;
  source: "upload" | "acme";
  names: string[];
  state: CustomerCertificateState;
  issuer?: string;
  fingerprint?: string;
  notBefore?: string;
  expiresAt?: string;
  /** False when browsers do not trust the certificate, for example a self-signed certificate. */
  trusted?: boolean;
  /** Only for "acme": the time of the next renewal. */
  renewAt?: string;
  /** The order that runs, with the TXT records of the DNS validation. */
  order?: { stage: CertificateOrderStage; startedAt?: string; records: { name: string; value: string }[] };
  /** The error of the last order. */
  error?: string;
  /** The time of the next try of a renewal that failed. */
  nextTryAt?: string;
  /** The domains that use the certificate now, from the reports of the servers. */
  usedBy: { domain: string; websiteId?: string; websiteName?: string; serverName: string }[];
  createdAt: string;
  /** The certificate chain in PEM format, after the panel read the certificate. */
  certificatePem?: string;
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
  /** The traffic of a website in a calendar month. 0 or none: no limit. */
  bandwidthBytes?: number;
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

export type WebsiteKind = "public" | "private" | "worker";

export interface Website {
  id: string;
  name: string;
  application: string;
  customerId: string;
  serverId: string;
  unixUser: string;
  /** The name of the website for a different website in a network with it, for example "shop-a1b2.internal". Null for a worker. */
  networkHost: string | null;
  /** The port that the website answers on in its networks. Null for a worker. */
  networkPort: number | null;
  /**
   * "public": a website with domains. "private": a website with no domain, only for the
   * websites of its networks. "worker": a background worker that serves no HTTP.
   */
  kind: WebsiteKind;
  /** The start command of a worker, or of a Node.js or Python website. */
  command: string | null;
  /** The install command of a Node.js or Python website, or null for no install step. */
  installCommand: string | null;
  /** The names of the environment variables. The API never gives the values. */
  environment: string[];
  createdAt: string;
  status: WebsiteStatus;
  runtime: Runtime;
  planId: string;
  limits: Limits;
  /**
   * False when the server of the website does not apply the disk limit of each website: its
   * file system has no user quotas, and a website has no disk image. No value when the server
   * did not report it.
   */
  diskLimitApplied?: boolean;
  use: ResourceUse;
  /** The traffic of the website in the current calendar month (UTC), in and out. */
  bandwidthBytes: number;
  domains: Domain[];
  /** The SFTP access from the panel API. */
  sftp?: SftpAccess;
  lastChange: LastChange;
  /** A staging copy points to its source website (FR-205). */
  stagingOf?: string;
  /** A preview website points to its pull request and to the website of the Git source. */
  previewOf?: PreviewOf;
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
  /** The action of the agent, for example CreateDatabase. */
  action?: string;
  state: JobState;
  steps: string[];
  step: number;
  error?: string;
  createdAt: string;
  finishedAt?: string;
  websiteId?: string;
  serverId?: string;
  /** The database server that the job makes, changes or removes. */
  databaseServerId?: string;
  /** The DNS zone that the job sends to a DNS server. */
  dnsZoneId?: string;
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
  /** A built-in family, or the name of a runtime from a manifest of the agent, for example "golang". */
  family: "frankenphp" | "apache-php-fpm" | "node" | "python" | "static" | "edge" | "mail" | "dns" | "database" | "custom" | (string & {});
  reference: string;
  running: string;
  available?: string;
  availableNote?: "rolling_out" | "queued";
  phpVersion?: PhpVersion;
  /** The version of Node.js or Python of an official image, for example "24". */
  version?: string;
  /** The usual start command and install command of an application image. */
  defaults?: { command: string; install: string };
  /**
   * The pack of an image of a runtime from a pack, for example Go (docs/packs.md). Its websites
   * run their start command, as Node.js websites do.
   */
  pack?: PackRef;
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
  /** The mail server of this server. Undefined when email is off. */
  mail?: ServerMail;
  /** The host and the port that the panel uses to connect to the agent. */
  agentAddress?: string;
  /** The reason when the panel could not connect to the agent. The jobs of the server then fail. */
  connectionError?: string;
  /** This server has the panel. It cannot be removed. */
  panel?: boolean;
  /** The database engines that the agent of the server can operate. An agent of an earlier release has no Valkey. */
  databaseEngines?: DatabaseEngine[];
  /** True when the agent of the server can operate the database servers of customers. */
  databaseInstances?: boolean;
  services: ServerService[];
  /** The port of the SFTP container of the websites (spec section 10). 22 for an agent of release 0.1. */
  sftpPort: number;
  /** The host name of the server as a name server, for example ns1.example.com. */
  dnsName?: string;
  /** True when the agent of the server can operate the DNS server. */
  dns: boolean;
  /** The public IP addresses that the agent of the server reported. */
  addresses: string[];
}

export type DnsRecordType = "A" | "AAAA" | "CNAME" | "MX" | "TXT" | "SRV" | "CAA" | "NS";

export interface DnsRecord {
  id: string;
  /** The name in the zone: "@" for the zone itself, "www", "*.shop". */
  name: string;
  type: DnsRecordType;
  value: string;
  /** In seconds. */
  ttl: number;
  /** Only an MX or an SRV record has a priority. */
  priority?: number;
  /** A record that the panel made: a name server of the zone, the address of a website, or a TXT record of the DNS validation of a certificate while the order runs. */
  managed?: "ns" | "website" | "acme";
}

/** updating: a DNS server does not have the last change yet. failed: a DNS server could not load the zone. */
export type DnsZoneStatus = "active" | "updating" | "failed" | "removing";

/**
 * What the public DNS says about the zone. here: the registrar sends the queries to the name
 * servers of the panel. elsewhere: to other name servers. none: the domain has no name servers.
 */
export type DnsDelegation = "here" | "elsewhere" | "none" | "unknown";

/** A DS record: the digest of a DNSSEC key, for the registry of the domain. */
export interface DsRecord { keyTag: number; algorithm: number; digestType: number; digest: string; record: string }

/**
 * The steps of a change of the DNSSEC key (a rollover with double signatures).
 * publish: the DNS servers publish the new key. submit: the customer gives the new DS record to
 * the registrar. retire: both keys sign the zone, then the old key goes.
 */
export type DnssecRolloverStep = "publish" | "submit" | "retire";

export interface DnssecRollover {
  step: DnssecRolloverStep;
  startedAt?: string;
  /** The time when the step can end. Undefined in the step "submit", and in the step "publish" until each DNS server has the new key. */
  until?: string;
  newDs: DsRecord;
  oldDs: DsRecord;
}

/**
 * ok: a DS record at the registry is for a key of the zone. none: the registry has no DS record,
 * and resolvers do not check the signatures. other: no DS record is for a key of the zone, and
 * resolvers that check DNSSEC refuse the answers. unknown: the lookup failed.
 */
export interface DnssecParent {
  state: "ok" | "none" | "other" | "unknown";
  /** The key tags of the keys of the zone that the registry has a DS record for. */
  keyTags: number[];
  /** The key tags of the other DS records at the registry. */
  otherKeyTags: number[];
  checkedAt?: string;
}

export interface DnsZone {
  id: string;
  name: string;
  customerId: string;
  status: DnsZoneStatus;
  serial: number;
  recordCount: number;
  nameServers: string[];
  delegation: { state: DnsDelegation; nameServers: string[]; checkedAt?: string };
  dnssec: {
    enabled: boolean;
    /** pending: the external DNS provider makes the key. The DS record comes after that. */
    state: "off" | "pending" | "active";
    /** The DS record that the customer gives to the registrar of the domain. During a change of the key, the DS record of the new key. */
    ds?: DsRecord;
    dnskey?: string;
    /** A change of the key on the DNS servers of the panel. Undefined when the key does not change. */
    rollover?: DnssecRollover;
    /** The DS records of the zone at the registry, from the public DNS. Undefined until the panel checks, and for a zone at an external DNS provider. */
    parent?: DnssecParent;
  };
  /** The state of the zone on each name server of the panel. */
  servers: { nameServer: string; state: "ok" | "pending" | "failed" | "removed" }[];
  /** The external DNS provider of the zone. Undefined for a zone on the DNS servers of the panel. */
  provider?: DnsZoneProvider;
  /** The zone transfers to the secondary DNS servers of the owner. Undefined for a zone at an external DNS provider. */
  transfers?: DnsTransfers;
  /** The number of websites of the customer with a domain in the zone. */
  websites: number;
  createdAt: string;
  /** Undefined until the panel has read the records of the zone. */
  records?: DnsRecord[];
}

export type TsigAlgorithm = "hmac-sha256" | "hmac-sha512";

/** A TSIG key that signs zone transfers. */
export interface TsigKey {
  name: string;
  algorithm: TsigAlgorithm;
  /** The secret in base64. Only the "secret" routes of the API give it. */
  secret?: string;
}

/** The zone transfers of one zone to the secondary DNS servers of its owner. */
export interface DnsTransfers {
  /** Empty when the zone has no transfers. */
  addresses: string[];
  notify: boolean;
  /** The host names of the secondaries. The zone has an NS record for each one. */
  nameServers: string[];
  tsig?: TsigKey;
  /** The addresses of the DNS servers of the panel. The secondaries copy the zone from these. */
  primaries: string[];
}

/** A secondary DNS server of the panel. It copies all zones. */
export interface DnsSecondary {
  id: string;
  name: string;
  addresses: string[];
  /** The host name of the secondary. Each zone has an NS record for it. */
  nameServer?: string;
  tsig?: TsigKey;
  notify: boolean;
  /** True when the secondary also copies the catalog zone. */
  catalog: boolean;
  createdAt: string;
}

/** The secondary DNS servers of the panel. Only an administrator has them. */
export interface DnsSecondaries {
  data: DnsSecondary[];
  catalogZone: string;
  /** The DNS servers of the panel, which the secondaries copy the zones from. */
  primaries: { nameServer: string; addresses: string[] }[];
  /** The state of the list on each DNS server of the panel. */
  servers: { server: string; state: "ok" | "pending" | "failed" }[];
}

/** The state of a zone at its external DNS provider. */
export interface DnsZoneProvider {
  accountId: string;
  accountName: string;
  /** The name of the provider, for example Cloudflare. */
  name: string;
  /** The last change at the provider. */
  state: "ok" | "pending" | "failed";
  error?: string;
  /** True when the provider answers for the zone. Some providers wait until the registrar has their name servers. */
  active: boolean;
  syncedAt?: string;
  changes?: { added: number; changed: number; removed: number };
  /** The records that the panel copied from the provider when the zone came there. */
  imported?: { added: number; existing: number; skipped: { line: number; text: string; code: string; reason: string }[] };
  /** The DNS servers of the panel still answer for the zone that moved to the provider. */
  handover?: { endsAt?: string };
}

/** An external DNS provider that a plugin offers (contract cf-dns-provider/1). */
export interface DnsProvider {
  pluginId: string;
  pluginName: string;
  id: string;
  name: string;
  /** True when the provider can sign the zones. */
  dnssec: boolean;
  types: DnsRecordType[];
  credentials: { id: string; label: string; secret: boolean; optional: boolean; hint: string }[];
}

/** An account at an external DNS provider. An account with no customer belongs to the panel. */
export interface DnsAccount {
  id: string;
  name: string;
  /** Undefined for an account of the panel. Each customer can use it. */
  customerId?: string;
  pluginId: string;
  provider: string;
  providerName: string;
  /** False when the plugin is off, or does not offer the provider now. */
  available: boolean;
  /** The name of the account at the provider. */
  account?: string;
  dnssec: boolean;
  types: DnsRecordType[];
  /** The values of the fields that are not secret. A secret field is null. */
  credentials: Record<string, string | null>;
  /** The zones that use the account. */
  zones: number;
  checkedAt?: string;
  createdAt: string;
}

/** The built-in database engines. */
export type BuiltInDatabaseEngine = "mariadb" | "postgresql" | "valkey";

/** A built-in engine, or the slug of a database pack (cf-database/1, docs/packs.md). */
export type DatabaseEngine = BuiltInDatabaseEngine | (string & {});

/**
 * A database engine of the panel, with its features. A database pack adds an engine: its
 * database servers get a version of the pack, and have no move, no Adminer and no access from
 * anywhere.
 */
export interface DatabaseEngineInfo {
  engine: DatabaseEngine;
  name: string;
  /** The port of the engine. A website connects to a database server of a pack on this port, by its network name. */
  port: number;
  /** The database pack of the engine. Null for a built-in engine. */
  pack: { id: string; slug: string; version: string; publisher: string } | null;
  /** The versions of a pack engine, newest first. A new database server gets the first one by default. Empty for a built-in engine. */
  versions: { version: string; supportEndsAt: string | null }[];
  /** True when the engine has users with access to more than one database (SQL). */
  users: boolean;
  moves: boolean;
  adminer: boolean;
  anywhere: boolean;
}

/** The limits of the container of a database server. */
export interface DatabaseLimits {
  vcpus: number;
  memoryBytes: number;
  diskBytes: number;
}

// Email
//
// A customer has mail domains. Each mail domain has mailboxes, and addresses that send their
// mail to other addresses: aliases and forwarders. The mail server of a server has the mail
// of all customers on that server.

export type MailStatus = "pending" | "active" | "failed" | "removing";

/** A DNS record that the owner of a mail domain adds. */
export interface MailRecord {
  type: string;
  name: string;
  value: string;
  /** "mx", "spf", "dkim" or "dmarc". */
  purpose: string;
  /** Null before the first check of the DNS. */
  found: boolean | null;
}

export interface MailDomain {
  id: string;
  name: string;
  customerId: string;
  serverId: string;
  status: MailStatus;
  /** Mail programs connect to this host name: IMAP on port 993, SMTP on port 465 or 587. */
  mailHost: string;
  records: MailRecord[];
  /** True when the last check found all records. */
  dnsReady: boolean;
  dnsCheckedAt?: string;
  mailboxes: number;
  aliases: number;
}

export interface Mailbox {
  id: string;
  address: string;
  localPart: string;
  domainId: string;
  customerId: string;
  status: MailStatus;
  quotaBytes: number;
  /** The size of the messages, from the last report of the server. */
  usedBytes: number;
  /** Not null: the mail server encrypts each new message with the OpenPGP key of the owner. */
  encryption?: MailEncryption | null;
  /**
   * Not null: the panel made the OpenPGP keys of the mailbox and has the private key, in a form
   * that only a password of the mailbox opens. "passwordCurrent" is false when the private key
   * has an older password.
   */
  privateKey?: { passwordCurrent: boolean } | null;
}

/** What the mail server found in the OpenPGP public key of a mailbox. */
export interface MailEncryption {
  fingerprints: string[];
  /** For example "OpenPGP v4: RSA-3072, X25519". */
  keyType: string;
  expiresAt: string | null;
  /** For example "expiresSoon", "weakRsa" or "addressMismatch". */
  warnings: string[];
}

export interface MailAlias {
  id: string;
  address: string;
  localPart: string;
  domainId: string;
  customerId: string;
  /** alias: the destinations are mailboxes of the customer. forwarder: they are other addresses. */
  kind: "alias" | "forwarder";
  destinations: string[];
  status: MailStatus;
}

/** The mail server of a server. An administrator turns it on. */
export interface ServerMail {
  hostname: string;
  status: "starting" | "running" | "failed";
  domains: number;
  /** Set when the server is a member of this mail cluster. The cluster controls its mail server. */
  clusterId?: string | null;
}

/** The step of a member in the setup of its mail cluster. */
export type MailClusterStep = "new" | "prepared" | "joined" | "running";

/**
 * A mail cluster: 3 or 5 servers with the email role. Each member has all the mail, so the
 * mail service continues when one server fails. Only an administrator sees the clusters.
 */
export interface MailCluster {
  id: string;
  name: string;
  /** The shared name. The MX records of the mail domains use it. */
  hostname: string;
  status: "setting_up" | "running" | "failed";
  phase: "prepare" | "bootstrap" | "join" | "first" | "rest" | "done";
  error: string | null;
  members: {
    serverId: string;
    serverName: string;
    serverState: string;
    number: number;
    hostname: string;
    endpoint: string;
    address: string;
    region: number;
    step: MailClusterStep;
  }[];
  records: { type: "A" | "AAAA"; name: string; value: string }[];
  domains: number;
  createdAt: string | null;
}

/**
 * starting: the panel makes the database server. failed: the panel could not make it.
 * removing: the panel removes it, and it gets no new database.
 */
export type DatabaseServerStatus = "starting" | "running" | "stopped" | "failed" | "moving" | "removing";

/**
 * A database server of a customer: one container with one engine and its own limits. All the
 * websites of the customer on the same server can connect to it.
 */
export interface DatabaseServer {
  id: string;
  name: string;
  engine: DatabaseEngine;
  /** The version of the engine of a database pack, for example "8.4". Null for a built-in engine. */
  engineVersion: string | null;
  customerId: string;
  /** The server that has the container. */
  serverId: string;
  /** The server that receives the database server, while its status is "moving". */
  movingToServerId?: string | null;
  /** The plan gives the limits and the number of databases. */
  planId: string;
  status: DatabaseServerStatus;
  /** The host name and the port that a website container on the same server connects to. */
  host: string;
  port: number;
  /**
   * The network name and its port, for example "shop-db.internal" and 3306. Each website in a
   * network with the database server connects to them, on the same server and on a different
   * server. Null for a database server with a name that an engine uses.
   */
  networkHost: string | null;
  networkPort: number;
  limits: DatabaseLimits;
  /** The last report of the server. diskSizeBytes is the size of the file system of the data. */
  usage?: { memoryBytes?: number; diskBytes?: number; diskSizeBytes?: number };
  databases: number;
  /** The number of databases that the plan of the database server allows. */
  maxDatabases: number;
  createdAt: string;
}

export interface Database {
  id: string;
  name: string;
  engine: DatabaseEngine;
  websiteId: string;
  sizeBytes: number;
  serverId: string;
  /** The database server of the customer that has the database. */
  databaseServerId?: string;
  /** The host name and the port that the app of the website connects to. */
  host?: string;
  port?: number;
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
  /** A user is in one database server. It can have access only to the databases in it. */
  databaseServerId?: string;
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
  /**
   * website: the files and the databases on the server of the website.
   * databases: only the databases in the database servers on a different server. The backup
   * is in the repository of that server.
   */
  part: "website" | "databases";
  /** The server whose repository has the snapshot. */
  serverId?: string;
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
  /** Administrators only: each server that has a backup repository, and whether it has its own key. */
  servers?: ServerStorageKey[];
}

/**
 * Second audit, finding F08: the object storage key of one server. A server with no
 * credential names uses the shared key of the backup policy.
 */
export interface ServerStorageKey {
  serverId: string;
  /** The name of the server. */
  server: string;
  /** The restic repository of the server. The key of the server must have access to this path only. */
  repository: string | null;
  credentialNames: string[];
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
  /** The number of mailboxes of a customer with the plan. */
  mailboxes: number;
  /** The total size of the mailboxes of a customer with the plan. */
  mailStorageBytes: number;
  /** The number of databases of each database server with the plan. */
  databases: number;
  phpExtensions: string[];
  /** The number of database servers of a customer with the plan. */
  databaseServers: number;
  /** The limits of each database server with the plan. */
  databaseLimits: DatabaseLimits;
  /** The number of DNS zones of a customer with the plan. */
  dnsZones: number;
  /**
   * The number of named private networks of a customer with the plan, in addition to the
   * network "default". 0: the customer has only the network "default". An earlier panel API
   * does not give it.
   */
  networks?: number;
}

/**
 * A private network of a customer, like a VPC. Each website in the network connects to each
 * other member by its network name, on the same server and on a different server. Each
 * customer has the network "default".
 */
export interface PrivateNetwork {
  id: string;
  customerId: string;
  name: string;
  isDefault: boolean;
  websiteIds: string[];
  databaseServerIds: string[];
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
  /** White label: the brand that a reseller set. Only a reseller has one. */
  branding?: ResellerBranding;
  brandStatus?: BrandStatus;
}

/**
 * White label: the brand that a person sees. The administrator sets the default brand. The
 * customers of a reseller see only the brand of the reseller.
 */
export interface Brand {
  name: string;
  /** The colour of the buttons and links. White text on it has a contrast of 4.5:1 or more. */
  colour: string;
  /** A data URL of a PNG, JPEG or WebP image, or null. */
  logo: string | null;
  supportEmail: string | null;
  /** True when the panel has the built-in brand: the panel shows the built-in logo. */
  builtIn: boolean;
  source?: "panel" | "reseller";
  panelDomain?: string;
  nameServers?: string[];
  mailHost?: string | null;
}

/** The fields of a brand that a person sets. A missing field has the built-in value. */
export interface BrandFields {
  name?: string;
  colour?: string;
  logo?: string;
  supportEmail?: string;
}

export interface ResellerBranding extends BrandFields {
  panelDomain?: string;
  /** One host name for each name server of the panel, in the order of the panel host names. */
  nameServers?: string[];
  mailHost?: string;
}

export interface BrandCertificate {
  domain: string;
  method: "letsencrypt" | "internal";
  state: "valid" | "pending" | "expired";
  expiresAt: string | null;
  error: string | null;
  checkedAt: string | null;
}

export interface BrandStatus {
  /** The TLS certificate of the panel domain of the reseller. Null with no panel domain. */
  certificate: BrandCertificate | null;
  /** The domain of the panel. A CNAME record of the reseller panel domain can point to it. */
  panelDomain: string;
  /** The host names of the name servers of the panel. */
  panelNameServers: string[];
  /** The addresses of each name server of the panel. */
  nameServerAddresses: Record<string, string[]>;
}

export type TokenScope =
  | "websites:read" | "websites:write"
  | "dns:read" | "dns:write"
  | "mail:read" | "mail:write"
  | "databases:read" | "databases:write"
  | "backups:read" | "backups:write"
  | "tokens:read" | "tokens:write"
  | "plugins:read" | "plugins:write"
  | "servers:read" | "servers:write"
  | "images:read" | "images:write"
  | "customers:read" | "customers:write"
  | "users:read" | "users:write"
  | "audit:read"
  | "notifications:read" | "notifications:write";

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

/** One item of the setup checklist of the panel (GET /setup). */
export interface SetupItem {
  id: "backups" | "totp" | "disk-limits" | "panel-certificate";
  title: string;
  /** not_applicable: the item does not apply to this panel. */
  state: "done" | "open" | "not_applicable";
  /** What occurs while the item is open. */
  effect: string;
  /** The current state, in words. */
  detail: string;
  action: { label: string; target: string };
}

export interface SetupChecklist {
  complete: boolean;
  open: number;
  items: SetupItem[];
}

/** The account of the signed-in user (GET /account). An administrator has no account. */
export interface Account {
  customer: { id: string; name: string; email: string; role: "reseller" | "owner"; suspended: boolean; createdAt: string };
  /** The reseller that gives the service. Undefined: the operator of the panel gives it. */
  provider?: { name: string; email: string };
  plan?: Plan;
  usage: { websites: number; databases: number; diskBytes: number; customers?: number; dnsZones: number; networks: number; bandwidthBytes: number };
  /** False when the panel has no object storage for backups. A backup then fails. */
  backupsAvailable: boolean;
  users: ManagedUser[];
}

/** Second audit, finding F09: a change to an administrator account that a different person made. */
export interface SecurityNotice {
  action: string;
  target: string;
  actor: string;
  at: string;
}

/** The password and the TOTP code of the signed-in user, to confirm a change to an administrator. */
export interface Confirmation {
  password: string;
  totp: string;
}

/** One server in the list of the mesh. */
export interface MeshServer {
  id: string;
  name: string;
  location: string | null;
  state: Server["state"];
  /** False for an agent of an earlier release: the server cannot have tunnels. */
  supported: boolean;
  /** False for an agent from before the networks: it cannot operate network members. */
  networks: boolean;
  publicKey: string | null;
  /** The address of the server in the mesh. A server gets it with its first tunnel. */
  address: string | null;
  /** The public IP address for the tunnel traffic. Null: the panel finds it. */
  tunnelAddress: string | null;
  tunnels: number;
  /** True when the server has the mesh state that the panel wants. */
  current: boolean;
}

/** What one server of a tunnel reported about the tunnel. */
export interface MeshTunnelSide {
  serverId: string;
  server: string | null;
  /** Null: the server did not report the tunnel in the last minutes. */
  reachable: boolean | null;
  handshakeAt: string | null;
  roundTripMs: number | null;
}

/** An encrypted tunnel between two servers. */
export interface MeshTunnel {
  id: string;
  state: "up" | "down" | "waiting";
  roundTripMs: number | null;
  /** True when an administrator made the tunnel. False when the panel made it for a network. */
  pinned: boolean;
  /** True when a website on one server uses a database server on the other. */
  inUse: boolean;
  sides: MeshTunnelSide[];
  createdAt: string | null;
}

/** The mesh: the encrypted tunnels between the servers of the panel. */
export interface Mesh {
  /** The address range of the mesh, for example "10.207.0.0/16". */
  range: string;
  /** The UDP port of the tunnels. */
  port: number;
  servers: MeshServer[];
  tunnels: MeshTunnel[];
}

export interface State {
  /**
   * loading: the panel has a session but has not loaded the data yet.
   * mustChangePassword: the user has a temporary password. The panel shows only the form for
   * a new password.
   * totpSetupRequired: the user is an administrator or a reseller with no TOTP. The panel
   * shows only the page that sets up TOTP (second audit, finding F09).
   */
  /** White label: the brand that the signed-in user sees. */
  brand?: Brand;
  session: {
    signedIn: boolean; loading?: boolean; mustChangePassword?: boolean; totpSetupRequired?: boolean; user: User; contextId: string;
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
  databaseServers: DatabaseServer[];
  /** The built-in database engines and the engines of the database packs. */
  databaseEngines: DatabaseEngineInfo[];
  mailDomains: MailDomain[];
  mailClusters: MailCluster[];
  mailboxes: Mailbox[];
  mailAliases: MailAlias[];
  /** The DNS zones of the customers that the user can see. */
  dnsZones: DnsZone[];
  /** The certificates of the customers that the user can see. */
  certificates: CustomerCertificate[];
  /** The host names of the DNS servers of the panel. Empty: the panel has no DNS server. */
  nameServers: string[];
  /** The accounts at external DNS providers that the user can use. */
  dnsAccounts: DnsAccount[];
  /** The external DNS providers that the plugins that are on offer. */
  dnsProviders: DnsProvider[];
  /** The secondary DNS servers of the panel. Only an administrator has them. */
  dnsSecondaries?: DnsSecondaries;
  /** The mesh. Only an administrator has it. */
  mesh?: Mesh;
  /** The private networks of the customers that the user can see. */
  networks: PrivateNetwork[];
  /** The pages of the plugins that are on, for the role of the user. */
  pluginPages: PluginPage[];
  /** The actions of the plugins that are on, for the role of the user. */
  pluginActions: PluginAction[];
  /** The open usage alerts that the user can see. */
  usageAlerts: UsageAlert[];
  /** The thresholds of the usage alerts, in percent of each limit. */
  alertSettings: UsageAlertSettings;
  databases: Database[];
  databaseUsers: DatabaseUser[];
  /**
   * For each website, the IDs of the database servers on a different server that the website
   * can use. The website connects to them by their network names.
   */
  remoteDatabaseServers: Record<string, string[]>;
  snapshots: Snapshot[];
  backupPolicy: BackupPolicy;
  plans: Plan[];
  customers: Customer[];
  /** The users that the signed-in user can see (audit finding B5). */
  users: ManagedUser[];
  tokens: ApiToken[];
  totpEnabled: boolean;
  /** Changes that a different person made to the administrator accounts in the last 7 days. */
  securityNotices: SecurityNotice[];
  audit: AuditEntry[];
  /** The host name of the panel, for the join command and the DNS hints. */
  panelDomain: string;
  panelCertificate: PanelCertificate;
  /** The setup checklist. Only an administrator has it. */
  setup?: SetupChecklist;
  /** The account of the user. An administrator has none. */
  account?: Account;
  joinToken: string;
  /** The fingerprint of the panel CA for the join command. */
  joinCaSha256?: string;
  /** The installation script of the release of the panel, for the join command. */
  joinInstallUrl?: string;
}

// Files, logs, cron jobs and deploys of a website. The panel reads them when the user opens
// their page. They are not in the state of the store.

/** A file or a directory in the files of a website. A path starts with "/", the website directory. */
export interface FileEntry {
  name: string;
  path: string;
  type: "file" | "directory" | "link" | "other";
  sizeBytes: number;
  modifiedAt: string;
  mode: string;
  /** The target of a link. */
  target?: string | null;
}

export interface FileList {
  path: string;
  entries: FileEntry[];
  /** True when the directory has more entries than the panel shows. */
  truncated: boolean;
}

export interface FileContent {
  file: FileEntry;
  encoding: "utf8" | "base64";
  content: string;
}

export interface LogLine {
  at: string;
  stream: "access" | "error";
  text: string;
}

export type FirewallMode = "off" | "detect" | "block";

/** The protection of a website in the edge proxy. The API never gives the passwords. */
export interface WebsiteSecurity {
  firewall: { mode: FirewallMode; excludedRules: number[]; skippedPaths: string[] };
  ipRules: { allow: string[]; block: string[] };
  protectedPaths: { path: string; users: { name: string }[] }[];
  signInLimit: { requestsPerMinute: number; paths: string[] };
}

/** A change of the protection. Each part replaces the part that the website has. */
export interface WebsiteSecurityChange {
  firewall?: { mode: FirewallMode; excludedRules?: number[]; skippedPaths?: string[] };
  ipRules?: { allow?: string[]; block?: string[] };
  /** A user with no password keeps the password that the same user of the same path has. */
  protectedPaths?: { path: string; users: { name: string; password?: string | null }[] }[];
  signInLimit?: { requestsPerMinute: number; paths?: string[] };
}

/** A request that the firewall blocked or detected. */
export interface FirewallEvent {
  at: string | null;
  clientIp: string;
  /** Only an event that the firewall blocked has the host. */
  host: string | null;
  uri: string;
  action: "blocked" | "detected";
  score: number;
  rules: { id: number; message: string; data: string }[];
}

export interface CronJob {
  id: string;
  schedule: string;
  command: string;
  enabled: boolean;
  lastRunAt: string | null;
  lastResult: "ok" | "failed" | null;
  lastExitCode: number | null;
  lastOutput: string | null;
}

/** One entry of the command log of a website: a command that a user ran with "Run a command". */
export interface WebsiteCommand {
  id: string;
  websiteId: string;
  source: "run";
  command: string;
  /** "done": the command ran and has an exit code. "failed": the command did not start. */
  state: "running" | "done" | "failed";
  exitCode: number | null;
  /** True when the server stopped the command at its time limit. */
  timedOut: boolean;
  timeoutSeconds: number | null;
  /** Who ran the command. A user who must not see the person gets a role name in its place. */
  actor: string;
  ip: string | null;
  jobId: string | null;
  createdAt: string;
  startedAt: string | null;
  finishedAt: string | null;
  /** The end of the output. Only the answer for one entry has it. */
  output?: string | null;
}

/** A page of the command log, the newest command first. */
export interface CommandLogPage {
  data: WebsiteCommand[];
  meta: { currentPage: number; lastPage: number; total: number };
}

/** How a deploy starts: by hand, with a signed webhook, or with a check of the branch on a schedule. */
export type DeployTrigger = "manual" | "webhook" | "schedule";

export interface GitSource {
  repository: string;
  branch: string;
  /** The directory in the repository that the web server serves. */
  webDirectory: string;
  trigger: DeployTrigger;
  /** The interval of the branch check, for the trigger "schedule". */
  checkMinutes: number;
  /** The last branch check, and its error. */
  checkedAt: string | null;
  checkError: string | null;
  buildCommands: string[];
  /** A command that changes the database. It runs before the switch. */
  migrationCommand: string | null;
  /** The public key that gives read access to the repository. */
  deployKey: string;
  /** Only a token that can change websites gets the address and the secret. */
  webhookUrl: string | null;
  webhookSecret: string | null;
  /** True when a push came during a deploy. The panel deploys it next. */
  deployQueued: boolean;
  /** The names of the secrets. The API does not give the values. */
  secrets: string[];
}

export interface Release {
  id: string;
  branch: string;
  commit: string | null;
  message: string | null;
  state: "building" | "ready" | "failed" | "removed";
  live: boolean;
  trigger: DeployTrigger;
  /** True when the deploy ran the migration command. */
  migration: boolean;
  /** The failed health check after a migration. The website kept this release. */
  healthError: string | null;
  durationSeconds: number | null;
  error: string | null;
  jobId: string | null;
  createdAt: string;
}

export interface Deploy {
  source: GitSource | null;
  releases: Release[];
}

/** The pull request of a preview website. The panel removes the preview when the pull request closes. */
export interface PreviewOf {
  /** The website of the Git source. */
  websiteId: string;
  number: number;
  branch: string;
  title: string;
  /** The page of the pull request on the Git host. */
  url: string | null;
}

export interface PreviewSettings {
  enabled: boolean;
  /** Each preview gets the name pr-<number>.<domain>. */
  domain: string | null;
  /** The largest number of previews at the same time. */
  limit: number;
  /** True: the previews get the environment variables and the build secrets of the website. */
  copyEnvironment: boolean;
}

export type PreviewState = "creating" | "deploying" | "live" | "failed" | "removing";

/** The preview website of one pull request. */
export interface Preview {
  number: number;
  title: string;
  /** The head branch of the pull request. */
  branch: string;
  url: string | null;
  /** The preview website. Null when the panel could not make it: "error" has the cause. */
  websiteId: string | null;
  name: string | null;
  state: PreviewState;
  /** The last deploy of the preview. */
  release: Release | null;
  error: string | null;
  createdAt: string;
  updatedAt: string;
}

export interface Previews {
  /** Null when the website has no Git source, or is a preview itself. */
  settings: PreviewSettings | null;
  suggestedDomain: string | null;
  maxLimit: number;
  previews: Preview[];
}

// Plugins: programs outside the panel with their own token (docs/plugins.md). The panel
// reads the list of plugins when an administrator opens the Plugins page.

export type PluginEvent =
  | "audit.recorded" | "job.finished"
  | "website.created" | "website.changed" | "website.removed"
  | "domain.added" | "domain.verified" | "domain.removed"
  | "database.created" | "database.removed"
  | "backup.started" | "restore.started" | "deploy.started"
  | "customer.created" | "customer.changed" | "user.created";

/** A before hook: the panel asks the plugin before the change, and the plugin can refuse it. */
export type PluginHookEvent = "website.creating" | "website.removing" | "domain.adding" | "database.creating" | "customer.creating";

export interface PluginHook {
  event: PluginHookEvent;
  path: string;
  /** What the panel does when the plugin does not answer in 5 seconds. */
  onError: "allow" | "refuse";
}

export type PluginActionTarget = "website" | "server" | "customer";

/** A button of a plugin on the page of a website, a server or a customer. */
export interface PluginAction {
  pluginId: string;
  pluginName: string;
  id: string;
  title: string;
  target: PluginActionTarget;
  /** A question that the panel asks before it runs the action. */
  confirm: string | null;
  roles: Role[];
}

/** The answer of a plugin to an action. The URL is on the address of the plugin. */
export interface PluginActionResult {
  message: string;
  url: string | null;
}

/** A page of a plugin. The panel opens it in a frame with the origin of the plugin. */
export interface PluginPage {
  pluginId: string;
  pluginName: string;
  id: string;
  title: string;
  url: string;
  origin: string;
  roles: Role[];
}

/** A public key of a publisher that an administrator trusts for plugins and packs. */
export interface TrustedKey {
  id: string;
  name: string;
  fingerprint: string;
  publicKey: string;
  createdAt: string;
}

export interface PluginDelivery {
  id: string;
  event: PluginEvent | "plugin.test";
  state: "pending" | "delivered" | "failed";
  attempts: number;
  responseStatus: number | null;
  error: string | null;
  payload: { id: string; event: string; createdAt: string; data: Record<string, unknown> };
  createdAt: string;
  deliveredAt: string | null;
}

export interface PluginManifest {
  schema: 1;
  slug: string;
  name: string;
  version: string;
  publisher: string;
  description: string;
  scopes: string[];
  events: PluginEvent[];
  webhook: string | null;
  pages: { id: string; title: string; path: string; roles: Role[] }[];
  actions: { id: string; title: string; target: PluginActionTarget; path: string; roles: Role[]; confirm: string }[];
  hooks: PluginHook[];
  /** An image for the panel to run. The digest is in the signed manifest. */
  image: PluginImage | null;
  commands: { name: string; description: string }[];
}

export interface PluginImage {
  ref: string;
  digest: string;
  port: number;
}

/** Where to run a new plugin: on a host of the administrator, or in a container of the panel. */
export type PluginPlacement = { hosted: false; baseUrl: string } | { hosted: true; serverId: string | null; host: string };

export interface Plugin {
  id: string;
  slug: string;
  name: string;
  version: string;
  publisher: string;
  description: string;
  enabled: boolean;
  baseUrl: string;
  webhookUrl: string | null;
  scopes: string[];
  events: PluginEvent[];
  pages: PluginPage[];
  actions: PluginAction[];
  hooks: PluginHook[];
  image: PluginImage | null;
  /** The container of a plugin that the panel runs, or null for a plugin outside the panel. */
  hosting: { serverId: string | null; serverName: string | null; host: string; status: "starting" | "running" | "stopped" | "failed" | "removing" } | null;
  commands: { name: string; description: string }[];
  signedBy: TrustedKey | null;
  tokenExpiresAt: string | null;
  tokenLastUsedAt: string | null;
  deliveries: { delivered: number; pending: number; failed: number; last: PluginDelivery | null };
  createdAt: string;
  updatedAt: string;
}

/** The answer to an install or to new credentials. The panel shows the token and the secret one time. */
export interface PluginCredentials {
  plugin: Plugin;
  /** Null for a plugin that the panel runs: its container gets the values. */
  token: string | null;
  webhookSecret: string | null;
}

/** The token of a plugin page, for the user who opens it. */
export interface PluginPageSession {
  token: string;
  expiresAt: string;
  scopes: string[];
  page: PluginPage;
  user: { id: number; name: string; role: Role; customerId: string | null };
}

// Usage history and usage alerts. The panel reads the history when the user opens a graph.

export type UsageRange = "24h" | "7d" | "30d" | "90d" | "1y";
export type UsageSubject = "website" | "server" | "database_server";
export type UsageResource = "cpu" | "memory" | "disk" | "bandwidth" | "processes";

/** One point of a graph. A value is null when no report is in the point. */
export interface UsagePoint {
  at: string;
  cpuPercent: number | null;
  cpuMaxPercent: number | null;
  memoryBytes?: number | null;
  memoryMaxBytes?: number | null;
  memoryPercent?: number | null;
  memoryMaxPercent?: number | null;
  diskBytes?: number | null;
  diskPercent?: number | null;
  processes?: number | null;
  bytesIn?: number | null;
  bytesOut?: number | null;
}

export interface UsageHistory {
  range: UsageRange;
  stepMinutes: number;
  from: string;
  to: string;
  points: UsagePoint[];
  limits?: Partial<Limits>;
  bandwidth?: { month: string; bytes: number; limitBytes: number };
  alerts: UsageAlert[];
}

export interface UsageAlert {
  id: string;
  subjectType: UsageSubject;
  subjectId: string;
  subjectName: string | null;
  customerId: string | null;
  serverId: string | null;
  resource: UsageResource;
  level: "warning" | "critical";
  used: number;
  limit: number;
  percent: number;
  usedText: string;
  limitText: string;
  openedAt: string;
  updatedAt: string;
  resolvedAt: string | null;
}

export interface UsageAlertSettings {
  warningPercent: number;
  criticalPercent: number;
}

// Packs (docs/packs.md): signed manifests from publishers that add a website runtime.

export interface PackRef { id: string; slug: string; name: string; version: string }

/**
 * The runtime contract of a pack. "cf-runtime/1" adds a runtime of the type "app", which runs
 * the start command of the website. "cf-php/1" adds versions of PHP to the built-in PHP runtime
 * in "family". The keys of the other contract are empty.
 */
export interface PackRuntime { contract: "cf-runtime/1" | "cf-php/1" | (string & {}); type: "app" | ""; defaultCommand: string; defaultInstall: string; family: string }

/** The key that signed a pack. "release" is true for the release key of Cloudfactory. */
export interface PackSigner { name: string; fingerprint: string | null; release: boolean }

export interface PackImage {
  /** Null for an image of a database pack: it is not a website image. */
  id: string | null;
  name: string;
  version: string;
  reference: string;
  tag: string;
  digest: string;
  /** A new digest from a new version of the pack. A website uses the image, so it waits for a rollout. */
  availableDigest: string | null;
  supportEndsAt: string | null;
  websites: number;
  /** The number of database servers with this version (database packs only). */
  databaseServers: number;
}

/**
 * The engine of a database pack (cf-database/1). Its database servers get the host ports from
 * firstPort to lastPort on the address of each customer.
 */
export interface PackDatabase { contract: string; port: number; dataDir: string; firstPort: number; lastPort: number; databaseServers: number }

/** The state of a pack on a web server: the server loaded it, waits for it, or failed to load it. */
export interface PackServer { id: string; name: string; version: string | null; state: "installed" | "pending" | "failing" }

export interface Pack {
  id: string;
  slug: string;
  name: string;
  version: string;
  publisher: string;
  description: string | null;
  signed: boolean;
  signedBy: PackSigner | null;
  /** runtime: the pack adds a website runtime. database: the pack adds a database engine. */
  kind: "runtime" | "database";
  /** The keys are empty for a database pack. */
  runtime: PackRuntime;
  /** Null for a runtime pack. */
  database: PackDatabase | null;
  images: PackImage[];
  servers: PackServer[];
  installedBy: string | null;
  installedAt: string;
  updatedAt: string;
}

/** A manifest as the panel reads it, before an install. */
export interface PackManifest {
  contract: string;
  kind: "runtime" | "database";
  slug: string;
  name: string;
  version: string;
  publisher: string;
  description: string;
  runtime: PackRuntime;
  database: { contract: string; port: number; dataDir: string } | null;
  images: { version: string; ref: string; repository: string; tag: string; digest: string; supportEndsAt: string | null }[];
}
