<?php

namespace App\Models;

use Illuminate\Database\Eloquent\Attributes\Fillable;
use Illuminate\Database\Eloquent\Attributes\Hidden;
use Illuminate\Database\Eloquent\Concerns\HasUlids;
use Illuminate\Database\Eloquent\Factories\HasFactory;
use Illuminate\Database\Eloquent\Model;
use Illuminate\Database\Eloquent\Relations\HasMany;
use Illuminate\Support\Facades\Crypt;
use Illuminate\Support\Facades\DB;

#[Fillable([
    'name', 'location', 'roles', 'state', 'agent_url', 'agent_version', 'certificate_fingerprint', 'certificate_pem', 'agent_token_hash',
    'os', 'kernel', 'arch', 'podman_version', 'gvisor_version', 'gvisor_platform', 'bare_metal', 'update_policy',
    'maintenance_window', 'pending_updates', 'reboot_required', 'services', 'usage', 'last_update_at', 'last_seen_at', 'local',
    'sftp_port', 'panel_ca_sha256', 'connection_error', 'connection_checked_at', 'database_engines',
])]
#[Hidden(['agent_token_hash', 'backup_password'])]
class Server extends Model
{
    use HasFactory, HasUlids;

    /**
     * The parts of a server that it can operate without. An agent of an earlier release does
     * not send "optional", so the panel also knows them by name.
     */
    public const OPTIONAL_SERVICES = ['disk-quotas'];

    /** The states of a server that can do work: a degraded server has a part that does not run. */
    public const WORKING = ['healthy', 'degraded'];

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
            'pending_updates' => 'array',
            'services' => 'array',
            'usage' => 'array',
            'database_engines' => 'array',
            'bare_metal' => 'boolean',
            'reboot_required' => 'boolean',
            'local' => 'boolean',
            'sftp_port' => 'integer',
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

    public function websites(): HasMany
    {
        return $this->hasMany(Website::class);
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
}
