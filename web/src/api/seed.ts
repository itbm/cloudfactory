// Sample data for the UI until the panel API exists.
// Times are relative to the moment the page loads.

import type {
  DnsRecord,
  DnsZone,
  FileNode,
  Limits,
  Plan,
  Release,
  Server,
  Snapshot,
  State,
  Website,
} from "./types";

export const MINUTE = 60_000;
export const HOUR = 60 * MINUTE;
export const DAY = 24 * HOUR;
export const MB = 1024 ** 2;
export const GB = 1024 ** 3;

const base = Date.now();
const ago = (ms: number) => new Date(base - ms).toISOString();
const ahead = (ms: number) => new Date(base + ms).toISOString();
/** A clock time today (or yesterday if still to come), for example "03:02" for nightly jobs. */
const atClock = (hh: number, mm: number, daysBack = 0) => {
  const d = new Date(base);
  d.setHours(hh, mm, 0, 0);
  if (d.getTime() > base) d.setTime(d.getTime() - DAY);
  return new Date(d.getTime() - daysBack * DAY).toISOString();
};

const limits = (vcpus: number, memGb: number, diskGb: number, ioMb: number, processes: number): Limits => ({
  vcpus,
  memoryBytes: memGb * GB,
  diskBytes: diskGb * GB,
  diskIoBytesPerSecond: ioMb * MB,
  processes,
});

export const plans: Plan[] = [
  { id: "plan_starter", name: "Starter", limits: limits(1, 0.5, 5, 20, 50), websites: 1, mailboxes: 5, databases: 2, phpExtensions: ["opcache", "intl", "gd", "zip"] },
  { id: "plan_standard", name: "Standard", limits: limits(1, 1, 10, 30, 75), websites: 5, mailboxes: 25, databases: 10, phpExtensions: ["opcache", "intl", "gd", "imagick", "zip", "sodium"] },
  { id: "plan_business", name: "Business", limits: limits(2, 2, 20, 50, 100), websites: 25, mailboxes: 100, databases: 50, phpExtensions: ["opcache", "intl", "gd", "imagick", "redis", "sodium", "zip", "bcmath", "soap", "xdebug"] },
];

const PHP_INI = { memory_limit: "256M", upload_max_filesize: "64M", max_execution_time: "60" };

let releaseSeq = 0;
const release = (ref: string, message: string, at: string, live = false, migration = false): Release => ({
  id: `rel_${++releaseSeq}`,
  ref,
  message,
  at,
  state: "succeeded",
  durationSeconds: 30 + ((releaseSeq * 7) % 40),
  live,
  migration,
});

interface SiteInput extends Partial<Website> {
  id: string;
  name: string;
  application: string;
}

function site(input: SiteInput): Website {
  const unix = input.name.split(".")[0].replace(/[^a-z0-9]/g, "").slice(0, 8);
  return {
    customerId: "cus_acme",
    serverId: "srv_web01",
    unixUser: `${unix}_${input.id.slice(-4)}`,
    createdAt: ago(200 * DAY),
    status: "running",
    runtime: { kind: "frankenphp", imageId: "img_fp85", phpVersion: "8.5", workerMode: false, extensions: ["opcache", "intl", "gd", "zip"], phpIni: { ...PHP_INI } },
    planId: "plan_standard",
    limits: plans[1].limits,
    use: { cpuPercent: 3, memoryBytes: 180 * MB, diskBytes: 2.1 * GB, processes: 14, diskIoBytesPerSecond: 0.6 * MB },
    domains: [{ name: input.name, kind: "primary", certificate: { state: "valid", expiresAt: ahead(50 * DAY) }, dnsPointsHere: true }],
    releases: [],
    previews: [],
    cron: [],
    sshKeys: [],
    shellEnabled: false,
    lastChange: { at: ago(6 * DAY), summary: "Plugin updates" },
    managedByTerraform: false,
    ...input,
  };
}

const keys = [
  { id: "key_1", name: "Alex laptop", fingerprint: "SHA256:q3v9…Xk2", addedAt: ago(120 * DAY) },
  { id: "key_2", name: "CI deploy", fingerprint: "SHA256:7hTa…pQ9", addedAt: ago(40 * DAY) },
];

