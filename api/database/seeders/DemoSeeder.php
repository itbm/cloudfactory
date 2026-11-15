<?php

namespace Database\Seeders;

use App\Enums\AgentAction;
use App\Enums\DomainKind;
use App\Enums\JobState;
use App\Enums\Role;
use App\Models\AgentJob;
use App\Models\AuditEntry;
use App\Models\Customer;
use App\Models\DatabaseServer;
use App\Models\DnsRecord;
use App\Models\DnsZone;
use App\Models\GitSource;
use App\Models\Image;
use App\Models\MeshTunnel;
use App\Models\Network;
use App\Models\NetworkMember;
use App\Models\Plan;
use App\Models\Release;
use App\Models\Rollout;
use App\Models\Server;
use App\Models\Setting;
use App\Models\Snapshot;
use App\Models\UsageAlert;
use App\Models\User;
use App\Models\Website;
use App\Services\Dns\Dnssec;
use App\Services\Dns\Zones;
use App\Services\Networks;
use App\Services\PanelCertificate;
use App\Services\SshKey;
use App\Services\UsageRecorder;
use Illuminate\Database\Seeder;
use Illuminate\Support\Carbon;
use Illuminate\Support\Facades\Crypt;
use Illuminate\Support\Facades\DB;
use Illuminate\Support\Facades\Hash;
use Illuminate\Support\Str;

/**
 * The sample data of the demonstration mode (config/demo.php). The web panel uses this data
 * for development, demonstrations, screenshots and the fixtures of its tests. It has an
 * example of each state that a page can show: a failed website, a server with updates, a
 * degraded server, a suspended customer, a domain that waits for its DNS record.
 *
 * All names end with ".example.com", ".example.org", ".example.net", ".example" or ".test", which are reserved for examples. A website domain ends with a public suffix, as on a real panel: the certificate of a name such as "api.acme.test" comes from the panel CA. The password of
 * each user is "password". Never use this data on a server with customers.
 */
class DemoSeeder extends Seeder
{
    private const MB = 1024 ** 2;

    private const GB = 1024 ** 3;

    public const PASSWORD = 'password';

    /** The agent release of the sample servers. The demonstration mode uses it as the latest release. */
    public const AGENT = '0.1.9';

    /** @var array<string, Plan> */
    private array $plans = [];

    /** @var array<string, Image> */
    private array $images = [];

    /** @var array<string, Server> */
    private array $servers = [];

    /** @var array<string, Customer> */
    private array $customers = [];

    /** @var array<string, Website> */
    private array $websites = [];

    /** @var array<string, DnsZone> */
    private array $zones = [];

    public function run(): void
    {
        $this->plans();
        $this->customersAndUsers();
        $this->images();
        $this->servers();
        $this->websites();
        $this->dns();
        $this->databases();
        $this->usageHistory();
        $this->settleMesh();
        $this->backups();
        $this->cronJobsAndDeploys();
        $this->jobsAndRollout();
        $this->settingsAndAudit();
    }

    private function plans(): void
    {
        $extensions = ['opcache', 'intl', 'gd', 'zip', 'exif', 'bcmath', 'mysqli', 'pdo_mysql', 'pdo_pgsql', 'redis', 'imagick', 'soap'];
        // name, vCPUs, memory (GB), disk (GB), websites, then for the database servers: number, databases in each one, memory (MB), disk (GB), then the DNS zones, the named networks and the bandwidth of each website in a month (GB)
        foreach ([['Starter', 1, 1, 10, 3, 2, 5, 512, 2, 2, 0, 100], ['Business', 2, 2, 40, 10, 6, 20, 1024, 10, 10, 3, 500], ['Agency', 4, 4, 100, 50, 20, 50, 2048, 20, 50, 10, 2000]] as [$name, $cpu, $memory, $disk, $sites, $databaseServers, $databases, $databaseMemory, $databaseDisk, $zones, $networks, $bandwidth]) {
            $this->plans[$name] = Plan::create([
                'name' => $name, 'websites' => $sites, 'mailboxes' => $sites * 10, 'databases' => $databases,
                'database_servers' => $databaseServers, 'dns_zones' => $zones, 'networks' => $networks,
                'database_limits' => ['vcpus' => min($cpu, 2), 'memoryBytes' => $databaseMemory * self::MB, 'diskBytes' => $databaseDisk * self::GB],
                'php_extensions' => $name === 'Starter' ? array_slice($extensions, 0, 8) : $extensions,
                'limits' => ['vcpus' => $cpu, 'memoryBytes' => $memory * self::GB, 'diskBytes' => $disk * self::GB, 'diskIoBytesPerSecond' => 50 * self::MB, 'processes' => 100 * $cpu, 'bandwidthBytes' => $bandwidth * self::GB],
            ]);
        }
    }

    private function customersAndUsers(): void
    {
        $p = $this->plans;
        $c = fn (string $name, string $email, string $role, Plan $plan, array $more = [], int $daysAgo = 30) => tap(new Customer(['name' => $name, 'email' => $email, 'role' => $role, 'plan_id' => $plan->id, ...$more]), function (Customer $customer) use ($daysAgo) {
            $customer->created_at = now()->subDays($daysAgo);
            $customer->save();
        });
        $this->customers['northwind'] = $c('Northwind Agency', 'ops@northwind.example.com', 'reseller', $p['Agency'], ['plan_ids' => [$p['Starter']->id, $p['Business']->id]], 300);
        $reseller = ['reseller_id' => $this->customers['northwind']->id];
        $this->customers['bakery'] = $c('Corner Bakery', 'hello@cornerbakery.example.org', 'owner', $p['Starter'], $reseller, 120);
        $this->customers['books'] = $c('Harbour Books', 'it@harbourbooks.example.net', 'owner', $p['Business'], $reseller, 45);
        $this->customers['acme'] = $c('Acme Ltd', 'it@acme.example.com', 'owner', $p['Business'], [], 400);
        $this->customers['lawson'] = $c('Lawson & Co', 'office@lawson.example.org', 'owner', $p['Starter'], ['suspended' => true, 'suspended_by' => 'administrator'], 200);

        $password = Hash::make(self::PASSWORD);
        $u = fn (string $name, string $email, Role $role, ?Customer $customer = null) => User::create(['name' => $name, 'email' => $email, 'password' => $password, 'role' => $role, 'customer_id' => $customer?->id]);
        $u('Alex Admin', 'admin@example.com', Role::Administrator);
        $u('Priya Operations', 'operations@example.com', Role::Administrator);
        $u('Sam Reseller', 'reseller@example.com', Role::Reseller, $this->customers['northwind']);
        $u('Olu Owner', 'owner@example.com', Role::Owner, $this->customers['acme']);
        $u('Morgan Finance', 'finance@acme.example.com', Role::Owner, $this->customers['acme']);
        $u('Bea Baker', 'bea@cornerbakery.example.org', Role::Owner, $this->customers['bakery'])->forceFill(['must_change_password' => true])->save();
        $u('Hari Books', 'hari@harbourbooks.example.net', Role::Owner, $this->customers['books']);
        $u('Lee Lawson', 'lee@lawson.example.org', Role::Owner, $this->customers['lawson']);
    }

