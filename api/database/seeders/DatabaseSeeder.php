<?php

namespace Database\Seeders;

use App\Enums\DomainKind;
use App\Enums\Role;
use App\Models\Customer;
use App\Models\Image;
use App\Models\Plan;
use App\Models\Server;
use App\Models\User;
use App\Models\Website;
use Illuminate\Database\Console\Seeds\WithoutModelEvents;
use Illuminate\Database\Seeder;
use Illuminate\Support\Facades\Hash;

/**
 * Sample data for development. It agrees with the sample data of the web panel (web/src/api/seed.ts).
 * Do not use this data on a production panel.
 */
class DatabaseSeeder extends Seeder
{
    use WithoutModelEvents;

    private const GB = 1024 ** 3;

    public function run(): void
    {
        $extensions = ['opcache', 'intl', 'gd', 'zip', 'redis', 'imagick', 'bcmath', 'soap', 'exif', 'mysqli', 'pdo_pgsql'];
        $plans = [
            'Starter' => Plan::create(['name' => 'Starter', 'websites' => 3, 'mailboxes' => 10, 'databases' => 3, 'php_extensions' => array_slice($extensions, 0, 5), 'limits' => self::limits(1, 1, 10), 'database_servers' => 3, 'database_limits' => self::databaseLimits(1, 1, 10)]),
            'Business' => Plan::create(['name' => 'Business', 'websites' => 10, 'mailboxes' => 50, 'databases' => 10, 'php_extensions' => $extensions, 'limits' => self::limits(2, 2, 40), 'database_servers' => 6, 'database_limits' => self::databaseLimits(2, 2, 40)]),
            'Agency' => Plan::create(['name' => 'Agency', 'websites' => 50, 'mailboxes' => 200, 'databases' => 50, 'php_extensions' => $extensions, 'limits' => self::limits(4, 4, 100), 'database_servers' => 12, 'database_limits' => self::databaseLimits(4, 4, 100)]),
        ];

        $acme = Customer::create(['name' => 'Acme Ltd', 'email' => 'it@acme.example', 'role' => 'owner', 'plan_id' => $plans['Business']->id]);
        $agency = Customer::create(['name' => 'Northwind Agency', 'email' => 'ops@northwind.example', 'role' => 'reseller', 'plan_id' => $plans['Agency']->id]);
        $bakery = Customer::create(['name' => 'Corner Bakery', 'email' => 'hello@bakery.example', 'role' => 'owner', 'plan_id' => $plans['Starter']->id, 'reseller_id' => $agency->id]);

        User::create(['name' => 'Alex Admin', 'email' => 'admin@example.com', 'password' => Hash::make('password'), 'role' => Role::Administrator]);
        User::create(['name' => 'Sam Reseller', 'email' => 'reseller@example.com', 'password' => Hash::make('password'), 'role' => Role::Reseller, 'customer_id' => $agency->id]);
        User::create(['name' => 'Olu Owner', 'email' => 'owner@example.com', 'password' => Hash::make('password'), 'role' => Role::Owner, 'customer_id' => $acme->id]);

        $images = [];
        foreach (['8.4' => '2028-12-31', '8.5' => '2029-12-31'] as $php => $ends) {
            $images["frankenphp-{$php}"] = Image::create([
                'name' => "FrankenPHP · PHP {$php}", 'family' => 'frankenphp', 'php_version' => $php,
                'reference' => config('cloudfactory.registry')."/frankenphp-php{$php}", 'tag' => "{$php}.0-1",
                'support_ends_at' => $ends, 'policy' => 'security_automatic', 'signature_verified' => true,
            ]);
        }
        $images['frankenphp-8.4']->update(['available_tag' => '8.4.0-2', 'available_digest' => 'sha256:'.str_repeat('a', 64)]);
        $images['apache-8.4'] = Image::create([
            'name' => 'Apache + PHP-FPM · PHP 8.4', 'family' => 'apache-php-fpm', 'php_version' => '8.4',
            'reference' => config('cloudfactory.registry').'/apache-php8.4', 'tag' => '8.4.0-1',
            'support_ends_at' => '2028-12-31', 'policy' => 'manual', 'signature_verified' => true,
        ]);

        $web1 = Server::create(['name' => 'web-1', 'location' => 'London', 'roles' => ['web', 'database'], 'state' => 'healthy', 'agent_url' => 'https://web-1.internal:7443', 'agent_version' => '0.1.0', 'os' => 'Debian 13', 'arch' => 'x86-64', 'podman_version' => '5.4.2', 'gvisor_version' => 'release-20261019.0', 'pending_updates' => [], 'services' => [], 'last_seen_at' => now()]);
        Server::create(['name' => 'web-2', 'location' => 'Amsterdam', 'roles' => ['web'], 'state' => 'healthy', 'agent_url' => 'https://web-2.internal:7443', 'agent_version' => '0.1.0', 'os' => 'Ubuntu 26.04 LTS', 'arch' => 'arm64', 'podman_version' => '5.6.0', 'gvisor_version' => 'release-20261019.0', 'pending_updates' => [['name' => 'openssl', 'from' => '3.5.1-1', 'to' => '3.5.1-2', 'security' => true], ['name' => 'podman', 'from' => '5.6.0', 'to' => '5.6.1', 'security' => false, 'containerTool' => true]], 'services' => [], 'last_seen_at' => now()]);

        foreach ([
            ['acme.example', 'WordPress', $acme, 'frankenphp-8.4'],
            ['shop.acme.example', 'WooCommerce', $acme, 'frankenphp-8.4'],
            ['api.acme.example', 'Laravel', $acme, 'frankenphp-8.5'],
            ['cornerbakery.example', 'WordPress', $bakery, 'apache-8.4'],
        ] as [$name, $app, $customer, $image]) {
            $plan = $customer->plan;
            $website = Website::create([
                'name' => $name, 'application' => $app, 'customer_id' => $customer->id, 'server_id' => $web1->id,
                'plan_id' => $plan->id, 'image_id' => $images[$image]->id, 'unix_user' => substr(str_replace('.', '', $name), 0, 8).'_'.substr(md5($name), 0, 4),
                'status' => 'running', 'extensions' => ['opcache', 'intl', 'gd'], 'php_ini' => ['memory_limit' => '256M'], 'limits' => $plan->limits,
            ]);
            $website->domains()->create(['name' => $name, 'kind' => DomainKind::Primary, 'verification_token' => 'cf-sample', 'verified_at' => now(), 'dns_points_here' => true, 'certificate_state' => 'valid', 'certificate_expires_at' => now()->addDays(60)]);
        }
    }

    /** A database server has the vCPUs of a website, the half of its memory and the half of its disk space. */
    private static function databaseLimits(float $vcpus, int $memoryGb, int $diskGb): array
    {
        return ['vcpus' => $vcpus, 'memoryBytes' => intdiv($memoryGb * self::GB, 2), 'diskBytes' => intdiv($diskGb * self::GB, 2)];
    }

    private static function limits(float $vcpus, int $memoryGb, int $diskGb): array
    {
        return ['vcpus' => $vcpus, 'memoryBytes' => $memoryGb * self::GB, 'diskBytes' => $diskGb * self::GB, 'diskIoBytesPerSecond' => 50 * 1024 ** 2, 'processes' => 100];
    }
}
