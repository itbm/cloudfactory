<?php

namespace App\Models;

use Illuminate\Database\Eloquent\Attributes\Fillable;
use Illuminate\Database\Eloquent\Model;
use Illuminate\Database\Eloquent\Relations\BelongsTo;

/** The result of one request of an uptime check. The panel keeps it for 8 days. */
#[Fillable(['uptime_check_id', 'checked_at', 'ok', 'status', 'response_ms', 'error'])]
class UptimeResult extends Model
{
    public $timestamps = false;

    protected function casts(): array
    {
        return ['checked_at' => 'datetime', 'ok' => 'boolean', 'status' => 'integer', 'response_ms' => 'integer'];
    }

    public function check(): BelongsTo
    {
        return $this->belongsTo(UptimeCheck::class, 'uptime_check_id');
    }
}