    private function images(): void
    {
        $registry = config('cloudfactory.registry');
        $digest = fn (string $seed) => 'sha256:'.hash('sha256', 'cloudfactory-demo-'.$seed);
        $official = fn (string $key, string $name, string $family, string $php, string $path, string $ends, array $more = []) => $this->images[$key] = Image::create([
            'name' => $name, 'family' => $family, 'php_version' => $php, 'reference' => "{$registry}/{$path}", 'tag' => '0.1.9', 'digest' => $digest($path.':0.1.9'),
            'support_ends_at' => $ends, 'policy' => 'security_automatic', 'signature_verified' => true, 'checked_at' => now()->subMinutes(20), ...$more,
        ]);
        // A new release waits for the administrator: the Updates page shows it.
        $official('frankenphp-8.4', 'FrankenPHP · PHP 8.4', 'frankenphp', '8.4', 'frankenphp-php8.4', '2028-12-31', ['available_tag' => '0.1.10', 'available_digest' => $digest('frankenphp-php8.4:0.1.10')]);
        $official('frankenphp-8.5', 'FrankenPHP · PHP 8.5', 'frankenphp', '8.5', 'frankenphp-php8.5', '2029-12-31');
        // FR-306: the panel warns 90 days before the security support of a PHP version stops.
        $official('apache-8.4', 'Apache + PHP-FPM · PHP 8.4', 'apache-php-fpm', '8.4', 'apache-php8.4', now()->addDays(60)->toDateString(), ['policy' => 'manual']);
        $official('apache-8.5', 'Apache + PHP-FPM · PHP 8.5', 'apache-php-fpm', '8.5', 'apache-php8.5', '2029-12-31');
        // FR-307: an image that an administrator added. It has a tag only, and no signature. The
        // last check of its registry failed, so the panel does not know if it is up to date.
        $this->images['node'] = Image::create([
            'name' => 'Node.js app', 'family' => 'node', 'reference' => 'registry.example/acme/storefront', 'tag' => 'stable', 'policy' => 'manual',
            'custom' => true, 'signature_verified' => false, 'container_port' => 3000, 'command' => ['node', 'server.js'], 'checked_at' => now()->subMinutes(20),
            'check_error' => 'The registry registry.example refused the request: 401 Unauthorized.',
        ]);
    }

    private function servers(): void
    {
        $running = fn (array $names) => array_map(fn (string $n) => ['name' => $n, 'state' => 'running'], $names);
        $services = $running(['edge', 'cf-sftp', 'firewall', 'podman']);
        $common = [
            'agent_version' => self::AGENT, 'os' => 'Debian 13', 'kernel' => '6.12.48', 'arch' => 'x86-64', 'podman_version' => '5.4.2',
            'gvisor_version' => 'release-20261019.0', 'pending_updates' => [], 'last_seen_at' => now(), 'connection_checked_at' => now(), 'sftp_port' => 2222,
            'database_engines' => ['mariadb', 'postgresql', 'valkey'], 'database_instances' => true,
        ];
        // Two servers answer DNS queries. The customers give their host names to the registrar of a domain.
        $dns = fn (int $n) => ['dns' => true, 'dns_name' => "ns{$n}.panel.example", 'addresses' => ["203.0.113.{$n}0", "2001:db8:{$n}0::1"]];
        $dnsService = [['name' => 'cf-dns', 'state' => 'running']];
        // The panel server. Its disk quotas are off, and it has websites from before the disk images.
        $this->servers['web-1'] = Server::create([
            ...$common, 'name' => 'web-1', 'location' => 'London', 'roles' => ['web', 'database', 'dns'], 'state' => 'healthy', 'local' => true, 'agent_url' => 'https://web-1.panel.example:7443', ...$dns(1),
            'services' => [...$services, ...$dnsService, ['name' => 'disk-quotas', 'state' => 'stopped', 'optional' => true, 'detail' => 'User quotas are off on the file system of the websites. Websites with a disk limit and no disk image: 5 of 5. This server does not apply the disk limit of these websites. Only a new website gets a disk image. Refer to "Disk quotas" in the installation guide.']],
            'usage' => ['cpuPercent' => 23, 'memoryPercent' => 58, 'diskPercent' => 41], 'last_update_at' => now()->subDays(3),
        ]);
        // Updates wait, and the last update needs a restart.
        $this->servers['web-2'] = Server::create([
            ...$common, 'name' => 'web-2', 'location' => 'Amsterdam', 'roles' => ['web', 'database', 'dns'], 'state' => 'healthy', 'os' => 'Ubuntu 26.04 LTS', 'kernel' => '6.17.0-8', 'arch' => 'arm64',
            'podman_version' => '5.6.0', 'agent_url' => 'https://web-2.panel.example:7443', 'reboot_required' => true, 'update_policy' => 'manual', ...$dns(2),
            'services' => [...$services, ...$dnsService, ['name' => 'disk-quotas', 'state' => 'running', 'optional' => true]],
            'pending_updates' => [
                ['name' => 'openssl', 'from' => '3.5.1-1', 'to' => '3.5.1-2', 'security' => true],
                ['name' => 'libc6', 'from' => '2.42-1', 'to' => '2.42-2', 'security' => true],
                ['name' => 'curl', 'from' => '8.15.0-1', 'to' => '8.15.0-2', 'security' => false],
                ['name' => 'podman', 'from' => '5.6.0', 'to' => '5.6.1', 'security' => false, 'containerTool' => true],
            ],
            'usage' => ['cpuPercent' => 9, 'memoryPercent' => 34, 'diskPercent' => 22], 'last_update_at' => now()->subDays(12),
        ]);
        // A necessary service does not run, and the agent is from an earlier release.
        $this->servers['web-3'] = Server::create([
            ...$common, 'name' => 'web-3', 'location' => 'Frankfurt', 'roles' => ['web'], 'state' => 'degraded', 'agent_version' => '0.1.8', 'agent_url' => 'https://web-3.panel.example:7443', 'addresses' => ['203.0.113.30'],
            'services' => [
                ...$running(['edge', 'firewall', 'podman']),
                ['name' => 'cf-sftp', 'state' => 'failed', 'detail' => 'The container stopped with exit code 125: the port 2222 is in use.'],
                ['name' => 'disk-quotas', 'state' => 'running', 'optional' => true],
            ],
            'usage' => ['cpuPercent' => 4, 'memoryPercent' => 19, 'diskPercent' => 12],
        ]);
        $this->mesh();
    }

