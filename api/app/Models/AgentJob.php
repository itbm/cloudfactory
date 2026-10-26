<?php

namespace App\Models;

use App\Enums\AgentAction;
use App\Enums\JobState;
use Illuminate\Database\Eloquent\Attributes\Fillable;
use Illuminate\Database\Eloquent\Concerns\HasUlids;
use Illuminate\Database\Eloquent\Model;
use Illuminate\Database\Eloquent\Relations\BelongsTo;

/** One job for one agent (spec section 6.3, steps 4 to 7). */
#[Fillable(['server_id', 'website_id', 'rollout_id', 'action', 'title', 'ref', 'payload', 'steps', 'step', 'state', 'error', 'started_at', 'finished_at'])]
class AgentJob extends Model
{
    use HasUlids;

    protected function casts(): array
    {
        return [
            'action' => AgentAction::class,
            'state' => JobState::class,
            // The payload can hold a database password for the agent. Keep it encrypted at rest.
            'payload' => 'encrypted:array',
            'steps' => 'array',
            'step' => 'integer',
            'started_at' => 'datetime',
            'finished_at' => 'datetime',
        ];
    }

    public function server(): BelongsTo
    {
        return $this->belongsTo(Server::class);
    }

    public function website(): BelongsTo
    {
        return $this->belongsTo(Website::class);
    }
}
