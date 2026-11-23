<?php

namespace App\Models;

use App\Enums\WebsiteStatus;
use Illuminate\Database\Eloquent\Attributes\Fillable;
use Illuminate\Database\Eloquent\Concerns\HasUlids;
use Illuminate\Database\Eloquent\Model;
use Illuminate\Database\Eloquent\Relations\BelongsTo;
use Illuminate\Database\Eloquent\Relations\HasMany;
use Illuminate\Database\Eloquent\Relations\HasOne;

/**
 * An address of a website that the panel requests on a schedule (App\Services\Uptime\Monitor).
 * The check is "down" after a number of failed requests in a row, and "up" again after one
 * request that succeeded.
 */
#[Fillable([
    'website_id', 'domain_id', 'scheme', 'path', 'interval_seconds', 'timeout_seconds', 'expected_status', 'keyword',
    'failures_before_alert', 'enabled', 'state', 'failures', 'failing_since', 'state_changed_at', 'next_check_at',
    'last_checked_at', 'last_status', 'last_response_ms', 'last_error',
])]
class UptimeCheck extends Model
{
    use HasUlids;

    public const PENDING = 'pending';

    public const UP = 'up';

    public const DOWN = 'down';

    /** The state that the API shows for a check that the panel does not run now. */
    public const PAUSED = 'paused';

    protected function casts(): array
    {
        return [
            'enabled' => 'boolean',
            'interval_seconds' => 'integer',
            'timeout_seconds' => 'integer',
            'expected_status' => 'integer',
            'failures_before_alert' => 'integer',
            'failures' => 'integer',
            'last_status' => 'integer',
            'last_response_ms' => 'integer',
            'failing_since' => 'datetime',
            'state_changed_at' => 'datetime',
            'next_check_at' => 'datetime',
            'last_checked_at' => 'datetime',
        ];
    }

    public function website(): BelongsTo
    {
        return $this->belongsTo(Website::class);
    }

    public function domain(): BelongsTo
    {
        return $this->belongsTo(Domain::class);
    }

    public function results(): HasMany
    {
        return $this->hasMany(UptimeResult::class);
    }

    public function incidents(): HasMany
    {
        return $this->hasMany(UptimeIncident::class);
    }

    public function openIncident(): HasOne
    {
        return $this->hasOne(UptimeIncident::class)->whereNull('resolved_at')->latestOfMany('started_at');
    }

    /** The address that the panel requests. */
    public function url(): string
    {
        return "{$this->scheme}://{$this->domain?->name}{$this->path}";
    }

    /**
     * Why the panel does not run the check now, or null when it runs it. The panel runs a
     * check only while the website runs: a stopped website is not down.
     */
    public function pausedReason(): ?string
    {
        $website = $this->website;
        if (! $this->enabled) {
            return 'The check is off.';
        }
        if (! $website || $website->status !== WebsiteStatus::Running) {
            return 'The website does not run.';
        }
        if ($website->customer?->isSuspended()) {
            return 'The customer is suspended.';
        }

        return null;
    }

    /** "pending", "up", "down" or "paused". */
    public function shownState(): string
    {
        return $this->pausedReason() === null ? $this->state : self::PAUSED;
    }
}
