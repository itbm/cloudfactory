<?php

namespace App\Models;

use Illuminate\Database\Eloquent\Attributes\Fillable;
use Illuminate\Database\Eloquent\Concerns\HasUlids;
use Illuminate\Database\Eloquent\Factories\HasFactory;
use Illuminate\Database\Eloquent\Model;
use Illuminate\Database\Eloquent\Relations\HasMany;

/**
 * FR-202: a plan. A website and a database server each have a plan, and a customer has a plan.
 *
 * - "limits" are the limits of each website with the plan.
 * - "database_limits" are the limits of each database server with the plan: "vcpus",
 *   "memoryBytes" and "diskBytes".
 * - "databases" is the number of databases of each database server with the plan. For a
 *   website with the shared database containers of an earlier release, it is the number of
 *   databases of the website.
 * - "websites" and "database_servers" are the numbers of websites and of database servers of a
 *   customer with the plan.
 */
#[Fillable(['name', 'limits', 'websites', 'mailboxes', 'databases', 'php_extensions', 'database_servers', 'database_limits'])]
class Plan extends Model
{
    use HasFactory, HasUlids;

    /** The number of database servers of a customer, when a request for a new plan gives none. */
    public const DEFAULT_DATABASE_SERVERS = 3;

    protected function casts(): array
    {
        return ['limits' => 'array', 'php_extensions' => 'array', 'database_limits' => 'array', 'database_servers' => 'integer'];
    }

    /** Each plan has limits for its database servers, also a plan from a request with none. */
    protected static function booted(): void
    {
        static::saving(function (Plan $plan) {
            $plan->database_servers ??= self::DEFAULT_DATABASE_SERVERS;
            $plan->database_limits ??= self::defaultDatabaseLimits($plan->limits ?? []);
        });
    }

    public function websites(): HasMany
    {
        return $this->hasMany(Website::class);
    }

    /** The database servers that have this plan. */
    public function databaseServers(): HasMany
    {
        return $this->hasMany(DatabaseServer::class);
    }

    /**
     * The limits of a database server when a request gives none: the vCPUs of a website, the
     * half of its memory and the half of its disk space, and not less than a database server
     * needs.
     *
     * @param  array<string, mixed>  $limits  The limits of a website.
     * @return array{vcpus: float, memoryBytes: int, diskBytes: int}
     */
    public static function defaultDatabaseLimits(array $limits): array
    {
        return [
            'vcpus' => (float) ($limits['vcpus'] ?? 1),
            'memoryBytes' => max(DatabaseServer::MIN_MEMORY, intdiv((int) ($limits['memoryBytes'] ?? 0), 2)),
            'diskBytes' => max(DatabaseServer::MIN_DISK, intdiv((int) ($limits['diskBytes'] ?? 0), 2)),
        ];
    }
}