export const websites: Website[] = [
  site({
    id: "web_example",
    name: "example.com",
    unixUser: "example_9c1d",
    application: "WordPress",
    runtime: { kind: "frankenphp", imageId: "img_fp85", phpVersion: "8.5", workerMode: false, extensions: ["opcache", "intl", "gd", "imagick", "zip"], phpIni: { ...PHP_INI } },
    use: { cpuPercent: 6, memoryBytes: 288 * MB, diskBytes: 4.2 * GB, processes: 22, diskIoBytesPerSecond: 1.2 * MB },
    domains: [
      { name: "example.com", kind: "primary", certificate: { state: "valid", expiresAt: ahead(62 * DAY) }, dnsPointsHere: true },
      { name: "www.example.com", kind: "redirect", certificate: { state: "valid", expiresAt: ahead(62 * DAY) }, dnsPointsHere: true },
    ],
    lastChange: { at: ago(3 * DAY), summary: "Plugin updates" },
    cron: [{ id: "cron_1", schedule: "*/15 * * * *", command: "php wp-cron.php", enabled: true, lastRunAt: ago(7 * MINUTE), lastResult: "succeeded" }],
    sshKeys: keys.slice(0, 1),
  }),
  site({
    id: "web_shop",
    name: "shop.example.net",
    application: "WooCommerce",
    unixUser: "shop_7f2a",
    createdAt: "2026-03-14T10:00:00Z",
    planId: "plan_business",
    limits: plans[2].limits,
    runtime: {
      kind: "frankenphp",
      imageId: "img_fp84",
      phpVersion: "8.4",
      workerMode: false,
      extensions: ["opcache", "intl", "gd", "imagick", "redis", "sodium", "zip"],
      phpIni: { memory_limit: "512M", upload_max_filesize: "64M", max_execution_time: "120" },
      previous: { imageId: "img_fp83", phpVersion: "8.3", tag: "1.12.4-php8.3" },
    },
    use: { cpuPercent: 14, memoryBytes: 742 * MB, diskBytes: 6.2 * GB, processes: 38, diskIoBytesPerSecond: 3 * MB },
    domains: [
      { name: "shop.example.net", kind: "primary", certificate: { state: "valid", expiresAt: ahead(41 * DAY) }, dnsPointsHere: true },
      { name: "www.shop.example.net", kind: "redirect", certificate: { state: "valid", expiresAt: ahead(41 * DAY) }, dnsPointsHere: true },
      { name: "shop-old.example.org", kind: "alias", certificate: { state: "pending" }, dnsPointsHere: false, verification: { type: "TXT", name: "_cloudfactory-challenge.shop-old.example.org", value: "cf-k3v9q2m7r4t8p1w6z5r0b2n8c4d7f1g3h6j9l2" } },
    ],
    git: {
      repository: "git@example.com:acme/shop.git",
      branch: "main",
      trigger: "webhook",
      scheduleMinutes: 5,
      buildCommands: ["composer install --no-dev --optimize-autoloader", "npm ci", "npm run build"],
      deployKey: "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIK3…shop_7f2a deploy",
      secrets: ["STRIPE_SECRET_KEY", "MAILER_DSN"],
    },
    releases: [
      release("a1b2c3d", "Fix tax rounding at checkout", ago(2 * HOUR), true),
      release("77e1f02", "Add delivery time estimate", ago(1 * DAY), false, true),
      release("5c9d4aa", "Update payment plugin", ago(3 * DAY)),
      release("0be3317", "Change product grid", ago(6 * DAY)),
      release("e44f8c1", "Fix coupon rounding", ago(9 * DAY)),
    ],
    previews: [
      { url: "https://pr-41.shop.example.net", title: "New delivery options" },
      { url: "https://pr-43.shop.example.net", title: "Upgrade to WooCommerce 10" },
    ],
    cron: [
      { id: "cron_2", schedule: "*/5 * * * *", command: "php wp-cron.php", enabled: true, lastRunAt: ago(3 * MINUTE), lastResult: "succeeded" },
      { id: "cron_3", schedule: "0 4 * * *", command: "php bin/export-orders.php", enabled: true, lastRunAt: atClock(4, 0), lastResult: "failed" },
    ],
    sshKeys: keys,
    lastChange: { at: ago(2 * HOUR), summary: "Deploy", ref: "main · a1b2c3d" },
  }),
  site({
    id: "web_app",
    name: "app.example.com",
    unixUser: "app_4e8b",
    application: "Laravel · worker mode",
    runtime: { kind: "frankenphp", imageId: "img_fp85", phpVersion: "8.5", workerMode: true, extensions: ["opcache", "intl", "redis", "zip", "bcmath"], phpIni: { ...PHP_INI } },
    planId: "plan_business",
    limits: { ...plans[2].limits, memoryBytes: 1 * GB },
    use: { cpuPercent: 9, memoryBytes: 410 * MB, diskBytes: 1.8 * GB, processes: 18, diskIoBytesPerSecond: 0.9 * MB },
    domains: [{ name: "app.example.com", kind: "primary", certificate: { state: "valid", expiresAt: ahead(80 * DAY) }, dnsPointsHere: true }],
    git: { repository: "git@example.com:acme/app.git", branch: "main", trigger: "schedule", scheduleMinutes: 5, buildCommands: ["composer install --no-dev", "php artisan optimize"], deployKey: "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIB7…app deploy", secrets: ["APP_KEY"] },
    releases: [release("9f8e7d6", "Queue retry settings", ago(25 * MINUTE), true), release("1d2e3f4", "Add team invites", ago(2 * DAY), false, true)],
    lastChange: { at: ago(25 * MINUTE), summary: "Deploy", ref: "main · 9f8e7d6" },
    managedByTerraform: true,
  }),
  site({
    id: "web_legacy",
    name: "legacy.example.net",
    // A website of a customer of the reseller Acme.
    customerId: "cus_bakery",
    planId: "plan_starter",
    application: "Custom PHP · uses .htaccess",
    runtime: { kind: "apache-php-fpm", imageId: "img_ap82", phpVersion: "8.2", workerMode: false, extensions: ["opcache", "gd", "zip"], phpIni: { ...PHP_INI } },
    limits: { ...plans[1].limits, memoryBytes: 512 * MB },
    use: { cpuPercent: 2, memoryBytes: 160 * MB, diskBytes: 3.4 * GB, processes: 9, diskIoBytesPerSecond: 0.2 * MB },
    domains: [{ name: "legacy.example.net", kind: "primary", certificate: { state: "valid", expiresAt: ahead(12 * DAY) }, dnsPointsHere: true }],
    lastChange: { at: ago(240 * DAY), summary: "SFTP upload" },
  }),
  site({
    id: "web_api",
    name: "api.example.org",
    application: "Node.js app · own image",
    runtime: { kind: "oci", imageId: "img_node24", workerMode: false, extensions: [], phpIni: {} },
    limits: { ...plans[1].limits, memoryBytes: 512 * MB },
    use: { cpuPercent: 3, memoryBytes: 205 * MB, diskBytes: 0.8 * GB, processes: 6, diskIoBytesPerSecond: 0.3 * MB },
    domains: [{ name: "api.example.org", kind: "primary", certificate: { state: "valid", expiresAt: ahead(55 * DAY) }, dnsPointsHere: true }],
    lastChange: { at: ago(1 * DAY), summary: "Image", ref: "tag v2.3.0" },
  }),
  site({
    id: "web_docs",
    name: "docs.example.org",
    application: "Static site",
    runtime: { kind: "static", imageId: "img_caddy", workerMode: false, extensions: [], phpIni: {} },
    limits: { ...plans[0].limits, memoryBytes: 128 * MB },
    use: { cpuPercent: 0.4, memoryBytes: 18 * MB, diskBytes: 0.2 * GB, processes: 1, diskIoBytesPerSecond: 0.1 * MB },
    domains: [{ name: "docs.example.org", kind: "primary", certificate: { state: "renewed", renewedAt: ago(12 * MINUTE), expiresAt: ahead(90 * DAY) }, dnsPointsHere: true }],
    git: { repository: "https://github.com/acme/docs.git", branch: "main", trigger: "webhook", scheduleMinutes: 5, buildCommands: ["npm ci", "npm run build"], deployKey: "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIC9…docs deploy", secrets: [] },
    releases: [release("41c0ffe", "Add install guide", ago(5 * HOUR), true)],
    lastChange: { at: ago(5 * HOUR), summary: "Deploy", ref: "main · 41c0ffe" },
  }),
  site({
    id: "web_staging",
    name: "staging.example.com",
    application: "Staging copy of example.com",
    status: "stopped",
    stagingOf: "web_example",
    use: { cpuPercent: 0, memoryBytes: 0, diskBytes: 4.1 * GB, processes: 0, diskIoBytesPerSecond: 0 },
    domains: [{ name: "staging.example.com", kind: "primary", certificate: { state: "not_needed" }, dnsPointsHere: true }],
    lastChange: { at: ago(14 * DAY), summary: "Cloned from example.com" },
  }),
  ...["blog", "news", "events", "careers", "status"].map((sub, i) =>
    site({
      id: `web_${sub}`,
      name: `${sub}.example.com`,
      application: "WordPress",
      serverId: i % 2 ? "srv_web02" : "srv_web01",
      use: { cpuPercent: 1 + i, memoryBytes: (120 + i * 30) * MB, diskBytes: (1 + i) * GB, processes: 10 + i, diskIoBytesPerSecond: 0.3 * MB },
      domains: [{ name: `${sub}.example.com`, kind: "primary", certificate: { state: "valid", expiresAt: ahead((30 + i * 9) * DAY) }, dnsPointsHere: true }],
      lastChange: { at: ago((6 + i) * DAY), summary: "Plugin updates" },
    }),
  ),
];

