<?php

namespace App\Models;

use Illuminate\Database\Eloquent\Attributes\Fillable;
use Illuminate\Database\Eloquent\Concerns\HasUlids;
use Illuminate\Database\Eloquent\Model;
use Illuminate\Database\Eloquent\Relations\BelongsTo;

/**
 * One entry of the command log of a website (FR-802): a command that a user ran in the
 * container of the website, with who ran it, when, from which address, and the result.
 */
#[Fillable(['website_id', 'customer_id', 'user_id', 'actor', 'ip', 'source', 'command', 'state', 'exit_code', 'timed_out', 'timeout_seconds', 'output', 'agent_job_id', 'started_at', 'finished_at'])]
class WebsiteCommand extends Model
{
    use HasUlids;

    /** The action "Run a command" of the panel, the API and the CLI. */
    public const SOURCE_RUN = 'run';

    public const RUNNING = 'running';

    /** The command ran. It has an exit code. */
    public const DONE = 'done';

    /** The command did not start, for example because the website stopped. */
    public const FAILED = 'failed';

    /** The size of the end of the output that the panel keeps: the agent sends the same size. */
    public const OUTPUT_MAX = 65536;

    protected function casts(): array
    {
        return [
            'exit_code' => 'integer',
            'timed_out' => 'boolean',
            'timeout_seconds' => 'integer',
            'started_at' => 'datetime',
            'finished_at' => 'datetime',
        ];
    }

    public function website(): BelongsTo
    {
        return $this->belongsTo(Website::class);
    }

    public function job(): BelongsTo
    {
        return $this->belongsTo(AgentJob::class, 'agent_job_id');
    }
}
