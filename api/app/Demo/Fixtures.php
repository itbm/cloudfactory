<?php

namespace App\Demo;

use App\Models\Certificate;
use App\Models\DnsZone;
use App\Models\Import;
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

    /** The requests that the web panel sends when it loads (web/src/api/live.ts). */
    public const PATHS = [
        '/me', '/me/passkeys', '/me/identities', '/websites?perPage=100&page=1', '/jobs?perPage=50', '/customers', '/plans', '/tokens', '/backup-policy', '/servers', '/images',
        '/rollouts', '/audit?perPage=100', '/users', '/panel-certificate', '/runtime-images', '/setup', '/account', '/database-servers', '/database-engines', '/dns-zones', '/dns-accounts', '/mesh', '/networks', '/usage-alerts',
        '/me/notification-preferences', '/notifications?perPage=50', '/mail-server', '/notification-templates',
        '/mail/domains', '/mail/mailboxes', '/mail/aliases', '/mail-clusters',
        '/plugin-pages', '/plugin-actions', '/plugins', '/trusted-keys', '/plugin-settings', '/packs', '/pack-settings', '/imports',
        '/certificates',
        // Single sign-on.
        '/identity-providers',
        // The page "Uptime" reads the checks and the incidents when the user opens it.
        '/uptime-checks?perPage=100&page=1', '/uptime-incidents',
    ];

    /**
     * @return array<string, array{status: int, body: mixed}> "GET /path" => the answer
     */
    public static function capture(string $role): array
    {
        $user = User::where('email', self::USERS[$role])->firstOrFail();
        $token = $user->createToken('Fixtures')->plainTextToken;
        $paths = self::PATHS;
        // The web panel reads the databases and the snapshots of each website that the user can see.
        // It reads the cron jobs, the command log, the terminal sessions, the deploys, the files,
        // the logs and the protection of a website when the user opens that part of the website.
        foreach (self::websites($user) as $id) {
            array_push($paths, "/websites/{$id}/databases", "/websites/{$id}/snapshots", "/websites/{$id}/cron-jobs", "/websites/{$id}/commands", "/websites/{$id}/terminal-sessions", "/websites/{$id}/deploy", "/websites/{$id}/previews",
                "/websites/{$id}/files?path=%2F", "/websites/{$id}/files?path=%2Fpublic", "/websites/{$id}/files/content?path=%2Fpublic%2Findex.php", "/websites/{$id}/logs?limit=50&summary=1",
                "/websites/{$id}/security", "/websites/{$id}/security/events", "/websites/{$id}/uptime-checks");
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
