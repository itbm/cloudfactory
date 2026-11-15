<?php

namespace App\Models;

use Illuminate\Database\Eloquent\Attributes\Fillable;
use Illuminate\Database\Eloquent\Attributes\Hidden;
use Illuminate\Database\Eloquent\Concerns\HasUlids;
use Illuminate\Database\Eloquent\Model;
use Illuminate\Database\Eloquent\Relations\BelongsTo;

/**
 * The Git repository that a website deploys from. The panel keeps the private SSH key, the
 * webhook token and the secrets encrypted. It sends the key and the secrets to the agent with
 * each deploy job, and the agent does not keep them.
 */
#[Fillable(['website_id', 'repository', 'branch', 'web_directory', 'trigger', 'build_commands', 'public_key', 'private_key', 'webhook_token', 'webhook_token_hash', 'secrets'])]
#[Hidden(['private_key', 'webhook_token', 'webhook_token_hash', 'secrets'])]
class GitSource extends Model
{
    use HasUlids;

    public const MANUAL = 'manual';

    public const WEBHOOK = 'webhook';

    protected function casts(): array
    {
        return [
            'build_commands' => 'array',
            'private_key' => 'encrypted',
            'webhook_token' => 'encrypted',
            'secrets' => 'encrypted:array',
        ];
    }

    public function website(): BelongsTo
    {
        return $this->belongsTo(Website::class);
    }

    /** The document root of the website when it serves a release. */
    public function documentRootOf(string $releaseId): string
    {
        return 'releases/'.$releaseId.($this->web_directory === '.' ? '' : '/'.$this->web_directory);
    }
}