    /**
     * One tunnel between the first two servers. The third server has an agent of an earlier
     * release, which reports no tunnel key, so it cannot have a tunnel.
     */
    private function mesh(): void
    {
        [$one, $two] = [$this->servers['web-1'], $this->servers['web-2']];
        $key = fn (string $name) => base64_encode(hash('sha256', 'demo tunnel key of '.$name, true));
        $one->update(['mesh_public_key' => $key('web-1'), 'mesh_address' => '10.207.0.1', 'mesh_endpoint' => '203.0.113.10', 'mesh_version' => 1, 'mesh_wanted_version' => 1, 'mesh_networks' => true, 'mesh_websites' => true, 'mesh_moves' => true]);
        $two->update(['mesh_public_key' => $key('web-2'), 'mesh_address' => '10.207.0.2', 'mesh_endpoint' => '203.0.113.20', 'mesh_version' => 1, 'mesh_wanted_version' => 1, 'mesh_networks' => true, 'mesh_websites' => true, 'mesh_moves' => true]);
        [$a, $b] = strcmp($one->id, $two->id) < 0 ? [$one, $two] : [$two, $one];
        $side = fn (int $micros) => ['reachable' => true, 'handshakeAt' => now()->subSeconds(40)->getTimestamp(), 'roundTripMicros' => $micros, 'reportedAt' => now()->getTimestamp()];
        MeshTunnel::create(['server_a_id' => $a->id, 'server_b_id' => $b->id, 'preshared_key' => $key('web-1 and web-2'), 'pinned' => true, 'status' => [$a->id => $side(7400), $b->id => $side(7550)]]);
    }

    /**
     * The websites and the database servers of the demo data changed the mesh state of the
     * servers. In the demo data, each server has the state already.
     */
    private function settleMesh(): void
    {
        AgentJob::where('action', AgentAction::SetMesh->value)->delete();
        foreach (Server::whereNotNull('mesh_public_key')->get() as $server) {
            $server->update(['mesh_version' => $server->mesh_wanted_version]);
        }
    }

    private function websites(): void
    {
        $key = 'ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIGh0dHBzOi8vY2xvdWRmYWN0b3J5LmV4YW1wbGUvZGVtbw olu@laptop';
        // name, application, customer, server, image, status, days since creation, use (CPU %, memory MB, disk MB, processes), more
        $rows = [
            ['acme.example.com', 'WordPress', 'acme', 'web-1', 'frankenphp-8.4', 'running', 380, [18, 412, 6200, 24], ['sftp_enabled' => true, 'sftp_keys' => [$key]]],
            ['shop.acme.example.com', 'WooCommerce', 'acme', 'web-1', 'frankenphp-8.4', 'running', 210, [61, 1730, 21400, 58], []],
            ['api.acme.example.com', 'Laravel', 'acme', 'web-1', 'frankenphp-8.5', 'running', 95, [9, 300, 840, 12], ['worker_mode' => true]],
            ['storefront.acme.example.com', 'Node.js app', 'acme', 'web-2', 'node', 'running', 20, [5, 210, 380, 9], []],
            ['old.acme.example.com', 'Website', 'acme', 'web-1', 'apache-8.4', 'stopped', 390, [0, 0, 1900, 0], []],
            ['northwind.example.com', 'WordPress', 'northwind', 'web-2', 'frankenphp-8.5', 'running', 290, [12, 380, 3100, 19], []],
            ['cornerbakery.example.org', 'WordPress', 'bakery', 'web-2', 'apache-8.4', 'running', 110, [7, 260, 9100, 14], ['sftp_enabled' => true, 'sftp_keys' => [$key]]],
            ['harbourbooks.example.net', 'WooCommerce', 'books', 'web-2', 'frankenphp-8.4', 'running', 40, [22, 640, 12800, 31], []],
            // The panel could not make this website: the page shows the cause and "Try again".
            ['events.harbourbooks.example.net', 'Laravel', 'books', 'web-3', 'frankenphp-8.5', 'failed', 0, [0, 0, 0, 0], []],
            ['lawson.example.org', 'WordPress', 'lawson', 'web-1', 'apache-8.5', 'stopped', 190, [0, 0, 2400, 0], ['stopped_by_suspension' => true]],
            // A private website has no domain: the other websites of Acme connect to it by its
            // network name. A worker runs a command and serves no HTTP. The name of each one
            // is its network name, which the panel makes from the short name.
            ['orders', 'Orders API', 'acme', 'web-2', 'frankenphp-8.5', 'running', 12, [4, 180, 210, 8], ['kind' => Website::PRIVATE]],
            ['queue', 'Queue worker', 'acme', 'web-1', 'frankenphp-8.5', 'running', 12, [3, 140, 90, 5], ['kind' => Website::WORKER, 'command' => 'php artisan queue:work --tries=3']],
        ];
        foreach ($rows as [$name, $application, $customer, $server, $image, $status, $days, $use, $more]) {
            $owner = $this->customers[$customer];
            $plan = $owner->plan;
            $unixUser = self::unixUser($name);
            $public = ! isset($more['kind']);
            if (! $public) {
                $name = Networks::websiteName($unixUser).Networks::DOMAIN;
            }
            $website = new Website([
                'name' => $name, 'application' => $application, 'customer_id' => $owner->id, 'server_id' => $this->servers[$server]->id, 'plan_id' => $plan->id,
                'image_id' => $this->images[$image]->id, 'unix_user' => $unixUser, 'status' => $status,
                'extensions' => $image === 'node' ? [] : ['opcache', 'intl', 'gd', 'zip'], 'php_ini' => $image === 'node' ? [] : ['memory_limit' => '256M', 'upload_max_filesize' => '64M'],
                'limits' => $plan->limits, 'document_root' => $application === 'Laravel' ? 'public' : '.',
                'usage' => ['cpuPercent' => $use[0], 'memoryBytes' => $use[1] * self::MB, 'diskBytes' => $use[2] * self::MB, 'processes' => $use[3]],
                ...$more,
            ]);
            $website->created_at = now()->subDays($days);
            $website->updated_at = now()->subHours(min($days * 24, 30));
            $website->save();
            $this->websites[$name] = $website;
            if (! $public) {
                continue;
            }
            $failed = $status === 'failed';
            $website->domains()->create([
                'name' => $name, 'kind' => DomainKind::Primary, 'verification_token' => 'cf-demo', 'verified_at' => now(), 'dns_points_here' => ! $failed,
                'certificate_state' => $failed ? 'pending' : 'valid', 'certificate_expires_at' => $failed ? null : now()->addDays(20 + strlen($name) * 2),
            ]);
            $this->websites[$name] = $website;
        }

        $domain = fn (string $website, array $values) => $this->websites[$website]->domains()->create(['verification_token' => 'cf-demo', 'verified_at' => now(), ...$values]);
        $domain('acme.example.com', ['name' => 'www.acme.example.com', 'kind' => DomainKind::Redirect, 'dns_points_here' => true, 'certificate_state' => 'valid', 'certificate_expires_at' => now()->addDays(64)]);
        // CF-03: the edge proxy serves a domain only after the customer proved it with a DNS record.
        $domain('acme.example.com', ['name' => 'acme-outlet.example.com', 'kind' => DomainKind::Alias, 'verification_token' => 'cf-'.substr(hash('sha256', 'acme-outlet.example.com'), 0, 40), 'verified_at' => null]);
        // The DNS of this domain does not point to the server yet.
        $domain('cornerbakery.example.org', ['name' => 'cornerbakery-orders.example.org', 'kind' => DomainKind::Alias, 'dns_points_here' => false, 'certificate_state' => 'pending']);
        // A name for a private network gets a certificate from the panel CA, not from Let's Encrypt.
        $domain('api.acme.example.com', ['name' => 'api.acme.test', 'kind' => DomainKind::Alias, 'dns_points_here' => true, 'certificate_state' => 'valid', 'certificate_expires_at' => now()->addDays(80)]);
    }

