<?php

namespace App\Demo;

use App\Models\Certificate;
use App\Models\DnsZone;
use App\Models\Import;
use App\Models\Mailbox;
use App\Models\MailDomain;
use App\Models\UptimeCheck;
use App\Models\User;
use App\Models\Website;
use Illuminate\Contracts\Http\Kernel;
use Illuminate\Http\Request;

/**
 * The answers of the API to the requests of the web panel, with the sample data of the
 * demonstration mode. The tests of the web panel use them (web/src/test/fixtures), so the web
 * panel is tested against the answers of the real API and not against a copy of it.
 */
class Fixtures
{
    /** One user of each role in the sample data. */
    public const USERS = ['administrator' => 'admin@example.com', 'reseller' => 'reseller@example.com', 'owner' => 'owner@example.com'];

    /**
     * The requests that the web panel sends when it loads (web/src/api/live.ts). Keep one path on
     * each line, in alphabetical order, so that two pull requests that add paths do not conflict.
     */
    public const PATHS = [
        '/account',
        '/audit?perPage=100',
        '/backup-policy',
        '/certificates',
        '/customers',
        '/database-engines',
        '/database-servers',
        '/dns-accounts',
        '/dns-zones',
        '/identity-providers',
        '/images',
        '/imports',
        '/jobs?perPage=50',
        '/mail-clusters',
        '/mail-server',
        '/mail/aliases',
        '/mail/domains',
        '/mail/mailboxes',
        '/me',
        '/me/identities',
        '/me/notification-preferences',
        '/me/passkeys',
        '/mesh',
        '/networks',
        '/notification-templates',
        '/notifications?perPage=50',
        '/pack-settings',
        '/packs',
        '/panel-certificate',
        '/plans',
        '/plugin-actions',
        '/plugin-pages',
        '/plugin-settings',
        '/plugins',
        '/rollouts',
        '/runtime-images',
        '/servers',
        '/setup',
        '/tokens',
        '/trusted-keys',
        '/uptime-checks?perPage=100&page=1',
        '/uptime-incidents',
        '/usage-alerts',
        '/users',
        '/websites?perPage=100&page=1',
    ];

    /**
     * The requests for each website that the user can see, with "{id}" for the ID of the website.
     * The web panel reads them when the user opens that part of the website. One path on each
     * line, in alphabetical order.
     */
    public const WEBSITE_PATHS = [
        '/websites/{id}/commands',
        '/websites/{id}/cron-jobs',
        '/websites/{id}/databases',
        '/websites/{id}/deploy',
        '/websites/{id}/files/content?path=%2Fpublic%2Findex.php',
        '/websites/{id}/files?path=%2F',
        '/websites/{id}/files?path=%2Fpublic',
        '/websites/{id}/logs?lines=200',
        '/websites/{id}/malware-scans',
        '/websites/{id}/previews',
        '/websites/{id}/security',
        '/websites/{id}/security/events',
        '/websites/{id}/snapshots',
        '/websites/{id}/terminal-sessions',
        '/websites/{id}/uptime-checks',
    ];

    /**
     * @return array<string, array{status: int, body: mixed}> "GET /path" => the answer
     */
    public static function capture(string $role): array
    {
        $user = User::where('email', self::USERS[$role])->firstOrFail();
        $token = $user->createToken('Fixtures')->plainTextToken;
        $paths = self::PATHS;
        foreach (self::websites($user) as $id) {
            foreach (self::WEBSITE_PATHS as $path) {
                $paths[] = str_replace('{id}', $id, $path);
            }
            // The tab "Uptime" of a website reads each check with its history.
            foreach (UptimeCheck::where('website_id', $id)->orderBy('created_at')->pluck('id') as $check) {
                $paths[] = "/websites/{$id}/uptime-checks/{$check}";
            }
        }
        // The page of a DNS zone reads the zone with its records.
        $visible = $user->visibleCustomerIds();
        foreach (DnsZone::when($visible !== null, fn ($q) => $q->whereIn('customer_id', $visible))->orderBy('name')->pluck('id') as $id) {
            $paths[] = "/dns-zones/{$id}";
        }
        // The page of a certificate reads the certificate with its chain.
        foreach (Certificate::when($visible !== null, fn ($q) => $q->whereIn('customer_id', $visible))->orderBy('id')->pluck('id') as $id) {
            $paths[] = "/certificates/{$id}";
        }
        // The page of an import reads the import with its plan or its parts.
        if ($user->isAdministrator()) {
            foreach (Import::orderBy('created_at')->pluck('id') as $id) {
                $paths[] = "/imports/{$id}";
            }
        }
        // The dialogs of a mailbox read these answers. The fixtures have the mailboxes of the first
        // mail domain of the user.
        $domain = MailDomain::when($visible !== null, fn ($q) => $q->whereIn('customer_id', $visible))->orderBy('name')->first();
        foreach (Mailbox::where('mail_domain_id', $domain?->id)->orderBy('local_part')->pluck('id') as $id) {
            $paths[] = "/mail/mailboxes/{$id}/autoresponder";
            $paths[] = "/mail/mailboxes/{$id}/backups";
        }
        $answers = [];
        foreach ($paths as $path) {
            $request = Request::create('/api/v1'.$path, 'GET', server: ['HTTP_ACCEPT' => 'application/json', 'HTTP_AUTHORIZATION' => 'Bearer '.$token]);
            $response = app(Kernel::class)->handle($request);
            $answers['GET '.$path] = ['status' => $response->getStatusCode(), 'body' => json_decode((string) $response->getContent(), true)];
            // The next request must read its own user from its own token.
            app('auth')->forgetGuards();
        }
        $user->tokens()->where('name', 'Fixtures')->delete();

        return $answers;
    }

