<?php

namespace App\Services\Exports;

use App\Enums\AgentAction;
use App\Enums\JobState;
use App\Enums\WebsiteStatus;
use App\Models\AgentJob;
use App\Models\Customer;
use App\Models\Database;
use App\Models\DatabaseServer;
use App\Models\DatabaseUser;
use App\Models\DnsRecord;
use App\Models\DnsZone;
use App\Models\Domain;
use App\Models\MailAlias;
use App\Models\Mailbox;
use App\Models\MailDomain;
use App\Models\Network;
use App\Models\User;
use App\Models\Website;

/**
 * The description of an account (account.json, format "cf-account/1"). It says what the account
 * has, so that a program can make the account again on a different panel. The import of an
 * account reads the same format (docs/account-export.md).
 *
 * The description has the secrets that the websites need to operate: the environment variables
 * and the public part of the deploy key. It does not have passwords, password hashes or private
 * keys: the panel keeps none of the passwords, and a new panel makes new keys.
 *
 * Each "content" value is the path of a member of the archive and its size. The panel puts the
 * parts of the agents at these paths (AccountExports::paths). The sizes come at the end of the
 * export (AccountExports::build).
 */
class Description
{
    public const FORMAT = 'cf-account/1';

    /**
     * @return array<string, mixed>
     */
    public static function of(Customer $customer, bool $files, bool $databases): array
    {
        $warnings = [];
        $servers = DatabaseServer::where('customer_id', $customer->id)->with('plan')->orderBy('name')->get();
        $serverNames = $servers->pluck('name', 'id');
        $websites = Website::where('customer_id', $customer->id)
            ->with(['image', 'domains', 'databases', 'databaseUsers.databases', 'cronJobs', 'gitSource'])
            ->orderBy('name')->get();
        $websiteNames = $websites->pluck('name', 'id');

        return [
            'format' => self::FORMAT,
            'source' => [
                'panel' => 'cloudfactory',
                'version' => (string) config('cloudfactory.agent.latest_version'),
                'account' => $customer->id,
                'exportedAt' => now()->utc()->toIso8601String(),
            ],
            'customer' => ['name' => $customer->name, 'email' => $customer->email, 'role' => $customer->role],
            // For information: a different panel has its own plans.
            'plan' => $customer->plan ? ['name' => $customer->plan->name] : null,
            'users' => User::where('customer_id', $customer->id)->whereNull('plugin_id')->orderBy('email')->get()
                ->map(fn (User $u) => ['name' => $u->name, 'email' => $u->email, 'role' => $u->role->value])->values()->all(),
            'websites' => $websites->map(function (Website $w) use ($files, $databases, $serverNames, &$warnings) {
                return self::website($w, $files, $databases, $serverNames->all(), $warnings);
            })->values()->all(),
            'databaseServers' => $servers->map(fn (DatabaseServer $s) => [
                'name' => $s->name,
                'engine' => $s->engine,
                'plan' => $s->plan?->name,
                'limits' => $s->limits,
            ])->values()->all(),
            'networks' => Network::where('customer_id', $customer->id)->with('members')->orderBy('name')->get()->map(fn (Network $n) => [
                'name' => $n->name,
                'default' => (bool) $n->is_default,
                'members' => [
                    'websites' => $n->members->whereNotNull('website_id')->map(fn ($m) => $websiteNames[$m->website_id] ?? null)->filter()->sort()->values()->all(),
                    'databaseServers' => $n->members->whereNotNull('database_server_id')->map(fn ($m) => $serverNames[$m->database_server_id] ?? null)->filter()->sort()->values()->all(),
                ],
            ])->values()->all(),
            'dnsZones' => DnsZone::where('customer_id', $customer->id)->with('records')->orderBy('name')->get()->map(fn (DnsZone $z) => [
                'name' => $z->name,
                'dnssec' => (bool) $z->dnssec,
                'records' => $z->records->sortBy([['name', 'asc'], ['type', 'asc'], ['value', 'asc']])->map(fn (DnsRecord $r) => [
                    'name' => $r->name,
                    'type' => $r->type,
                    'ttl' => $r->ttl,
                    'value' => $r->value,
                    'priority' => in_array($r->type, ['MX', 'SRV'], true) ? (int) $r->priority : null,
                    // "ns" and "website": the panel made the record. A different panel makes
                    // its own name servers and the records of its websites.
                    'managed' => $r->managed,
                ])->values()->all(),
                'content' => ['path' => self::zonePath($z->name), 'bytes' => 0],
            ])->values()->all(),
            'mail' => ['domains' => MailDomain::where('customer_id', $customer->id)->with(['mailboxes', 'aliases'])->orderBy('name')->get()->map(fn (MailDomain $d) => [
                'name' => $d->name,
                'mailboxes' => $d->mailboxes->sortBy('local_part')->map(fn (Mailbox $m) => [
                    'localPart' => $m->local_part,
                    'quotaBytes' => (int) $m->quota_bytes,
                    'usedBytes' => (int) $m->used_bytes,
                    // The public key of the encryption of the stored mail. A different panel
                    // can encrypt with it again. The private key is not in the export.
                    'publicKey' => $m->public_key,
                    // The messages are not in this release of the export.
                    'content' => null,
                ])->values()->all(),
                'aliases' => $d->aliases->sortBy('local_part')->map(fn (MailAlias $a) => [
                    'localPart' => $a->local_part,
                    'kind' => $a->kind,
                    'destinations' => array_values($a->destinations ?? []),
                ])->values()->all(),
            ])->values()->all()],
            'warnings' => [
                ...$warnings,
                ...(MailDomain::where('customer_id', $customer->id)->exists() ? [['item' => 'mail', 'message' => 'The export does not have the messages of the mailboxes or their passwords.']] : []),
            ],
        ];
    }