    /**
     * DNS hosting: a zone with DNSSEC, a zone that waits for the registrar, and a zone that
     * one DNS server did not take. Each zone has the records of the websites of its customer.
     */
    private function dns(): void
    {
        $servers = Zones::servers();
        $elsewhere = ['ns1.registrar.example', 'ns2.registrar.example'];
        // name, customer, days since creation, name servers in the public DNS (null: the panel), DNSSEC, the DNS server that failed
        $rows = [
            ['acme.example.com', 'acme', 370, null, true, null],
            // The registrar of this domain does not send the queries to the panel yet.
            ['acme-outlet.example.com', 'acme', 0, $elsewhere, false, null],
            ['northwind.example.com', 'northwind', 280, null, false, null],
            ['cornerbakery.example.org', 'bakery', 100, null, false, null],
            // One DNS server did not take the last change: the page shows "Send again".
            ['harbourbooks.example.net', 'books', 38, null, false, 'web-2'],
        ];
        foreach ($rows as [$name, $customer, $days, $seen, $dnssec, $failed]) {
            $key = $dnssec ? Dnssec::generate() : null;
            $serial = now()->subHours(strlen($name))->timestamp;
            $zone = new DnsZone([
                'customer_id' => $this->customers[$customer]->id, 'name' => $name, 'serial' => $serial, 'status' => DnsZone::ACTIVE,
                'delegation' => $seen === null ? DnsZone::HERE : DnsZone::ELSEWHERE, 'delegation_name_servers' => $seen ?? $servers->pluck('dns_name')->all(),
                'delegation_checked_at' => now()->subMinutes(9), 'dnssec' => $dnssec, 'dnssec_key' => $key['key'] ?? null,
                'dnssec_public_key' => $key['publicKey'] ?? null, 'dnssec_key_tag' => $key['keyTag'] ?? null,
                'sync' => $servers->mapWithKeys(fn (Server $s) => [$s->id => ['serial' => $serial, 'state' => $s->name === $failed ? 'failed' : 'ok']])->all(),
            ]);
            $zone->created_at = now()->subDays($days);
            $zone->save();
            foreach ($servers as $server) {
                $zone->records()->create(['name' => '@', 'type' => 'NS', 'ttl' => Zones::NS_TTL, 'value' => $server->dns_name, 'managed' => DnsRecord::NS]);
            }
            // The A and AAAA records of the domains of the websites of the customer in the zone.
            foreach (Website::with(['domains', 'server'])->where('customer_id', $zone->customer_id)->get() as $website) {
                foreach ($website->domains as $domain) {
                    foreach ($zone->contains($domain->name) ? ($website->server->addresses ?? []) : [] as $address) {
                        $zone->records()->create(['name' => $zone->relative($domain->name), 'type' => str_contains($address, ':') ? 'AAAA' : 'A', 'ttl' => Zones::WEBSITE_TTL, 'value' => $address, 'managed' => DnsRecord::WEBSITE]);
                    }
                }
            }
            $this->zones[$name] = $zone;
        }
        // The records that the customers made: email at a different provider, and some services.
        $record = fn (string $zone, string $name, string $type, string $value, ?int $priority = null, int $ttl = 3600) => $this->zones[$zone]->records()->create(compact('name', 'type', 'value', 'priority', 'ttl'));
        $record('acme.example.com', '@', 'MX', 'mx1.mail.example', 10);
        $record('acme.example.com', '@', 'MX', 'mx2.mail.example', 20);
        $record('acme.example.com', '@', 'TXT', 'v=spf1 include:_spf.mail.example -all');
        $record('acme.example.com', '_dmarc', 'TXT', 'v=DMARC1; p=quarantine; rua=mailto:dmarc@acme.example.com');
        $record('acme.example.com', '@', 'CAA', '0 issue "letsencrypt.org"');
        $record('acme.example.com', 'status', 'CNAME', 'acme.status.example', null, 300);
        $record('acme.example.com', '_sip._tcp', 'SRV', '5 5060 sip.voice.example', 10);
        $record('acme.example.com', 'vpn', 'A', '198.51.100.24');
        $record('cornerbakery.example.org', '@', 'MX', 'mail.cornerbakery.example.org', 10);
        $record('cornerbakery.example.org', 'mail', 'A', '198.51.100.80');
        $record('harbourbooks.example.net', '@', 'TXT', 'v=spf1 -all');
    }

