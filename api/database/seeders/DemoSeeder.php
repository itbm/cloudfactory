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
use App\Models\Image;
use App\Models\Plan;
use App\Models\Rollout;
use App\Models\Server;
use App\Models\Setting;
use App\Models\Snapshot;
use App\Models\User;
use App\Models\Website;
use App\Services\PanelCertificate;
use Illuminate\Database\Seeder;
use Illuminate\Support\Facades\Crypt;
use Illuminate\Support\Facades\Hash;

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

    public function run(): void
    {
        $this->plans();
        $this->customersAndUsers();
        $this->images();
        $this->servers();
        $this->websites();
        $this->databases();
        $this->backups();
        $this->jobsAndRollout();
        $this->settingsAndAudit();
    }

    private function plans(): void
    {
        $extensions = ['opcache', 'intl', 'gd', 'zip', 'exif', 'bcmath', 'mysqli', 'pdo_mysql', 'pdo_pgsql', 'redis', 'imagick', 'soap'];
        // name, vCPUs, memory (GB), disk (GB), websites, then for the database servers: number, databases in each one, memory (MB), disk (GB)
        foreach ([['Starter', 1, 1, 10, 3, 2, 5, 512, 2], ['Business', 2, 2, 40, 10, 6, 20, 1024, 10], ['Agency', 4, 4, 100, 50, 20, 50, 2048, 20]] as [$name, $cpu, $memory, $disk, $sites, $databaseServers, $databases, $databaseMemory, $databaseDisk]) {
            $this->plans[$name] = Plan::create([
                'name' => $name, 'websites' => $sites, 'mailboxes' => $sites * 10, 'databases' => $databases,
                'database_servers' => $databaseServers,
                'database_limits' => ['vcpus' => min($cpu, 2), 'memoryBytes' => $databaseMemory * self::MB, 'diskBytes' => $databaseDisk * self::GB],
                'php_extensions' => $name === 'Starter' ? array_slice($extensions, 0, 8) : $extensions,
                'limits' => ['vcpus' => $cpu, 'memoryBytes' => $memory * self::GB, 'diskBytes' => $disk * self::GB, 'diskIoBytesPerSecond' => 50 * self::MB, 'processes' => 100 * $cpu],
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
        // The panel server. Its disk quotas are off, as on a server with a file system that has no quota support.
        $this->servers['web-1'] = Server::create([
            ...$common, 'name' => 'web-1', 'location' => 'London', 'roles' => ['web', 'database'], 'state' => 'healthy', 'local' => true, 'agent_url' => 'https://web-1.panel.example:7443',
            'services' => [...$services, ['name' => 'disk-quotas', 'state' => 'stopped', 'optional' => true, 'detail' => 'The file system of /srv/cloudfactory has no project quotas. The server does not apply the disk limit of a website.']],
            'usage' => ['cpuPercent' => 23, 'memoryPercent' => 58, 'diskPercent' => 41], 'last_update_at' => now()->subDays(3),
        ]);
        // Updates wait, and the last update needs a restart.
        $this->servers['web-2'] = Server::create([
            ...$common, 'name' => 'web-2', 'location' => 'Amsterdam', 'roles' => ['web', 'database'], 'state' => 'healthy', 'os' => 'Ubuntu 26.04 LTS', 'kernel' => '6.17.0-8', 'arch' => 'arm64',
            'podman_version' => '5.6.0', 'agent_url' => 'https://web-2.panel.example:7443', 'reboot_required' => true, 'update_policy' => 'manual',
            'services' => [...$services, ['name' => 'disk-quotas', 'state' => 'running', 'optional' => true]],
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
            ...$common, 'name' => 'web-3', 'location' => 'Frankfurt', 'roles' => ['web'], 'state' => 'degraded', 'agent_version' => '0.1.8', 'agent_url' => 'https://web-3.panel.example:7443',
            'services' => [
                ...$running(['edge', 'firewall', 'podman']),
                ['name' => 'cf-sftp', 'state' => 'failed', 'detail' => 'The container stopped with exit code 125: the port 2222 is in use.'],
                ['name' => 'disk-quotas', 'state' => 'running', 'optional' => true],
            ],
            'usage' => ['cpuPercent' => 4, 'memoryPercent' => 19, 'diskPercent' => 12],
        ]);
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
        ];
        foreach ($rows as [$name, $application, $customer, $server, $image, $status, $days, $use, $more]) {
            $owner = $this->customers[$customer];
            $plan = $owner->plan;
            $website = new Website([
                'name' => $name, 'application' => $application, 'customer_id' => $owner->id, 'server_id' => $this->servers[$server]->id, 'plan_id' => $plan->id,
                'image_id' => $this->images[$image]->id, 'unix_user' => self::unixUser($name), 'status' => $status,
                'extensions' => $image === 'node' ? [] : ['opcache', 'intl', 'gd', 'zip'], 'php_ini' => $image === 'node' ? [] : ['memory_limit' => '256M', 'upload_max_filesize' => '64M'],
                'limits' => $plan->limits, 'database_instance' => $owner->id, 'document_root' => $application === 'Laravel' ? 'public' : '.',
                'usage' => ['cpuPercent' => $use[0], 'memoryBytes' => $use[1] * self::MB, 'diskBytes' => $use[2] * self::MB, 'processes' => $use[3]],
                ...$more,
            ]);
            $website->created_at = now()->subDays($days);
            $website->updated_at = now()->subHours(min($days * 24, 30));
            $website->save();
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

        // A second MariaDB server of one customer, with a smaller plan and no databases yet. It has the next port.
        $acme = $this->customers['acme'];
        $starter = $this->plans['Starter'];
        DatabaseServer::create([
            'customer_id' => $acme->id, 'server_id' => $shop->server_id, 'plan_id' => $starter->id, 'engine' => 'mariadb', 'name' => 'mariadb-reports',
            'port' => DatabaseServer::FIRST_PORT['mariadb'] + 1, 'limits' => $starter->database_limits, 'status' => DatabaseServer::RUNNING,
            'usage' => ['memoryBytes' => 96 * self::MB, 'diskBytes' => 70 * self::MB, 'diskSizeBytes' => $starter->database_limits['diskBytes']],
        ]);
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
            $databases = $website->sqlDatabases()->get()->map(fn ($d) => "{$d->engine}:{$d->name}")->all();
            $size = (int) (($website->usage['diskBytes'] ?? 0) * 0.6);
            // The last three nights, and one manual backup for the busy websites.
            foreach ([1, 2, 3] as $night) {
                $this->snapshot($website, 'scheduled', $size - $night * 3 * self::MB, $databases, today()->subDays($night - 1)->setTime(3, 2 + strlen($name) % 9));
            }
            if (in_array($name, ['acme.example.com', 'shop.acme.example.com', 'harbourbooks.example.net'], true)) {
                $this->snapshot($website, 'manual', $size, $databases, now()->subHours(5));
            }
        }
    }

    private function snapshot(Website $website, string $kind, int $size, array $databases, \DateTimeInterface $at): void
    {
        $snapshot = new Snapshot([
            'website_id' => $website->id, 'kind' => $kind, 'restic_id' => hash('sha256', $website->id.$kind.$at->format('c')), 'size_bytes' => max($size, self::MB),
            'databases' => $databases, 'mailboxes' => [], 'dump_hashes' => [],
        ]);
        $snapshot->created_at = $at > now() ? now()->subHours(20) : $at;
        $snapshot->save();
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
            [$owner, 'Added domain', 'acme-outlet.example.com', 130],
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
        return substr(preg_replace('/[^a-z0-9]/', '', explode('.', $name)[0].explode('.', $name)[1]), 0, 8).'_'.substr(hash('sha256', $name), 0, 4);
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
