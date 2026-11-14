<?php

namespace App\Models;

use Illuminate\Database\Eloquent\Attributes\Fillable;
use Illuminate\Database\Eloquent\Concerns\HasUlids;
use Illuminate\Database\Eloquent\Model;
use Illuminate\Database\Eloquent\Relations\BelongsTo;
use Illuminate\Database\Eloquent\Relations\HasMany;

/**
 * A database server of a customer: one database container with one engine on a server. It has
 * its own limits, and a customer cannot use the memory, the disk space or the connections of
 * the database server of a different customer.
 *
 * All websites of the customer on the same server can connect to the database server, on the
 * host name of the engine and the port of the database server.
 */
#[Fillable(['customer_id', 'server_id', 'plan_id', 'engine', 'name', 'port', 'limits', 'status', 'usage'])]
class DatabaseServer extends Model
{
    use HasUlids;

    /** The agent makes the database server, or starts it for the first time. */
    public const STARTING = 'starting';

    public const RUNNING = 'running';

    /** The agent reports that the container does not operate. */
    public const STOPPED = 'stopped';

    /** The agent could not make the database server. */
    public const FAILED = 'failed';

    /** The agent removes the database server. It gets no new database. */
    public const REMOVING = 'removing';

    /** The host name of each engine in a website container. */
    public const HOSTS = ['mariadb' => 'mariadb.internal', 'postgresql' => 'postgres.internal', 'valkey' => 'valkey.internal'];

    /**
     * The first port of each engine on the address of a customer. A customer can have PORTS
     * database servers of each engine on a server. The agent has the same values
     * (quadlet.DatabasePort and quadlet.DatabasePorts).
     */
    public const FIRST_PORT = ['mariadb' => 3306, 'postgresql' => 5432, 'valkey' => 6379];

    public const PORTS = 50;

    /** The smallest limits of a database server: 512 MB of memory and 1 GB of disk space. */
    public const MIN_MEMORY = 512 * 1024 ** 2;

    public const MIN_DISK = 1024 ** 3;

    protected function casts(): array
    {
        return ['limits' => 'array', 'usage' => 'array', 'port' => 'integer'];
    }

    public function customer(): BelongsTo
    {
        return $this->belongsTo(Customer::class);
    }

    /** The server that has the container. */
    public function server(): BelongsTo
    {
        return $this->belongsTo(Server::class);
    }

    public function plan(): BelongsTo
    {
        return $this->belongsTo(Plan::class);
    }

    public function databases(): HasMany
    {
        return $this->hasMany(Database::class);
    }

    public function users(): HasMany
    {
        return $this->hasMany(DatabaseUser::class);
    }

    /** The host name that a website container uses for the database server. */
    public function host(): string
    {
        return self::HOSTS[$this->engine];
    }

    /** The database server as the agent gets it. */
    public function forAgent(): array
    {
        return [
            'id' => $this->id,
            'customer' => $this->customer_id,
            'engine' => $this->engine,
            'port' => $this->port,
            'limits' => [
                'vcpus' => (float) ($this->limits['vcpus'] ?? 0),
                'memoryBytes' => (int) ($this->limits['memoryBytes'] ?? 0),
                'diskBytes' => (int) ($this->limits['diskBytes'] ?? 0),
            ],
        ];
    }
}
