<?php

namespace App\Console\Commands;

use App\Enums\Role;
use App\Enums\UpdatePolicy;
use App\Models\Image;
use App\Models\JoinToken;
use App\Models\Plan;
use App\Models\Rollout;
use App\Models\User;
use App\Services\Audit;
use App\Services\ImageUpdateChecker;
use Illuminate\Console\Attributes\Description;
use Illuminate\Console\Attributes\Signature;
use Illuminate\Console\Command;
use Illuminate\Support\Str;

/**
 * The installation script runs this command at each installation (FR-1808). In a new panel, it
 * adds the first administrator, the standard images and plans, and a join token for the panel
 * server. After an update of the panel, it moves the standard images to the new release.
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
        $php = ['8.4' => '2028-12-31', '8.5' => '2029-12-31'];
        foreach ($php as $version => $ends) {
            self::upgrade(Image::firstOrCreate(['reference' => "{$registry}/frankenphp-php{$version}"], [
                'name' => "FrankenPHP · PHP {$version}", 'family' => 'frankenphp', 'php_version' => $version,
                'tag' => $tag, 'support_ends_at' => $ends, 'policy' => 'security_automatic', 'signature_verified' => true,
            ]), $tag);
            self::upgrade(Image::firstOrCreate(['reference' => "{$registry}/apache-php{$version}"], [
                'name' => "Apache + PHP-FPM · PHP {$version}", 'family' => 'apache-php-fpm', 'php_version' => $version,
                'tag' => $tag, 'support_ends_at' => $ends, 'policy' => 'security_automatic', 'signature_verified' => true,
            ]), $tag);
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

    /**
     * An update of the panel brings a new release of the standard images. Before, an image
     * kept the tag of the first installation until an administrator started a rollout, also
     * when no website used the image.
     *
     * - An image that no website uses changes to the new tag at once.
     * - An image with websites shows the new tag as available. The administrator starts the
     *   rollout on the Updates page, or the update policy of the image starts it.
     */
    private static function upgrade(Image $image, string $tag): void
    {
        $new = ImageUpdateChecker::version($tag);
        $current = ImageUpdateChecker::version((string) $image->tag);
        if ($image->wasRecentlyCreated || $new === null || ($current !== null && ImageUpdateChecker::compare($new, $current) <= 0)) {
            return;
        }
        if (Rollout::where('image_id', $image->id)->whereIn('state', ['running', 'paused'])->exists()) {
            return;
        }
        if (! $image->inUse() && $image->policy !== UpdatePolicy::Manual) {
            $from = $image->tag;
            $image->changeVersion($tag, null);
            Audit::system('Changed image version', "{$image->name}: {$from} to {$tag}. No website uses the image.");

            return;
        }
        $available = $image->available_tag === null ? null : ImageUpdateChecker::version($image->available_tag);
        if ($available === null || ImageUpdateChecker::compare($new, $available) > 0) {
            // The agent checks the signature of the image when it pulls the tag.
            $image->update(['available_tag' => $tag, 'available_digest' => null]);
        }
    }
}
