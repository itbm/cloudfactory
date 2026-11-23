<?php

namespace App\Models;

use Illuminate\Database\Eloquent\Attributes\Fillable;
use Illuminate\Database\Eloquent\Concerns\HasUlids;
use Illuminate\Database\Eloquent\Model;
use Illuminate\Database\Eloquent\Relations\BelongsTo;
use Illuminate\Database\Eloquent\Relations\HasMany;

/**
 * A session of the web terminal of a website (FR-802): who opened it, when, from which
 * address, how it ended, and the recording of its output. Its commands are in the command log
 * of the website (WebsiteCommand with the source "terminal").
 */
#[Fillable(['website_id', 'customer_id', 'user_id', 'actor', 'ip', 'state', 'cols', 'rows', 'agent_job_id', 'end_reason', 'exit_code', 'command_count', 'recording', 'recording_cut', 'started_at', 'ended_at'])]
class TerminalSession extends Model
{
    use HasUlids;

    /** The panel sent the job. The agent connects to the relay and waits for the browser. */
    public const OPENING = 'opening';

    /** The shell started. */
    public const OPEN = 'open';

    public const ENDED = 'ended';

    /** The terminal did not open, for example because the browser did not connect. */
    public const FAILED = 'failed';

    /** The terminals of one website that are open at the same time. The agent has the same limit. */
    public const OPEN_MAX = 2;

    /** The longest time of a session. The relay and the agent have the same limit. */
    public const MAX_HOURS = 12;

    /** The maximum size of a recording. The agent has the same limit. */
    public const RECORDING_MAX = 1048576;

    /** The browser ticket is good for this time. The browser connects at once. */
    public const BROWSER_TICKET_SECONDS = 60;

    /** The agent ticket is good for this time from when the panel sends the job. */
    public const AGENT_TICKET_SECONDS = 300;

    protected $hidden = ['recording'];

    protected function casts(): array
    {
        return [
            'cols' => 'integer',
            'rows' => 'integer',
            'exit_code' => 'integer',
            'command_count' => 'integer',
            'recording_cut' => 'boolean',
            'started_at' => 'datetime',
            'ended_at' => 'datetime',
        ];
    }

    public function website(): BelongsTo
    {
        return $this->belongsTo(Website::class);
    }

    public function commands(): HasMany
    {
        return $this->hasMany(WebsiteCommand::class);
    }

    /**
     * The state that the user sees. A session with no end report after the longest time of a
     * session ended: for example, the agent stopped.
     */
    public function visibleState(): string
    {
        if (in_array($this->state, [self::OPENING, self::OPEN], true) && $this->created_at?->lt(now()->subHours(self::MAX_HOURS)->subMinutes(10))) {
            return self::ENDED;
        }

        return $this->state;
    }

    /** The fields of a list, with no recording: a recording can have 1 MiB. */
    public function scopeWithoutRecording($query)
    {
        return $query->select(['id', 'website_id', 'customer_id', 'user_id', 'actor', 'ip', 'state', 'cols', 'rows', 'agent_job_id', 'end_reason',
            'exit_code', 'command_count', 'recording_cut', 'started_at', 'ended_at', 'created_at', 'updated_at'])
            ->selectRaw('(recording is not null) as has_recording');
    }

    public function hasRecording(): bool
    {
        return array_key_exists('has_recording', $this->attributes) ? (bool) $this->attributes['has_recording'] : $this->recording !== null;
    }

    /** The sessions that hold a slot of the website: open, or opening. */
    public function scopeActive($query)
    {
        return $query->whereIn('state', [self::OPENING, self::OPEN])->where('created_at', '>=', now()->subHours(self::MAX_HOURS)->subMinutes(10));
    }
}
