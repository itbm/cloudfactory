<?php

namespace App\Models;

use Illuminate\Database\Eloquent\Attributes\Fillable;
use Illuminate\Database\Eloquent\Concerns\HasUlids;
use Illuminate\Database\Eloquent\Model;
use Illuminate\Database\Eloquent\Relations\BelongsTo;

/** One WP-CLI command that a user ran in the container of a website, and its result. */
#[Fillable(['website_id', 'agent_job_id', 'user_id', 'command', 'status', 'exit_code', 'output', 'ran_at'])]
class WordPressCommand extends Model
{
    use HasUlids;

    protected $table = 'wordpress_commands';

    protected function casts(): array
    {
        return ['exit_code' => 'integer', 'ran_at' => 'datetime'];
    }

    public function website(): BelongsTo
    {
        return $this->belongsTo(Website::class);
    }

    public function user(): BelongsTo
    {
        return $this->belongsTo(User::class);
    }
}