const services = (names: string[]) => names.map((name) => ({ name, state: "running" as const }));

const server = (s: Partial<Server> & Pick<Server, "id" | "name" | "location" | "roles">): Server => ({
  agentVersion: "0.9.4",
  cpuPercent: 10,
  memoryPercent: 40,
  diskPercent: 35,
  state: "healthy",
  os: "Debian 13 (trixie)",
  kernel: "6.12.48-1",
  arch: "x86-64",
  podmanVersion: "5.4.2",
  gvisorVersion: "release-20261013.0",
  gvisorPlatform: "systrap",
  bareMetal: false,
  updatePolicy: "security",
  maintenanceWindow: "02:00 to 05:00 (Europe/London)",
  pendingUpdates: [],
  lastUpdateAt: atClock(6, 25),
  rebootRequired: false,
  services: services(["cloudfactory-agent", "podman", "edge-proxy (Caddy)", "nftables"]),
  ...s,
});

export const servers: Server[] = [
  server({
    id: "srv_web01", name: "web-01", location: "London", roles: ["web", "database"], cpuPercent: 23, memoryPercent: 58, diskPercent: 41,
    bareMetal: true, gvisorPlatform: "kvm",
    services: services(["cloudfactory-agent", "podman", "edge-proxy (Caddy)", "MariaDB 11.8", "PostgreSQL 18", "SFTPGo", "nftables"]),
    pendingUpdates: [{ name: "openssl", from: "3.5.1-1", to: "3.5.1-1+deb13u1", security: true }],
  }),
  server({
    id: "srv_web02", name: "web-02", location: "Manchester", roles: ["web"], cpuPercent: 41, memoryPercent: 66, diskPercent: 52,
    os: "Ubuntu 26.04 LTS", kernel: "6.17.0-14-generic", state: "updating", stateDetail: "Updating 18 of 30 containers",
    services: services(["cloudfactory-agent", "podman", "edge-proxy (Caddy)", "SFTPGo", "nftables"]),
    pendingUpdates: [
      { name: "linux-image-generic", from: "6.17.0-14", to: "6.17.0-15", security: true },
      { name: "curl", from: "8.14.1-2ubuntu1", to: "8.14.1-2ubuntu1.1", security: true },
      { name: "htop", from: "3.4.0-1", to: "3.4.1-1", security: false },
    ],
    rebootRequired: true,
  }),
  server({ id: "srv_mail01", name: "mail-01", location: "London", roles: ["email"], cpuPercent: 12, services: services(["cloudfactory-agent", "podman", "Stalwart mail", "nftables"]) }),
  server({ id: "srv_dns01", name: "dns-01", location: "London", roles: ["dns"], cpuPercent: 2, agentVersion: "0.9.3", state: "agent_update", arch: "arm64", services: services(["cloudfactory-agent", "podman", "PowerDNS", "nftables"]) }),
  server({ id: "srv_dns02", name: "dns-02", location: "Amsterdam", roles: ["dns"], cpuPercent: 1, arch: "arm64", services: services(["cloudfactory-agent", "podman", "PowerDNS", "nftables"]) }),
  server({ id: "srv_backup01", name: "backup-01", location: "Frankfurt", roles: ["backup"], cpuPercent: 6, diskPercent: 71, services: services(["cloudfactory-agent", "podman", "restic", "nftables"]) }),
];