    private function databases(): void
    {
        // website, engine, name, size (MB), then for Valkey: number and keys
        $rows = [
            ['acme.example.com', 'mariadb', 'wp', 310], ['shop.acme.example.com', 'mariadb', 'shop', 1840], ['shop.acme.example.com', 'valkey', 'cache', 0, 1, 18412],
            ['api.acme.example.com', 'postgresql', 'app', 96], ['api.acme.example.com', 'valkey', 'queue', 0, 2, 227],
            ['northwind.example.com', 'mariadb', 'wp', 140], ['cornerbakery.example.org', 'mariadb', 'wp', 220], ['harbourbooks.example.net', 'mariadb', 'shop', 760],
            ['lawson.example.org', 'mariadb', 'wp', 88],
        ];
        foreach ($rows as $row) {
            [$site, $engine, $suffix, $size] = $row;
            $website = $this->websites[$site];
            $name = $website->unix_user.'_'.$suffix;
            $server = $this->databaseServer($website, $engine, $size);
            $database = $website->databases()->create(['engine' => $engine, 'name' => $name, 'size_bytes' => $size * self::MB, 'number' => $row[4] ?? null, 'key_count' => $row[5] ?? null, 'database_server_id' => $server->id]);
            $user = $website->databaseUsers()->create(['engine' => $engine, 'name' => $name, 'access' => 'website', 'database_server_id' => $server->id]);
            $user->databases()->attach($database->id, ['privileges' => 'readwrite']);
        }
        // A second user that only reads, for example for a reporting tool.
        $shop = $this->websites['shop.acme.example.com'];
        $reports = $shop->databaseUsers()->create(['engine' => 'mariadb', 'name' => $shop->unix_user.'_reports', 'access' => 'anywhere', 'database_server_id' => $shop->databases()->where('engine', 'mariadb')->value('database_server_id')]);
        $reports->databases()->attach($shop->databases()->where('engine', 'mariadb')->value('id'), ['privileges' => 'readonly']);

        // A second MariaDB server of one customer, with a smaller plan. It has the next port.
        $acme = $this->customers['acme'];
        $starter = $this->plans['Starter'];
        $second = DatabaseServer::create([
            'customer_id' => $acme->id, 'server_id' => $shop->server_id, 'plan_id' => $starter->id, 'engine' => 'mariadb', 'name' => 'mariadb-reports',
            'port' => DatabaseServer::FIRST_PORT['mariadb'] + 1, 'limits' => $starter->database_limits, 'status' => DatabaseServer::RUNNING,
            'usage' => ['memoryBytes' => 96 * self::MB, 'diskBytes' => 70 * self::MB, 'diskSizeBytes' => $starter->database_limits['diskBytes']],
        ]);
        // A website on a different server has a database in it. The website connects through
        // the network of the customer, by the network name of the database server.
        $storefront = $this->websites['storefront.acme.example.com'];
        $name = $storefront->unix_user.'_orders';
        $database = $storefront->databases()->create(['engine' => 'mariadb', 'name' => $name, 'size_bytes' => 52 * self::MB, 'database_server_id' => $second->id]);
        $user = $storefront->databaseUsers()->create(['engine' => 'mariadb', 'name' => $name, 'access' => 'website', 'database_server_id' => $second->id]);
        $user->databases()->attach($database->id, ['privileges' => 'readwrite']);
        // A named network: only the storefront and the reports database server are in it.
        $reports = Network::create(['customer_id' => $acme->id, 'name' => 'reports', 'is_default' => false]);
        NetworkMember::create(['network_id' => $reports->id, 'website_id' => $storefront->id]);
        NetworkMember::create(['network_id' => $reports->id, 'database_server_id' => $second->id]);
    }

