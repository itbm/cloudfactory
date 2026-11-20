<?php

namespace App\Models;

use Illuminate\Database\Eloquent\Attributes\Fillable;
use Illuminate\Database\Eloquent\Attributes\Hidden;
use Illuminate\Database\Eloquent\Concerns\HasUlids;
use Illuminate\Database\Eloquent\Factories\HasFactory;
use Illuminate\Database\Eloquent\Model;
use Illuminate\Database\Eloquent\Relations\BelongsTo;
use Illuminate\Database\Eloquent\Relations\HasMany;
use Illuminate\Support\Facades\Crypt;
use Illuminate\Support\Facades\DB;

#[Fillable([
    'name', 'location', 'roles', 'state', 'agent_url', 'agent_version', 'certificate_fingerprint', 'certificate_pem', 'agent_token_hash',
    'os', 'kernel', 'arch', 'podman_version', 'gvisor_version', 'gvisor_platform', 'bare_metal', 'update_policy',
    'maintenance_window', 'pending_updates', 'reboot_required', 'services', 'usage', 'last_update_at', 'last_seen_at', 'local',
    'sftp_port', 'panel_ca_sha256', 'connection_error', 'connection_checked_at', 'database_engines', 'database_instances', 'dns_name', 'dns', 'addresses', 'dns_resync',
    'mesh_public_key', 'mesh_address', 'mesh_endpoint', 'mesh_version', 'mesh_wanted_version', 'mesh_networks', 'mesh_websites', 'mesh_moves', 'mesh_state_hash',
    'sftp_port', 'panel_ca_sha256', 'connection_error', 'connection_checked_at', 'database_engines', 'database_instances', 'mail_hostname', 'mail_status',
    'mail_cluster_id', 'mail_cluster_member', 'agent_runtimes', 'agent_features',
])]
#[Hidden(['agent_token_hash', 'backup_password', 'backup_credentials'])]
class Server extends Model
{
    use HasFactory, HasUlids;

    /**
     * The parts of a server that it can operate without. An agent of an earlier release does
     * not send "optional", so the panel also knows them by name.
     */
    public const OPTIONAL_SERVICES = ['disk-quotas', 'database-disk-limits'];

    /** The states of a server that can do work: a degraded server has a part that does not run. */
    public const WORKING = ['healthy', 'degraded'];

    /**
     * True when the server applies the disk limit of a website, and false when it does not. Null
     * when the agent did not report it: an agent of an earlier release, or no report yet.
     */
    public function appliesDiskLimits(): ?bool
    {
        $quotas = collect($this->services ?? [])->firstWhere('name', 'disk-quotas');

        return $quotas === null ? null : ($quotas['state'] ?? '') === 'running';
    }

    /** True when a reported service is a part that the server can operate without. */
    public static function optionalService(array $service): bool
    {
        return ($service['optional'] ?? false) || in_array($service['name'] ?? '', self::OPTIONAL_SERVICES, true);
    }

    /**
     * The reported services, each with "optional" and "detail". An agent of an earlier release
     * reported the nftables service, which is not active before the first restart of the
     * server. The panel does not show that service: the new agent reports "firewall".
     *
     * @return list<array{name: string, state: string, optional: bool, detail: ?string}>
     */
    public static function presentServices(?array $services): array
    {
        return array_values(array_map(fn (array $s) => [
            'name' => $s['name'],
            'state' => $s['state'],
            'optional' => self::optionalService($s),
            'detail' => $s['detail'] ?? null,
        ], array_filter($services ?? [], fn (array $s) => ($s['name'] ?? '') !== 'nftables')));
    }

    /** True when a necessary service of the report does not run. */
    public static function hasFailedService(?array $services): bool
    {
        return collect(self::presentServices($services))->contains(fn ($s) => $s['state'] !== 'running' && ! $s['optional']);
    }

