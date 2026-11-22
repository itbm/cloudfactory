<?php

namespace App\Models;

use Illuminate\Database\Eloquent\Attributes\Fillable;
use Illuminate\Database\Eloquent\Concerns\HasUlids;
use Illuminate\Database\Eloquent\Model;
use Illuminate\Database\Eloquent\Relations\BelongsTo;

/**
 * A time when an uptime check was down: from the first failed request to the first request
 * that succeeded again. The incident is open while "resolved_at" is null.
 */
#[Fillable(['uptime_check_id', 'started_at', 'resolved_at', 'error', 'ended_by_pause'])]
class UptimeIncident extends Model
{
    use HasUlids;

    protected function casts(): array
    {
        return ['started_at' => 'datetime', 'resolved_at' => 'datetime', 'ended_by_pause' => 'boolean'];
    }

    public function check(): BelongsTo
    {
        return $this->belongsTo(UptimeCheck::class, 'uptime_check_id');
    }

    public function isOpen(): bool
    {
        return $this->resolved_at === null;
    }

    /** The length of the incident in seconds, until now for an open incident. */
    public function durationSeconds(): int
    {
        return max(0, (int) $this->started_at->diffInSeconds($this->resolved_at ?? now(), true));
    }
}