    /**
     * Usage history for the graphs, and usage alerts. The values follow a day cycle around the
     * current use of each website, server and database server, with the same random values at
     * each run. The disk use grows to its current value. The panel keeps buckets of 5 minutes
     * for 8 days and of 1 hour for 400 days: the demo has 2 days of 5 minutes and 400 days of
     * 1 hour.
     */
    private function usageHistory(): void
    {
        $now = now()->utc();
        $month = $now->format('Y-m');
        $monthStart = $now->copy()->startOfMonth();
        // The traffic of each running website in one hour at the peak of the day, in MB.
        $traffic = ['acme.example.com' => 180, 'shop.acme.example.com' => 420, 'api.acme.example.com' => 60, 'storefront.acme.example.com' => 35,
            'northwind.example.com' => 90, 'cornerbakery.example.org' => 70, 'harbourbooks.example.net' => 1150, 'orders.internal' => 8];
        $rows = [];
        $flush = function (bool $all = false) use (&$rows) {
            if ($all || count($rows) >= 500) {
                foreach (array_chunk($rows, 500) as $chunk) {
                    DB::table('usage_samples')->insert($chunk);
                }
                $rows = [];
            }
        };
        // $at: the start of the bucket. $f: the factor of the day cycle, from about 0.3 to 1.
        $add = function (string $type, string $id, int $minutes, Carbon $at, float $cpu, float $memory, float $disk, int $processes, int $bytes) use (&$rows, $flush) {
            $samples = $minutes;
            $rows[] = [
                'subject_type' => $type, 'subject_id' => $id, 'resolution' => $minutes, 'bucket_at' => $at->format('Y-m-d H:i:s'), 'samples' => $samples,
                'cpu_sum' => round($cpu * $samples, 2), 'cpu_max' => round($cpu * (1.2 + mt_rand(0, 60) / 100), 2), 'memory_sum' => round($memory * $samples), 'memory_max' => round($memory * (1.03 + mt_rand(0, 8) / 100)),
                'disk' => round($disk), 'processes_max' => $processes, 'bytes_in' => (int) ($bytes * 0.08), 'bytes_out' => (int) ($bytes * 0.92),
            ];
            $flush();
        };
        $cycle = fn (Carbon $at) => 0.65 + 0.35 * sin(($at->hour + $at->minute / 60 - 9) / 24 * 2 * M_PI) + mt_rand(-8, 8) / 100;
        $weekday = fn (Carbon $at) => $at->isWeekend() ? 0.75 : 1.0;

        foreach ($this->websites as $name => $website) {
            if ($website->status->value === 'failed') {
                continue;
            }
            mt_srand(crc32($name));
            $use = $website->usage;
            $running = $website->status->value === 'running';
            $peak = ($traffic[$name] ?? 0) * self::MB;
            $created = $website->created_at->copy()->utc();
            $monthBytes = 0;
            foreach ([UsageRecorder::FINE => $now->copy()->subDays(2), UsageRecorder::COARSE => $now->copy()->subDays(400)] as $minutes => $from) {
                $at = UsageRecorder::bucket($from->max($created), $minutes);
                $span = max(1, $now->getTimestamp() - $created->getTimestamp());
                for (; $at <= $now; $at->addMinutes($minutes)) {
                    $f = max(0.2, $cycle($at)) * $weekday($at);
                    $growth = 0.55 + 0.45 * min(1, ($at->getTimestamp() - $created->getTimestamp()) / $span);
                    $stopped = ! $running && $at > $now->copy()->subDays(20);
                    $bytes = $stopped ? 0 : (int) ($peak * $f * $minutes / 60);
                    $add(UsageAlert::WEBSITE, $website->id, $minutes, $at->copy(),
                        $stopped ? 0 : max(0.3, $use['cpuPercent'] * $f * 1.4 ?: 2 * $f),
                        $stopped ? 0 : max(16 * self::MB, $use['memoryBytes'] * (0.85 + 0.15 * $f) ?: 64 * self::MB * $f),
                        $use['diskBytes'] * $growth,
                        $stopped ? 0 : (int) max(1, $use['processes'] * (0.7 + 0.4 * $f)),
                        $bytes);
                    if ($minutes === UsageRecorder::COARSE && $at >= $monthStart) {
                        $monthBytes += $bytes;
                    }
                }
            }
            $website->forceFill(['bandwidth_month' => $month, 'bandwidth_bytes' => $monthBytes])->saveQuietly();
        }

        foreach ($this->servers as $name => $server) {
            if (! $server->usage) {
                continue;
            }
            mt_srand(crc32($name));
            $u = $server->usage;
            foreach ([UsageRecorder::FINE => $now->copy()->subDays(2), UsageRecorder::COARSE => $now->copy()->subDays(400)] as $minutes => $from) {
                $created = $server->created_at->copy()->utc();
                $at = UsageRecorder::bucket($from->max($created), $minutes);
                for (; $at <= $now; $at->addMinutes($minutes)) {
                    $f = max(0.2, $cycle($at)) * $weekday($at);
                    $age = 1 - min(1, $now->diffInDays($at, true) / 400);
                    $add(UsageAlert::SERVER, $server->id, $minutes, $at->copy(), min(100, $u['cpuPercent'] * $f * 1.3), min(100, $u['memoryPercent'] * (0.85 + 0.2 * $f)),
                        $u['diskPercent'] * (0.6 + 0.4 * $age), 0, (int) (1600 * self::MB * $f * $minutes / 60 * ($u['cpuPercent'] / 20)));
                }
            }
        }

        foreach (DatabaseServer::all() as $databaseServer) {
            mt_srand(crc32($databaseServer->id));
            $u = $databaseServer->usage ?? [];
            $created = ($databaseServer->created_at ?? $now)->copy()->utc()->subDays(60);
            foreach ([UsageRecorder::FINE => $now->copy()->subDays(2), UsageRecorder::COARSE => $now->copy()->subDays(60)] as $minutes => $from) {
                $at = UsageRecorder::bucket($from->max($created), $minutes);
                for (; $at <= $now; $at->addMinutes($minutes)) {
                    $f = max(0.2, $cycle($at));
                    $age = 1 - min(1, $now->diffInDays($at, true) / 60);
                    $add(UsageAlert::DATABASE_SERVER, $databaseServer->id, $minutes, $at->copy(), 0, ($u['memoryBytes'] ?? 0) * (0.9 + 0.1 * $f), ($u['diskBytes'] ?? 0) * (0.8 + 0.2 * $age), 0, 0);
                }
            }
        }
        $flush(true);
        mt_srand();

        // The alerts: the shop is near its memory limit, the bakery near its disk limit and the
        // bookshop at its bandwidth limit of the month. A closed alert of a server shows the
        // history.
        $shop = $this->websites['shop.acme.example.com'];
        $bakery = $this->websites['cornerbakery.example.org'];
        $books = $this->websites['harbourbooks.example.net'];
        $books->forceFill(['bandwidth_bytes' => (int) (0.962 * $books->limits['bandwidthBytes'])])->saveQuietly();
        $alert = fn (Website|Server $subject, string $resource, string $level, float $used, float $limit, int $hoursAgo, ?int $resolvedHoursAgo = null) => UsageAlert::create([
            'subject_type' => $subject instanceof Website ? UsageAlert::WEBSITE : UsageAlert::SERVER, 'subject_id' => $subject->id,
            'customer_id' => $subject instanceof Website ? $subject->customer_id : null, 'server_id' => $subject instanceof Website ? $subject->server_id : $subject->id,
            'resource' => $resource, 'level' => $level, 'used' => $used, 'limit' => $limit, 'percent' => round($used / $limit * 100, 1),
            'notified_level' => $level, 'opened_at' => now()->subHours($hoursAgo), 'resolved_at' => $resolvedHoursAgo === null ? null : now()->subHours($resolvedHoursAgo),
        ]);
        $alert($books, 'bandwidth', UsageAlert::CRITICAL, $books->bandwidth_bytes, $books->limits['bandwidthBytes'], 30);
        $alert($shop, 'memory', UsageAlert::WARNING, $shop->usage['memoryBytes'], $shop->limits['memoryBytes'], 5);
        $alert($bakery, 'disk', UsageAlert::WARNING, $bakery->usage['diskBytes'], $bakery->limits['diskBytes'], 52);
        $alert($this->servers['web-1'], 'memory', UsageAlert::WARNING, 86.4, 100, 70, 66);
    }

    /**
     * The database server of a customer for one engine on the server of a website. Each
     * database of the demo data is in the first database server of its engine.
     */
    private function databaseServer(Website $website, string $engine, int $sizeMb): DatabaseServer
    {
        $plan = $website->customer->plan;
        $server = DatabaseServer::firstOrCreate(
            ['customer_id' => $website->customer_id, 'server_id' => $website->server_id, 'engine' => $engine, 'port' => DatabaseServer::FIRST_PORT[$engine]],
            ['plan_id' => $plan->id, 'name' => $engine.'-1', 'limits' => $plan->database_limits, 'status' => DatabaseServer::RUNNING, 'usage' => ['memoryBytes' => 0, 'diskBytes' => 0, 'diskSizeBytes' => $plan->database_limits['diskBytes']]],
        );
        // The use is an example: it grows with the size of the databases.
        $usage = $server->usage;
        $usage['diskBytes'] += (64 + $sizeMb) * self::MB;
        $usage['memoryBytes'] = (int) min($plan->database_limits['memoryBytes'] * 0.8, 120 * self::MB + $usage['diskBytes'] / 4);
        $server->update(['usage' => $usage]);

        return $server;
    }

    private function backups(): void
    {
        foreach ($this->websites as $name => $website) {
            if ($website->status->value === 'failed' || $website->created_at->gt(now()->subDay())) {
                continue;
            }
            // A backup has one part for the website, and one part for the databases on each
            // different server.
            [$remote, $local] = $website->sqlDatabases()->with('databaseServer')->get()->partition(fn ($d) => $d->databaseServer->server_id !== $website->server_id);
            $databases = $local->map(fn ($d) => "{$d->engine}:{$d->name}")->values()->all();
            $size = (int) (($website->usage['diskBytes'] ?? 0) * 0.6);
            // The last three nights, and one manual backup for the busy websites.
            foreach ([1, 2, 3] as $night) {
                $at = today()->subDays($night - 1)->setTime(3, 2 + strlen($name) % 9);
                $this->snapshot($website, 'scheduled', $size - $night * 3 * self::MB, $databases, $at);
                foreach ($remote->groupBy(fn ($d) => $d->databaseServer->server_id) as $serverId => $part) {
                    $this->snapshot($website, 'scheduled', (int) ($part->sum('size_bytes') * 0.4), $part->map(fn ($d) => "{$d->engine}:{$d->name}")->values()->all(), $at, $serverId);
                }
            }
            if (in_array($name, ['acme.example.com', 'shop.acme.example.com', 'harbourbooks.example.net'], true)) {
                $this->snapshot($website, 'manual', $size, $databases, now()->subHours(5));
            }
        }
    }

