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
#[Fillable(['website_id', 'customer_id', 'user_id', 'actor', 'ip', 'source', 'terminal_session_id', 'command', 'state', 'exit_code', 'timed_out', 'timeout_seconds', 'output', 'agent_job_id', 'started_at', 'finished_at'])]
class WebsiteCommand extends Model
{
    use HasUlids;

    /** The action "Run a command" of the panel, the API and the CLI. */
    public const SOURCE_RUN = 'run';

    /**
     * A command that a user typed in the web terminal. The agent finds it in the output of the
     * terminal. It has no exit code and no output: the recording of the session has the output.
     */
    public const SOURCE_TERMINAL = 'terminal';

    /** The longest command line from the terminal. The agent has the same limit. */
    public const TERMINAL_COMMAND_MAX = 2000;

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

    public function terminalSession(): BelongsTo
    {
        return $this->belongsTo(TerminalSession::class);
    }

    public function job(): BelongsTo
    {
        return $this->belongsTo(AgentJob::class, 'agent_job_id');
    }
}