let recordSeq = 0;
const rec = (type: DnsRecord["type"], name: string, value: string, extra: Partial<DnsRecord> = {}): DnsRecord => ({
  id: `rec_${++recordSeq}`,
  type,
  name,
  value,
  ttl: 3600,
  ...extra,
});

const mailRecords = (domain: string): DnsRecord[] => [
  rec("MX", "@", "mail-01.cloudfactory.io.", { priority: 10, managed: "mail" }),
  rec("TXT", "@", "v=spf1 mx include:_spf.cloudfactory.io -all", { managed: "mail" }),
  rec("TXT", "cf2026._domainkey", "v=DKIM1; k=ed25519; p=11qYAYKxCrfVS/7TyWQHOg7hcvPapiMlrwIaaPcHURo=", { managed: "mail" }),
  rec("TXT", "_dmarc", `v=DMARC1; p=quarantine; rua=mailto:dmarc@${domain}`, { managed: "mail" }),
];

const nsRecords = (): DnsRecord[] => [
  rec("NS", "@", "dns-01.cloudfactory.io.", { managed: "ns", ttl: 86400 }),
  rec("NS", "@", "dns-02.cloudfactory.io.", { managed: "ns", ttl: 86400 }),
];

const zone = (name: string, records: DnsRecord[], extra: Partial<DnsZone> = {}): DnsZone => ({
  name,
  customerId: "cus_acme",
  nameservers: ["dns-01.cloudfactory.io", "dns-02.cloudfactory.io"],
  records: [...nsRecords(), ...records],
  managedByTerraform: false,
  ...extra,
});