    /** @param  ?string  $serverId  The server of a part with only databases. Null is a backup of the website. */
    private function snapshot(Website $website, string $kind, int $size, array $databases, \DateTimeInterface $at, ?string $serverId = null): void
    {
        $snapshot = new Snapshot([
            'website_id' => $website->id, 'kind' => $kind, 'restic_id' => hash('sha256', $website->id.$kind.$at->format('c').$serverId), 'size_bytes' => max($size, self::MB),
            'databases' => $databases, 'mailboxes' => [], 'dump_hashes' => [],
            'server_id' => $serverId ?? $website->server_id, 'part' => $serverId ? Snapshot::DATABASES : Snapshot::WEBSITE,
        ]);
        $snapshot->created_at = $at > now() ? now()->subHours(20) : $at;
        $snapshot->save();
    }

    /** The cron jobs of some websites, and one website that deploys from Git. */
    private function cronJobsAndDeploys(): void
    {
        // website, schedule, command, on, minutes since the last run, exit code, output
        $rows = [
            ['acme.example.com', '*/5 * * * *', 'wp cron event run --due-now', true, 3, 0, "Executed the cron event 'wp_scheduled_delete' in 0.012s.\nSuccess: Executed a total of 1 cron event."],
            ['shop.acme.example.com', '*/5 * * * *', 'wp cron event run --due-now', true, 4, 0, 'Success: Executed a total of 3 cron events.'],
            ['shop.acme.example.com', '15 2 * * *', 'php bin/export-orders.php --yesterday', true, 600, 1, "PHP Fatal error:  Uncaught RuntimeException: The export directory is not writable in /app/bin/export-orders.php:18\nStack trace:\n#0 {main}\n  thrown in /app/bin/export-orders.php on line 18"],
            ['shop.acme.example.com', '0 4 * * 0', 'wp transient delete --expired', false, null, null, null],
            ['api.acme.example.com', '* * * * *', 'cd current && php artisan schedule:run', true, 1, 0, 'No scheduled commands are ready to run.'],
            ['northwind.example.com', '30 1 * * *', 'wp db optimize', true, 1300, 0, 'Success: Database optimized.'],
        ];
        foreach ($rows as [$name, $schedule, $command, $enabled, $minutes, $exit, $output]) {
            $this->websites[$name]->cronJobs()->create([
                'schedule' => $schedule, 'command' => $command, 'enabled' => $enabled,
                'last_run_at' => $minutes === null ? null : now()->subMinutes($minutes), 'last_exit_code' => $exit, 'last_output' => $output,
            ]);
        }

        $api = $this->websites['api.acme.example.com'];
        $key = SshKey::generate('cloudfactory-'.$api->unix_user);
        $api->gitSource()->create([
            'repository' => 'git@github.com:acme-example/api.git', 'branch' => 'main', 'web_directory' => 'public', 'trigger' => GitSource::WEBHOOK,
            'build_commands' => ['composer install --no-dev --optimize-autoloader', 'ln -s /app/shared/.env .env', 'php artisan migrate --force'],
            'public_key' => $key['public'], 'private_key' => $key['private'],
            'webhook_token' => $token = Str::random(48), 'webhook_token_hash' => hash('sha256', $token),
            'secrets' => ['COMPOSER_AUTH' => '{"github-oauth":{"github.com":"demo"}}', 'SENTRY_AUTH_TOKEN' => 'demo'],
        ]);
        // hours ago, commit subject, state, live, seconds, trigger, error
        $releases = [
            [400, 'Add the invoice endpoints', Release::REMOVED, false, 71, GitSource::MANUAL, null],
            [170, 'Cache the product list', Release::READY, false, 64, GitSource::WEBHOOK, null],
            [72, 'Use the new stock service', Release::READY, false, 69, GitSource::WEBHOOK, null],
            [26, 'Update the dependencies', Release::FAILED, false, 22, GitSource::WEBHOOK, "a build command failed: exit status 2: Installing dependencies from lock file\nYour lock file does not contain a compatible set of packages. Please run composer update.\n\n  Problem 1\n    - laravel/framework is locked to version v13.2.0 and an update of this package was not requested."],
            [25, 'Update the dependencies and the lock file', Release::READY, true, 75, GitSource::WEBHOOK, null],
        ];
        foreach ($releases as [$hours, $message, $state, $live, $seconds, $trigger, $error]) {
            $release = new Release([
                'website_id' => $api->id, 'ref' => 'main', 'commit' => sha1($message), 'message' => $message, 'state' => $state, 'live' => $live,
                'duration_seconds' => $seconds, 'trigger' => $trigger, 'error' => $error,
            ]);
            $release->id = $release->newUniqueId();
            $release->document_root = 'releases/'.$release->id.'/public';
            $release->created_at = $release->updated_at = now()->subHours($hours);
            $release->save();
            if ($live) {
                $api->update(['document_root' => $release->document_root]);
            }
        }
    }

