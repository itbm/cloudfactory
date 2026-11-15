<?php

namespace App\Models;

use Illuminate\Database\Eloquent\Attributes\Fillable;
use Illuminate\Database\Eloquent\Attributes\Hidden;
use Illuminate\Database\Eloquent\Concerns\HasUlids;
use Illuminate\Database\Eloquent\Model;
use Illuminate\Database\Eloquent\Relations\BelongsTo;

/**
 * An encrypted tunnel between two servers of the panel. The traffic between the two servers
 * goes through the public internet, and WireGuard encrypts it.
 */
#[Fillable(['server_a_id', 'server_b_id', 'preshared_key', 'pinned', 'status'])]
#[Hidden(['preshared_key'])]
class MeshTunnel extends Model
{
    use HasUlids;

    /** A report of a server is current for this number of seconds. The agent reports each minute. */
    public const CURRENT_SECONDS = 180;

    protected function casts(): array
    {
        return [
            // The secret key of the pair. Only the two servers get it.
            'preshared_key' => 'encrypted',
            'pinned' => 'boolean',
            'status' => 'array',
        ];
    }

    public function serverA(): BelongsTo
    {
        return $this->belongsTo(Server::class, 'server_a_id');
    }

    public function serverB(): BelongsTo
    {
        return $this->belongsTo(Server::class, 'server_b_id');
    }

    /** The ID of the other server of the pair. */
    public function otherId(string $serverId): string
    {
        return $this->server_a_id === $serverId ? $this->server_b_id : $this->server_a_id;
    }

    /**
     * What a server reported last for this tunnel, or null when the report is not current.
     *
     * @return array{reachable: bool, handshakeAt: int, roundTripMicros: int, reportedAt: int}|null
     */
    public function side(string $serverId): ?array
    {
        $side = $this->status[$serverId] ?? null;
        if (! is_array($side) || ($side['reportedAt'] ?? 0) < now()->subSeconds(self::CURRENT_SECONDS)->getTimestamp()) {
            return null;
        }

        return $side;
    }

    /**
     * "up" when the two servers reach each other, "down" when one of them reports that the
     * other does not answer, and "waiting" when a server did not report the tunnel yet.
     */
    public function state(): string
    {
        $sides = [$this->side($this->server_a_id), $this->side($this->server_b_id)];
        if (in_array(false, array_map(fn ($s) => $s['reachable'] ?? null, $sides), true)) {
            return 'down';
        }

        return in_array(null, $sides, true) ? 'waiting' : 'up';
    }
}