export const zones: DnsZone[] = [
  zone("example.com", [
    rec("A", "@", "203.0.113.10", { managed: "website" }),
    rec("AAAA", "@", "2001:db8::10", { managed: "website" }),
    rec("CNAME", "www", "example.com.", { managed: "website" }),
    rec("A", "app", "203.0.113.10", { managed: "website" }),
    ...["blog", "news", "events", "careers", "status", "staging"].map((s) => rec("A", s, "203.0.113.10", { managed: "website" })),
    ...mailRecords("example.com"),
    rec("CAA", "@", '0 issue "letsencrypt.org"'),
  ]),
  zone("example.net", [
    rec("A", "shop", "203.0.113.10", { managed: "website" }),
    rec("CNAME", "www.shop", "shop.example.net.", { managed: "website" }),
    rec("A", "legacy", "203.0.113.10", { managed: "website" }),
    ...mailRecords("example.net"),
  ]),
  zone("example.org", [
    rec("A", "api", "203.0.113.10", { managed: "website" }),
    rec("A", "docs", "203.0.113.10", { managed: "website" }),
    rec("A", "shop-old", "198.51.100.77"),
    rec("TXT", "@", "google-site-verification=3m9x0aQ"),
  ], { managedByTerraform: true }),
];

const now = () => ago(0);

const tree = (root: string, entries: [string, number, string?][]): FileNode[] =>
  entries.map(([path, size, content]) => ({
    path: `${root}/${path}`.replace(/\/$/, ""),
    name: path.split("/").filter(Boolean).pop()!,
    type: path.endsWith("/") ? "dir" : "file",
    sizeBytes: size,
    modifiedAt: ago((size % 17) * DAY + (size % 5) * HOUR),
    content,
  }));

const wpFiles = tree("/public", [
  ["wp-admin/", 0],
  ["wp-content/", 0],
  ["wp-content/plugins/", 0],
  ["wp-content/themes/", 0],
  ["wp-content/uploads/", 0],
  ["wp-includes/", 0],
  ["index.php", 405, "<?php\n// Front controller.\ndefine('WP_USE_THEMES', true);\nrequire __DIR__ . '/wp-blog-header.php';\n"],
  ["wp-config.php", 3120, "<?php\ndefine('DB_NAME', 'shop_wp');\ndefine('DB_USER', 'shop_wp');\ndefine('DB_PASSWORD', getenv('DB_PASSWORD'));\ndefine('DB_HOST', 'mariadb.internal');\n"],
  [".htaccess", 523, "# BEGIN WordPress\nRewriteEngine On\nRewriteRule ^index\\.php$ - [L]\n# END WordPress\n"],
  ["robots.txt", 68, "User-agent: *\nDisallow: /wp-admin/\n"],
]);

export const files: Record<string, FileNode[]> = Object.fromEntries(
  websites.map((w) => [
    w.id,
    [
      ...tree("", [["public/", 0], ["logs/", 0], ["tmp/", 0]]),
      ...wpFiles,
      ...tree("/logs", [["access.log", 1_248_000], ["error.log", 18_400]]),
    ],
  ]),
);

export const snapshots: Snapshot[] = websites.flatMap((w) =>
  Array.from({ length: 7 }, (_, d): Snapshot => ({
    id: `snap_${w.id}_${d}`,
    at: atClock(3, 2, d),
    kind: "nightly",
    sizeBytes: Math.round((w.use.diskBytes * 0.22 + d * 3 * MB)),
    websiteId: w.id,
    databases: w.id === "web_shop" ? ["shop_7f2a_wp", "shop_7f2a_reports"] : w.runtime.kind === "frankenphp" ? [`${w.unixUser}_db`] : [],
    mailboxes: w.id === "web_shop" ? ["orders@shop.example.net"] : w.id === "web_example" ? ["hello@example.com"] : [],
  })),
);