    private function jobsAndRollout(): void
    {
        $job = function (string $server, AgentAction $action, string $title, JobState $state, int $minutesAgo, ?string $website = null, array $more = []) {
            $steps = $action->steps();
            $job = new AgentJob([
                'server_id' => $this->servers[$server]->id, 'website_id' => $website ? $this->websites[$website]->id : null, 'action' => $action, 'title' => $title,
                'ref' => $website, 'payload' => [], 'steps' => $steps, 'step' => $state === JobState::Succeeded ? count($steps) : 0, 'state' => $state,
                'started_at' => now()->subMinutes($minutesAgo), 'finished_at' => now()->subMinutes(max(0, $minutesAgo - 1)), ...$more,
            ]);
            $job->created_at = now()->subMinutes($minutesAgo);
            $job->save();

            return $job;
        };
        $job('web-1', AgentAction::Backup, 'Back up', JobState::Succeeded, 300, 'acme.example.com');
        $job('web-2', AgentAction::InstallHostUpdates, 'Install updates on web-2', JobState::Succeeded, 12 * 24 * 60);
        $job('web-1', AgentAction::CreateDatabase, 'Create database', JobState::Succeeded, 95, 'api.acme.example.com');
        $job('web-2', AgentAction::AddDomain, 'Add domain cornerbakery-orders.example.org', JobState::Succeeded, 70, 'cornerbakery.example.org');
        // The first try failed, and the second try of the same change succeeded.
        $job('web-1', AgentAction::CreateDatabase, 'Create database', JobState::Failed, 50, 'shop.acme.example.com', [
            'step' => 0, 'payload' => ['engine' => 'valkey', 'name' => $this->websites['shop.acme.example.com']->unix_user.'_cache'],
            'error' => 'runuser -u cf-db -- env XDG_RUNTIME_DIR=/run/user/994 podman exec cf-valkey valkey-cli -s /run/valkey/valkey.sock PING: exit status 1: Could not connect to Valkey at /run/valkey/valkey.sock: No such file or directory',
        ]);
        $job('web-1', AgentAction::CreateDatabase, 'Create database', JobState::Succeeded, 45, 'shop.acme.example.com', ['payload' => ['engine' => 'valkey', 'name' => $this->websites['shop.acme.example.com']->unix_user.'_cache']]);
        $job('web-3', AgentAction::CreateWebsite, 'Create website', JobState::Failed, 25, 'events.harbourbooks.example.net', [
            'step' => 3,
            'error' => 'systemctl start cf-site-'.$this->websites['events.harbourbooks.example.net']->unix_user.'.service: exit status 1: the container did not answer the health check on port 8080 in 90 seconds',
        ]);

        // The DNS servers got a zone. One DNS server did not take the last change of a different zone.
        foreach (['web-1', 'web-2'] as $server) {
            $job($server, AgentAction::SetDnsZones, 'Add TXT record to acme.example.com', JobState::Succeeded, 140, null, ['ref' => 'acme.example.com', 'dns_zone_id' => $this->zones['acme.example.com']->id]);
        }
        $books = ['ref' => 'harbourbooks.example.net', 'dns_zone_id' => $this->zones['harbourbooks.example.net']->id];
        $job('web-1', AgentAction::SetDnsZones, 'Add TXT record to harbourbooks.example.net', JobState::Succeeded, 33, null, $books);
        $job('web-2', AgentAction::SetDnsZones, 'Add TXT record to harbourbooks.example.net', JobState::Failed, 33, null, [
            ...$books, 'step' => 2, 'error' => 'knotc -b zone-reload harbourbooks.example.net: exit status 1: error: [harbourbooks.example.net.] (operation timed out)',
        ]);

        // A rollout that ended yesterday. One website went back to the earlier image.
        $sites = collect(['old.acme.example.com', 'cornerbakery.example.org'])->map(fn ($n) => $this->websites[$n]->id);
        $rollout = new Rollout([
            'image_id' => $this->images['apache-8.4']->id, 'from_tag' => '0.1.8', 'to_tag' => '0.1.9', 'severity' => 'security', 'state' => 'done', 'stage' => 3,
            'website_ids' => $sites->all(), 'updated_ids' => [$sites[1]],
            'rolled_back' => [['websiteId' => $sites[0], 'reason' => 'The health check failed: the website gave status 500.']],
        ]);
        $rollout->created_at = now()->subHours(30);
        $rollout->updated_at = now()->subHours(29);
        $rollout->save();
    }

    private function settingsAndAudit(): void
    {
        // The object storage of the backups. The demo agent does not connect to it.
        Setting::put('backup_policy', [...config('cloudfactory.backups'), 'target' => 's3:s3.eu-central.example/cloudfactory-backups']);
        Setting::put('backup_credentials', Crypt::encryptString(json_encode(['AWS_ACCESS_KEY_ID' => 'demo', 'AWS_SECRET_ACCESS_KEY' => 'demo'])));
        // One server has its own key. The other servers use the shared key, and the Backups page says so.
        $this->servers['web-1']->setBackupCredentials(['AWS_ACCESS_KEY_ID' => 'demo-web-1', 'AWS_SECRET_ACCESS_KEY' => 'demo']);
        PanelCertificate::record(['domain' => PanelCertificate::domain(), 'method' => 'internal', 'state' => 'valid', 'expiresAt' => now()->addDays(85)->toIso8601String(), 'caCertificate' => self::certificate()]);

        $admin = User::where('email', 'admin@example.com')->first();
        $owner = User::where('email', 'owner@example.com')->first();
        $reseller = User::where('email', 'reseller@example.com')->first();
        $token = $admin->createToken('Terraform', ['websites:read', 'websites:write', 'databases:read', 'databases:write'], now()->addDays(90));
        $token->accessToken->forceFill(['last_used_at' => now()->subHours(3)])->save();

        $entries = [
            [$admin, 'Signed in', '', 6], [$admin, 'Started rollout', 'Apache + PHP-FPM · PHP 8.4 0.1.9', 1800], [$admin, 'Suspended customer', 'Lawson & Co', 2900],
            [$reseller, 'Created customer', 'Harbour Books', 64800], [$reseller, 'Created website', 'events.harbourbooks.example.net', 25],
            [$owner, 'Created database', $this->websites['shop.acme.example.com']->unix_user.'_cache', 45], [$owner, 'Backed up website', 'acme.example.com', 300],
            [$owner, 'Added domain', 'acme-outlet.example.com', 130], [$owner, 'Added DNS record', 'acme.example.com TXT @', 140],
            [$owner, 'Created DNS zone', 'acme-outlet.example.com', 120],
        ];
        foreach ($entries as [$user, $action, $target, $minutesAgo]) {
            $entry = new AuditEntry(['user_id' => $user->id, 'actor' => $user->name, 'ip' => '203.0.113.'.(10 + $user->id), 'action' => $action, 'target' => $target, 'customer_id' => $user->customer_id]);
            $entry->created_at = now()->subMinutes($minutesAgo);
            $entry->save();
        }
    }

    /** The Unix user of a website: letters from the name, and four characters that do not change. */
    private static function unixUser(string $name): string
    {
        return substr(preg_replace('/[^a-z0-9]/', '', explode('.', $name)[0].(explode('.', $name)[1] ?? '')), 0, 8).'_'.substr(hash('sha256', $name), 0, 4);
    }

    /** A self-signed certificate, as an example of the certificate of the panel CA. */
    private static function certificate(): string
    {
        $key = openssl_pkey_new(['private_key_type' => OPENSSL_KEYTYPE_EC, 'curve_name' => 'prime256v1']);
        $request = openssl_csr_new(['commonName' => 'Cloudfactory panel CA (demo)'], $key, ['digest_alg' => 'sha256']);
        openssl_x509_export(openssl_csr_sign($request, null, $key, 3650, ['digest_alg' => 'sha256']), $pem);

        return $pem;
    }
}
