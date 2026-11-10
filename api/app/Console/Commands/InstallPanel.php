<?php

namespace App\Console\Commands;

use App\Enums\Role;
use App\Models\Image;
use App\Models\JoinToken;
use App\Models\Plan;
use App\Models\User;
use Illuminate\Console\Attributes\Description;
use Illuminate\Console\Attributes\Signature;
use Illuminate\Console\Command;
use Illuminate\Support\Str;

/**
 * The installation script runs this command once in a new panel (FR-1808). It adds the first
 * administrator, the standard images and plans, and a join token for the panel server.
 * The output is JSON, so that the script can read it.
 */
#[Signature('cloudfactory:install {email : The email address of the first administrator} {--image-tag=0.1.0 : The tag of the runtime images}')]
#[Description('Prepare a new panel: first administrator, standard images and plans, and a join token.')]
class InstallPanel extends Command
{
    public function handle(): int
    {
        $email = (string) $this->argument('email');
        if (! filter_var($email, FILTER_VALIDATE_EMAIL)) {
            $this->error('Give a valid email address.');

            return self::FAILURE;
        }

        $password = null;
        if (! User::where('role', Role::Administrator)->exists()) {
            $password = Str::password(20, symbols: false);
            // The installation script prints this password. The administrator sets a new one
            // at the first sign-in.
            User::create(['name' => 'Administrator', 'email' => $email, 'password' => $password, 'role' => Role::Administrator])
                ->forceFill(['must_change_password' => true])->save();
        }

        $registry = config('cloudfactory.registry');
        $tag = (string) $this->option('image-tag');
        // FR-301 and FR-302. The dates are the end of security support of each PHP version.
        $php = ['8.2' => '2026-12-31', '8.3' => '2027-12-31', '8.4' => '2028-12-31', '8.5' => '2029-12-31'];
        foreach ($php as $version => $ends) {
            Image::firstOrCreate(['reference' => "{$registry}/frankenphp-php{$version}"], [
                'name' => "FrankenPHP · PHP {$version}", 'family' => 'frankenphp', 'php_version' => $version,
                'tag' => $tag, 'support_ends_at' => $ends, 'policy' => 'security_automatic', 'signature_verified' => true,
            ]);
            Image::firstOrCreate(['reference' => "{$registry}/apache-php{$version}"], [
                'name' => "Apache + PHP-FPM · PHP {$version}", 'family' => 'apache-php-fpm', 'php_version' => $version,
                'tag' => $tag, 'support_ends_at' => $ends, 'policy' => 'security_automatic', 'signature_verified' => true,
            ]);
        }

        $gb = 1024 ** 3;
        $extensions = ['opcache', 'intl', 'gd', 'zip', 'exif', 'bcmath', 'mysqli', 'pdo_mysql', 'pdo_pgsql', 'redis', 'imagick', 'soap'];
        foreach ([['Starter', 1, 1, 10, 3], ['Business', 2, 2, 40, 10], ['Agency', 4, 4, 100, 50]] as [$name, $cpu, $mem, $disk, $sites]) {
            Plan::firstOrCreate(['name' => $name], [
                'websites' => $sites, 'mailboxes' => $sites * 10, 'databases' => $sites, 'php_extensions' => $extensions,
                'limits' => ['vcpus' => $cpu, 'memoryBytes' => $mem * $gb, 'diskBytes' => $disk * $gb, 'diskIoBytesPerSecond' => 50 * 1024 ** 2, 'processes' => 100 * $cpu],
            ]);
        }

        $token = 'cfj_'.Str::random(32);
        JoinToken::create(['token_hash' => hash('sha256', $token), 'expires_at' => now()->addHour()]);

        $this->line(json_encode(['email' => $email, 'password' => $password, 'joinToken' => $token]));

        return self::SUCCESS;
    }
}
