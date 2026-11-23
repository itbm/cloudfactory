<?php

namespace Tests\Support;

use App\Enums\DomainKind;
use App\Enums\Role;
use App\Models\Customer;
use App\Models\DatabaseServer;
use App\Models\Image;
use App\Models\Plan;
use App\Models\Server;
use App\Models\User;
use App\Models\Website;
use App\Services\Dns\Lookup;
use App\Services\DomainPolicy;
use App\Services\Totp;
use Laravel\Sanctum\Sanctum;

/** Small helpers that make the data that the tests need. */
trait Hosting
{
    protected function plan(array $attributes = []): Plan
    {
        return Plan::create($attributes + [
            'name' => 'Business',
            'websites' => 10,
            'mailboxes' => 10,
            'databases' => 10,
            'php_extensions' => ['opcache', 'intl', 'gd', 'zip', 'redis'],
            'limits' => ['vcpus' => 2, 'memoryBytes' => 2 * 1024 ** 3, 'diskBytes' => 20 * 1024 ** 3, 'diskIoBytesPerSecond' => 50 * 1024 ** 2, 'processes' => 100],
        ]);
    }

    protected function customer(?Plan $plan = null, array $attributes = []): Customer
    {
        return Customer::create($attributes + ['name' => 'Acme Ltd', 'email' => 'it@acme.example', 'role' => 'owner', 'plan_id' => ($plan ?? $this->plan())->id]);
    }

    /** A server. Its agent reports that it can operate database servers. */
    protected function server(array $attributes = []): Server
    {
        return Server::create($attributes + ['name' => 'web-'.substr(uniqid(), -5), 'roles' => ['web'], 'state' => 'healthy', 'agent_url' => 'https://web-1.internal:7443', 'agent_version' => '0.1.0', 'database_instances' => true]);
    }

    protected function image(array $attributes = []): Image
    {
        return Image::create($attributes + ['name' => 'FrankenPHP · PHP 8.4', 'family' => 'frankenphp', 'php_version' => '8.4', 'reference' => 'ghcr.io/cloudfactory/frankenphp-php8.4', 'tag' => '8.4.0-1', 'signature_verified' => true]);
    }

    protected function website(Customer $customer, Server $server, Image $image, string $name = 'acme.example', array $attributes = []): Website
    {
        $website = Website::create($attributes + [
            'name' => $name, 'customer_id' => $customer->id, 'server_id' => $server->id, 'plan_id' => $customer->plan_id,
            'image_id' => $image->id, 'unix_user' => substr(str_replace('.', '', $name), 0, 8).'_'.substr(md5($name), 0, 4),
            'status' => 'running', 'extensions' => ['opcache'], 'php_ini' => [], 'limits' => $customer->plan->limits,
        ]);
        $website->domains()->create(['name' => $name, 'kind' => DomainKind::Primary, 'verification_token' => 'cf-test', 'verified_at' => now()]);

        return $website;
    }

    /** A database server of the customer of a website, on the server of the website. */
    protected function databaseServerOf(Website $website, string $engine = 'mariadb', array $attributes = []): DatabaseServer
    {
        return DatabaseServer::create($attributes + [
            'customer_id' => $website->customer_id, 'server_id' => $website->server_id, 'plan_id' => $website->plan_id, 'engine' => $engine,
            'name' => "{$engine}-1", 'port' => DatabaseServer::FIRST_PORT[$engine], 'status' => DatabaseServer::RUNNING,
            'limits' => ['vcpus' => 1, 'memoryBytes' => 1024 ** 3, 'diskBytes' => 5 * 1024 ** 3],
        ]);
    }

    protected function user(Role $role = Role::Administrator, ?Customer $customer = null): User
    {
        return User::factory()->create(['role' => $role, 'customer_id' => $customer?->id]);
    }

    /** Turns on TOTP for a user, and gives the key. Totp::code($key) gives a code. */
    protected function totp(User $user): string
    {
        $secret = Totp::generateSecret();
        $user->forceFill(['totp_secret' => $secret, 'totp_last_step' => null])->save();

        return $secret;
    }

    /** A DNS that answers TXT queries from the array. The test can change the array later. */
    protected function fakeTxt(array &$txt): DomainPolicy
    {
        $policy = new class($txt) extends DomainPolicy
        {
            public function __construct(public array &$txt) {}

            protected function txtRecords(string $name): array
            {
                return $this->txt[$name] ?? [];
            }
        };
        $this->app->instance(DomainPolicy::class, $policy);

        return $policy;
    }

    /**
     * A public DNS that answers NS queries from the array. The test can change the array later.
     * The DS records are in the property "ds" of the answer.
     */
    protected function fakeNameServers(array &$ns): Lookup
    {
        $lookup = new class($ns) extends Lookup
        {
            /** @var array<string, list<array<string, mixed>>|null> The DS records of each zone. Null: the lookup fails. */
            public array $ds = [];

            public function __construct(public array &$ns) {}

            public function nameServers(string $zone): array
            {
                return $this->ns[$zone] ?? [];
            }

            public function ds(string $zone): ?array
            {
                return array_key_exists($zone, $this->ds) ? $this->ds[$zone] : [];
            }
        };
        $this->app->instance(Lookup::class, $lookup);

        return $lookup;
    }

    /** A server that answers DNS queries, with a name server host name. */
    protected function dnsServer(string $host = 'ns1.hosting.example', array $attributes = []): Server
    {
        return $this->server($attributes + ['name' => 'dns-'.substr(md5($host), 0, 5), 'roles' => ['dns'], 'dns' => true, 'dns_name' => $host]);
    }

    /** Sign in as the user with a token that has these scopes. */
    protected function actingWith(User $user, array $scopes = ['*']): User
    {
        Sanctum::actingAs($user, $scopes);

        return $user;
    }
}