    /** @return list<string> */
    private static function websites(User $user): array
    {
        $query = Website::query()->orderBy('name');
        if (! $user->isAdministrator()) {
            $own = $user->customer_id;
            $query->whereHas('customer', fn ($q) => $q->where('id', $own)->orWhere('reseller_id', $own));
        }

        return $query->pluck('id')->all();
    }

    /**
     * Writes one file for each answer in the directory of a role, and removes the files of
     * requests that the panel does not send now. A file has the request, the status and the body.
     *
     * @param  array<string, array{status: int, body: mixed}>  $answers  "GET /path" => the answer
     * @return int the number of files
     */
    public static function write(string $directory, array $answers): int
    {
        if (! is_dir($directory) && ! mkdir($directory, 0755, true)) {
            throw new \RuntimeException("Cannot make the directory {$directory}.");
        }
        $files = [];
        foreach ($answers as $request => $answer) {
            $name = self::fileName($request);
            if (isset($files[$name])) {
                throw new \LogicException("The requests \"{$files[$name]}\" and \"{$request}\" have the same fixture file {$name}.");
            }
            $files[$name] = $request;
            $json = json_encode(['request' => $request, ...$answer], JSON_PRETTY_PRINT | JSON_UNESCAPED_SLASHES | JSON_UNESCAPED_UNICODE)."\n";
            $path = "{$directory}/{$name}";
            if (! is_file($path) || file_get_contents($path) !== $json) {
                file_put_contents($path, $json);
            }
        }
        foreach (glob("{$directory}/*.json") ?: [] as $path) {
            if (! isset($files[basename($path)])) {
                unlink($path);
            }
        }

        return count($files);
    }

    /**
     * Reads the files of a role that write() made.
     *
     * @return array<string, array{status: int, body: mixed}> "GET /path" => the answer, in the order of the requests
     */
    public static function read(string $directory): array
    {
        $answers = [];
        foreach (glob("{$directory}/*.json") ?: [] as $path) {
            $file = json_decode((string) file_get_contents($path), true, flags: JSON_THROW_ON_ERROR);
            $answers[$file['request']] = ['status' => $file['status'], 'body' => $file['body']];
        }
        ksort($answers);

        return $answers;
    }

    /**
     * The name of the file of a request: the path in lower case, with "_" in the place of each
     * other character. For example "GET /websites?perPage=100&page=1" has the file
     * "websites_perpage_100_page_1.json".
     */
    public static function fileName(string $request): string
    {
        $path = strtolower((string) preg_replace('/^GET /', '', $request));

        return trim((string) preg_replace('/[^a-z0-9-]+/', '_', $path), '_').'.json';
    }

    /**
     * The structure of a value: each key path, with "[]" for the items of a list. Two answers
     * with the same structure have the same fields, also when the values are different.
     *
     * @return list<string>
     */
    public static function structure(mixed $value, string $path = ''): array
    {
        if (! is_array($value)) {
            return [$path];
        }
        $paths = [$path];
        foreach ($value as $key => $item) {
            $paths = [...$paths, ...self::structure($item, array_is_list($value) ? $path.'[]' : $path.'.'.$key)];
        }

        return array_values(array_unique($paths));
    }
}