    protected function casts(): array
    {
        return [
            'roles' => 'array',
            'mail_cluster_member' => 'array',
            'pending_updates' => 'array',
            'services' => 'array',
            'usage' => 'array',
            'database_engines' => 'array',
            'agent_runtimes' => 'array',
            'agent_features' => 'array',
            'database_instances' => 'boolean',
            'dns' => 'boolean',
            'addresses' => 'array',
            'dns_resync' => 'array',
            'bare_metal' => 'boolean',
            'reboot_required' => 'boolean',
            'local' => 'boolean',
            'sftp_port' => 'integer',
            'mesh_version' => 'integer',
            'mesh_networks' => 'boolean',
            'mesh_websites' => 'boolean',
            'mesh_moves' => 'boolean',
            'mesh_wanted_version' => 'integer',
            'last_update_at' => 'datetime',
            'last_seen_at' => 'datetime',
            'connection_checked_at' => 'datetime',
        ];
    }

    /**
     * True when the agent of the server can operate the database engine. An agent of an
     * earlier release does not report its engines: it has the engines with SQL only.
     */
    public function hasDatabaseEngine(string $engine): bool
    {
        return in_array($engine, $this->database_engines ?? Database::SQL_ENGINES, true);
    }

    /**
     * True when the agent of the server reports an optional feature that is on, for example
     * "valkey-backups". An agent of an earlier release has no optional features.
     */
    public function hasAgentFeature(string $feature): bool
    {
        return in_array($feature, $this->agent_features ?? [], true);
    }

    /**
     * True when the agent of the server can run the website runtime. An agent of an earlier
     * release does not report its runtimes: it has the built-in runtimes only.
     */
    public function hasRuntime(string $runtime): bool
    {
        return in_array($runtime, $this->agent_runtimes ?? ['frankenphp', 'apache-php-fpm', 'node', 'python', 'custom'], true);
    }

    public function websites(): HasMany
    {
        return $this->hasMany(Website::class);
    }

    /** The database servers of the customers on this server. */
    public function databaseServers(): HasMany
    {
        return $this->hasMany(DatabaseServer::class);
    }

    public function mailDomains(): HasMany
    {
        return $this->hasMany(MailDomain::class);
    }

    /** The mail cluster that this server is a member of, or null. */
    public function mailCluster(): BelongsTo
    {
        return $this->belongsTo(MailCluster::class);
    }

    /** True when the mail server of this server accepts mail domains and mailboxes. */
    public function hasMail(): bool
    {
        return in_array('email', $this->roles ?? [], true) && $this->mail_hostname !== null && $this->mail_status !== null;
    }

    public function jobs(): HasMany
    {
        return $this->hasMany(AgentJob::class);
    }

    /**
     * Audit finding S2: the password of the restic repository of this server. The panel makes
     * it at the first use and keeps it encrypted. Only this server gets it, with each backup
     * job. The lock makes sure that two jobs at the same time get the same password.
     */
    public function backupPassword(): string
    {
        return DB::transaction(function () {
            $value = DB::table('servers')->where('id', $this->id)->lockForUpdate()->value('backup_password');
            if (! is_string($value)) {
                $value = Crypt::encryptString(base64_encode(random_bytes(48)));
                DB::table('servers')->where('id', $this->id)->update(['backup_password' => $value]);
            }

            return Crypt::decryptString($value);
        });
    }

    /**
     * Second audit, finding F08: the key of this server for the object storage, as restic
     * environment variables. It is empty when the server uses the key in the backup policy.
     * The storage provider limits the key to the repository of this server, and only this
     * server gets it.
     *
     * @return array<string, string>
     */
    public function backupCredentials(): array
    {
        $value = DB::table('servers')->where('id', $this->id)->value('backup_credentials');

        return is_string($value) ? (array) json_decode(Crypt::decryptString($value), true) : [];
    }

    /**
     * Sets the key of this server for the object storage. An empty list removes it.
     *
     * @param  array<string, string>  $credentials
     */
    public function setBackupCredentials(array $credentials): void
    {
        $this->forceFill(['backup_credentials' => $credentials === [] ? null : Crypt::encryptString(json_encode((object) $credentials))])->save();
    }
}
