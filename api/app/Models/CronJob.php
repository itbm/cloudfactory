<?php

namespace App\Models;

use Illuminate\Database\Eloquent\Attributes\Fillable;
use Illuminate\Database\Eloquent\Concerns\HasUlids;
use Illuminate\Database\Eloquent\Model;
use Illuminate\Database\Eloquent\Relations\BelongsTo;

/**
 * A command that the agent runs in the container of a website at the times of a schedule.
 * The schedule has the five fields of cron. Its time is UTC.
 */
#[Fillable(['website_id', 'schedule', 'command', 'enabled', 'last_run_at', 'last_exit_code', 'last_output'])]
class CronJob extends Model
{
    use HasUlids;

    protected function casts(): array
    {
        return ['enabled' => 'boolean', 'last_run_at' => 'datetime', 'last_exit_code' => 'integer'];
    }

    public function website(): BelongsTo
    {
        return $this->belongsTo(Website::class);
    }
}