    /**
     * @param  array<string, string>  $serverNames
     * @param  list<array{item: string, message: string}>  $warnings
     * @return array<string, mixed>
     */
    private static function website(Website $w, bool $files, bool $databases, array $serverNames, array &$warnings): array
    {
        $image = $w->image;
        $source = $w->gitSource;
        $withFiles = $files && self::hasFiles($w);
        if ($files && ! $withFiles) {
            $warnings[] = ['item' => "websites/{$w->name}", 'message' => 'The panel could not make this website on its server, so the export has no files of it.'];
        }

        return [
            'name' => $w->name,
            'kind' => $w->kind ?? Website::PUBLIC,
            'application' => $w->application,
            // The disk space that the files used at the last report of the server.
            'diskBytes' => (int) ($w->usage['diskBytes'] ?? 0),
            'runtime' => [
                'family' => $image->family,
                'name' => $image->name,
                'image' => $image->reference.':'.$w->currentTag(),
                'custom' => (bool) $image->custom,
                'phpVersion' => $image->php_version,
                'version' => $image->runtime_version,
                'extensions' => array_values($w->extensions ?? []),
                'phpIni' => (object) ($w->php_ini ?? []),
                'workerMode' => (bool) $w->worker_mode,
                'documentRoot' => $w->document_root ?? ($image->isApp() ? '.' : 'public'),
                'command' => $w->command,
                'installCommand' => $w->install_command,
            ],
            'limits' => $w->limits,
            'domains' => $w->domains->sortBy('name')->map(fn (Domain $d) => ['name' => $d->name, 'kind' => $d->kind->value])->values()->all(),
            'environment' => (object) $w->variables(),
            'cronJobs' => $w->cronJobs->map(fn ($c) => ['schedule' => $c->schedule, 'command' => $c->command, 'enabled' => (bool) $c->enabled])->values()->all(),
            'deploy' => $source ? [
                'repository' => $source->repository,
                'branch' => $source->branch,
                'webDirectory' => $source->web_directory,
                'trigger' => $source->trigger,
                'buildCommands' => array_values($source->build_commands ?? []),
                // The public part of the deploy key. A different panel makes a new key, and the
                // Git host then needs the new public key.
                'publicKey' => $source->public_key,
                // The names of the secrets of the build. Their values are not in the export.
                'secrets' => array_keys($source->secrets ?? []),
            ] : null,
            'sftpKeys' => array_values($w->sftp_keys ?? []),
            'databases' => $w->databases->sortBy('name')->map(function (Database $d) use ($w, $databases, $serverNames, &$warnings) {
                $content = null;
                if ($databases && in_array($d->engine, Database::SQL_ENGINES, true)) {
                    $content = ['path' => self::dumpPath($w->name, $d->engine, $d->name), 'bytes' => 0];
                } elseif ($databases) {
                    $warnings[] = ['item' => "websites/{$w->name}/databases/{$d->name}", 'message' => 'The export does not have the data of Valkey databases.'];
                }

                return [
                    'engine' => $d->engine,
                    'name' => $d->name,
                    'server' => $serverNames[$d->database_server_id] ?? null,
                    'content' => $content,
                ];
            })->values()->all(),
            'databaseUsers' => $w->databaseUsers->sortBy('name')->map(fn (DatabaseUser $u) => [
                'engine' => $u->engine,
                'name' => $u->name,
                'access' => $u->access,
                'grants' => array_map(fn (array $g) => ['database' => $g['database'], 'privileges' => $g['privileges']], $u->grants()),
            ])->values()->all(),
            'content' => [
                'files' => $withFiles ? ['path' => self::filesPath($w->name), 'bytes' => 0, 'exclude' => []] : null,
            ],
        ];
    }

    /**
     * A website has files on its server when the panel could make it. A website that failed
     * and that no job made has no Unix user and no directory on its server.
     */
    public static function hasFiles(Website $website): bool
    {
        return $website->status !== WebsiteStatus::Failed || AgentJob::where('website_id', $website->id)
            ->where('action', AgentAction::CreateWebsite->value)->where('state', JobState::Succeeded->value)->exists();
    }

    public static function filesPath(string $website): string
    {
        return "websites/{$website}/files.tar.gz";
    }

    /** The MariaDB dump is SQL text with gzip. The PostgreSQL dump has the custom format of pg_dump. */
    public static function dumpPath(string $website, string $engine, string $database): string
    {
        $extension = $engine === 'mariadb' ? 'sql.gz' : 'pgdump';

        return "websites/{$website}/databases/{$engine}-{$database}.{$extension}";
    }

    public static function zonePath(string $zone): string
    {
        return "dns/{$zone}.zone";
    }
}