export function seed(): State {
  return structuredClone<State>({
    session: {
      signedIn: true,
      user: { id: "usr_1", name: "Alex Morgan", email: "alex@acme.example", role: "administrator" },
      contextId: "cus_acme",
    },
    contexts: [
      { id: "cus_acme", name: "Acme Agency", role: "reseller" },
      { id: "platform", name: "Platform", role: "administrator" },
    ],
    websites,
    jobs: [
      { id: "job_100", title: "Deploy", ref: "a1b2c3d", state: "succeeded", steps: ["Fetch", "Build", "Switch release", "Health check"], step: 4, createdAt: ago(2 * HOUR + MINUTE), finishedAt: ago(2 * HOUR), websiteId: "web_shop" },
      { id: "job_099", title: "Nightly backup (1.4 GB)", state: "succeeded", steps: ["Snapshot files", "Dump databases", "Upload"], step: 3, createdAt: atClock(3, 0), finishedAt: atClock(3, 2), websiteId: "web_shop" },
      { id: "job_098", title: "Renew certificate", state: "succeeded", steps: ["ACME order", "Install"], step: 2, createdAt: ago(3 * DAY), finishedAt: ago(3 * DAY), websiteId: "web_shop" },
      { id: "job_097", title: "Deploy", ref: "9f8e7d6", state: "succeeded", steps: ["Fetch", "Build", "Switch release", "Health check"], step: 4, createdAt: ago(26 * MINUTE), finishedAt: ago(25 * MINUTE), websiteId: "web_app" },
    ],
    activity: [
      { id: "act_3", text: "Certificate renewed for docs.example.org", at: ago(12 * MINUTE) },
      { id: "act_2", text: "Deployed app.example.com", ref: "9f8e7d6", at: ago(25 * MINUTE) },
      { id: "act_1", text: "Nightly backup finished for all websites (3.2 GB)", at: atClock(3, 4) },
    ],
    notifications: [
      { id: "ntf_3", tone: "danger", text: "oldshop.example.org rolled back to the previous image after a failed health check.", at: ago(18 * MINUTE), read: false, href: "/updates" },
      { id: "ntf_2", tone: "warning", text: "Cron job php bin/export-orders.php failed on shop.example.net.", at: atClock(4, 0), read: true, href: "/websites/web_shop/cron" },
      { id: "ntf_1", tone: "success", text: "Nightly backup finished for all websites (3.2 GB).", at: atClock(3, 4), read: true, href: "/backups" },
    ],
    servers,
    images: [
      { id: "img_fp85", name: "FrankenPHP · PHP 8.5", family: "frankenphp", phpVersion: "8.5", reference: "ghcr.io/cloudfactory/frankenphp", running: "1.12.4", available: "1.12.5", availableNote: "rolling_out", websites: "64", policy: "security_automatic", custom: false, signatureVerified: true, digest: "sha256:91ab0c…77d2" },
      { id: "img_fp84", name: "FrankenPHP · PHP 8.4", family: "frankenphp", phpVersion: "8.4", reference: "ghcr.io/cloudfactory/frankenphp", running: "1.12.4", available: "1.12.5", availableNote: "queued", websites: "41", policy: "security_automatic", custom: false, signatureVerified: true, digest: "sha256:3f9c2e…a21e" },
      { id: "img_fp83", name: "FrankenPHP · PHP 8.3", family: "frankenphp", phpVersion: "8.3", supportEndsAt: "2027-12-31", reference: "ghcr.io/cloudfactory/frankenphp", running: "1.12.4", available: "1.12.5", websites: "12", policy: "manual", custom: false, signatureVerified: true, digest: "sha256:c02d11…9b0f" },
      { id: "img_fp82", name: "FrankenPHP · PHP 8.2", family: "frankenphp", phpVersion: "8.2", supportEndsAt: "2026-12-31", reference: "ghcr.io/cloudfactory/frankenphp", running: "1.12.4", websites: "3", policy: "manual", custom: false, signatureVerified: true, digest: "sha256:4e7a90…0c31" },
      { id: "img_ap82", name: "Apache + PHP-FPM · PHP 8.2", family: "apache-php-fpm", phpVersion: "8.2", supportEndsAt: "2026-12-31", reference: "ghcr.io/cloudfactory/apache-php", running: "2026.10", websites: "7", policy: "manual", policyNote: "Email owners about upgrading", custom: false, signatureVerified: true, digest: "sha256:aa51e3…12f8" },
      { id: "img_ap84", name: "Apache + PHP-FPM · PHP 8.4", family: "apache-php-fpm", phpVersion: "8.4", reference: "ghcr.io/cloudfactory/apache-php", running: "2026.10", websites: "2", policy: "security_automatic", custom: false, signatureVerified: true, digest: "sha256:0d2c5b…e7a4" },
      { id: "img_node24", name: "Node.js 24", family: "node", reference: "docker.io/library/node", running: "24 LTS", websites: "5", policy: "automatic", custom: true, signatureVerified: true, digest: "sha256:5b1d77…4c09" },
      { id: "img_caddy", name: "Edge proxy · Caddy", family: "edge", reference: "ghcr.io/cloudfactory/caddy", running: "2.11.4", websites: "All", policy: "automatic", policyNote: "One server at a time", custom: false, signatureVerified: true, digest: "sha256:e8f3a2…61bc" },
      { id: "img_stalwart", name: "Mail · Stalwart", family: "mail", reference: "ghcr.io/cloudfactory/stalwart", running: "0.16", websites: "All mail", policy: "manual", custom: false, signatureVerified: true, digest: "sha256:7c40de…aa17" },
      { id: "img_pdns", name: "DNS · PowerDNS", family: "dns", reference: "ghcr.io/cloudfactory/powerdns", running: "5.0.1", websites: "All zones", policy: "automatic", policyNote: "One DNS server at a time", custom: false, signatureVerified: true, digest: "sha256:12bb90…d3e5" },
      { id: "img_mariadb", name: "MariaDB 11.8", family: "database", reference: "ghcr.io/cloudfactory/mariadb", running: "11.8.3", websites: "All databases", policy: "manual", custom: false, signatureVerified: true, digest: "sha256:9a0e61…5f22" },
    ],
    rollout: {
      id: "rol_42",
      imageId: "img_fp85",
      title: "FrankenPHP 1.12.5 with PHP 8.5",
      fromTag: "1.12.4",
      toTag: "1.12.5",
      severity: "security",
      state: "running",
      startedAt: atClock(1, 58),
      window: "02:00 to 05:00 (Europe/London)",
      websites: 64,
      servers: 2,
      stages: [
        { name: "Canary", websites: 1, state: "done", note: "1 website, watched for 30 minutes" },
        { name: "10%", websites: 6, state: "done" },
        { name: "50%", websites: 32, state: "in_progress" },
        { name: "Everyone", websites: 64, state: "waiting" },
      ],
      healthy: 31,
      rolledBack: 1,
      rollbacks: [
        { website: "oldshop.example.org", reason: "returned HTTP 500 after the update. It went back to the previous image after 40 seconds and is serving normally." },
      ],
      checks: [
        { label: "Signed with the Cloudfactory release key", passed: true },
        { label: "Scan: 0 critical, 0 high", passed: true },
        { label: "SBOM attached", passed: true },
        { label: "Health check: HTTP 200 and error rate under 1% for 5 minutes", passed: null },
      ],
    },
    zones,
    mailboxes: [
      { id: "mbx_1", address: "hello@example.com", quotaBytes: 5 * GB, usedBytes: 4.6 * GB },
      { id: "mbx_2", address: "orders@shop.example.net", quotaBytes: 10 * GB, usedBytes: 4 * GB },
      { id: "mbx_3", address: "support@example.org", quotaBytes: 5 * GB, usedBytes: 0.9 * GB },
      { id: "mbx_4", address: "alex@example.com", quotaBytes: 5 * GB, usedBytes: 1.2 * GB },
    ],
    aliases: [
      { id: "als_1", kind: "alias", source: "info@example.com", destinations: ["hello@example.com"] },
      { id: "als_2", kind: "forwarder", source: "billing@shop.example.net", destinations: ["accounts@acme.example", "orders@shop.example.net"] },
    ],
    databases: [
      { id: "db_1", name: "shop_7f2a_wp", engine: "mariadb", websiteId: "web_shop", sizeBytes: 820 * MB, serverId: "srv_web01" },
      { id: "db_5", name: "shop_7f2a_staging", engine: "mariadb", websiteId: "web_shop", sizeBytes: 790 * MB, serverId: "srv_web01" },
      { id: "db_2", name: "shop_7f2a_reports", engine: "postgresql", websiteId: "web_shop", sizeBytes: 140 * MB, serverId: "srv_web01" },
      { id: "db_3", name: "example_9c1d_wp", engine: "mariadb", websiteId: "web_example", sizeBytes: 310 * MB, serverId: "srv_web01" },
      { id: "db_4", name: "app_4e8b_main", engine: "postgresql", websiteId: "web_app", sizeBytes: 1.1 * GB, serverId: "srv_web01" },
    ],
    databaseUsers: [
      { id: "dbu_1", name: "shop_7f2a_wp", engine: "mariadb", websiteId: "web_shop", grants: [{ databaseId: "db_1", database: "shop_7f2a_wp", privileges: "readwrite" }], access: "website" },
      // CF-01: one user with a different permission on each database.
      { id: "dbu_5", name: "shop_7f2a_sync", engine: "mariadb", websiteId: "web_shop", grants: [{ databaseId: "db_1", database: "shop_7f2a_wp", privileges: "readonly" }, { databaseId: "db_5", database: "shop_7f2a_staging", privileges: "readwrite" }], access: "website" },
      { id: "dbu_2", name: "shop_7f2a_reports", engine: "postgresql", websiteId: "web_shop", grants: [{ databaseId: "db_2", database: "shop_7f2a_reports", privileges: "readwrite" }], access: "website" },
      { id: "dbu_3", name: "example_9c1d_wp", engine: "mariadb", websiteId: "web_example", grants: [{ databaseId: "db_3", database: "example_9c1d_wp", privileges: "readwrite" }], access: "website" },
      { id: "dbu_4", name: "app_4e8b_app", engine: "postgresql", websiteId: "web_app", grants: [{ databaseId: "db_4", database: "app_4e8b_main", privileges: "readwrite" }], access: "website" },
    ],
    files,
    snapshots,
    backupPolicy: { daily: 7, weekly: 4, monthly: 6, time: "03:00", target: "s3:backups.fra1.example-storage.com/cloudfactory", credentialNames: ["AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY"] },
    plans,
    customers: [
      { id: "cus_acme", name: "Acme Agency", email: "ops@acme.example", planId: "plan_business", role: "reseller", createdAt: ago(300 * DAY), suspended: false },
      { id: "cus_bakery", name: "Hill Street Bakery", email: "owner@hillstreet.example", planId: "plan_starter", role: "owner", resellerId: "cus_acme", createdAt: ago(90 * DAY), suspended: false },
      { id: "cus_studio", name: "North Studio", email: "dev@northstudio.example", planId: "plan_standard", role: "owner", resellerId: "cus_acme", createdAt: ago(45 * DAY), suspended: false },
      { id: "cus_lawson", name: "Lawson & Co", email: "it@lawson.example", planId: "plan_business", role: "owner", createdAt: ago(400 * DAY), suspended: true },
    ],
    branding: { name: "Acme Hosting", colour: "#1d5fd6", panelDomain: "panel.acme.example", supportEmail: "support@acme.example" },
    tokens: [
      { id: "tok_1", name: "Terraform (production)", scopes: ["websites:read", "websites:write", "dns:read", "dns:write", "databases:write"], createdAt: ago(60 * DAY), expiresAt: ahead(120 * DAY), lastUsedAt: ago(3 * HOUR), prefix: "cf_live_7Hq2" },
      { id: "tok_2", name: "WHMCS billing", scopes: ["websites:read", "websites:write"], createdAt: ago(200 * DAY), expiresAt: ahead(10 * DAY), lastUsedAt: ago(1 * DAY), prefix: "cf_live_k9Pe" },
    ],
    passkeys: [
      { id: "pk_1", name: "MacBook Touch ID", addedAt: ago(100 * DAY), lastUsedAt: ago(2 * HOUR) },
      { id: "pk_2", name: "YubiKey 5C", addedAt: ago(100 * DAY) },
    ],
    totpEnabled: true,
    audit: [
      { id: "aud_6", at: ago(25 * MINUTE), actor: "CI deploy key", ip: "198.51.100.23", action: "Deployed", target: "app.example.com 9f8e7d6" },
      { id: "aud_5", at: ago(2 * HOUR), actor: "Alex Morgan", ip: "203.0.113.45", action: "Deployed", target: "shop.example.net a1b2c3d" },
      { id: "aud_4", at: ago(3 * HOUR), actor: "Terraform (production)", ip: "192.0.2.80", action: "Updated DNS record", target: "example.org A docs" },
      { id: "aud_3", at: ago(1 * DAY), actor: "Alex Morgan", ip: "203.0.113.45", action: "Changed image", target: "api.example.org tag v2.3.0" },
      { id: "aud_2", at: ago(2 * DAY), actor: "Sam Patel", ip: "203.0.113.61", action: "Created mailbox", target: "support@example.org" },
      { id: "aud_1", at: ago(3 * DAY), actor: "Alex Morgan", ip: "203.0.113.45", action: "Signed in with passkey", target: "MacBook Touch ID" },
    ],
    alertChannels: [
      { id: "alc_1", kind: "email", target: "ops@acme.example", events: ["Server health", "Failed update", "Failed backup", "Mailbox 90% full"] },
      { id: "alc_2", kind: "webhook", target: "https://hooks.acme.example/cloudfactory", events: ["Server health", "Failed update", "Failed deploy"] },
    ],
    system: { registry: "ghcr.io/cloudfactory", registryMirror: "", panelDomain: "panel.cloudfactory.io", timezone: "Europe/London" },
    joinToken: "cfj_3kV9xQ2mL7pR8tW1",
  });
}

export const nowIso = now;
