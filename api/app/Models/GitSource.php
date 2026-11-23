<?php

namespace App\Models;

use Illuminate\Database\Eloquent\Attributes\Fillable;
use Illuminate\Database\Eloquent\Attributes\Hidden;
use Illuminate\Database\Eloquent\Concerns\HasUlids;
use Illuminate\Database\Eloquent\Model;
use Illuminate\Database\Eloquent\Relations\BelongsTo;
use Illuminate\Support\Str;

/**
 * The Git repository that a website deploys from. The panel keeps the private SSH key, the
 * webhook token, the webhook secret and the secrets encrypted. It sends the key and the
 * secrets to the agent with each deploy job, and the agent does not keep them.
 */
#[Fillable(['website_id', 'repository', 'branch', 'web_directory', 'trigger', 'check_minutes', 'deploy_queued', 'queued_commit', 'checked_at', 'check_error', 'build_commands', 'migration_command', 'public_key', 'private_key', 'webhook_token', 'webhook_token_hash', 'webhook_secret', 'secrets'])]
#[Hidden(['private_key', 'webhook_token', 'webhook_token_hash', 'webhook_secret', 'secrets'])]
class GitSource extends Model
{
    use HasUlids;

    /** A deploy starts only when a user starts it. */
    public const MANUAL = 'manual';

    /** A signed request from the Git host to the webhook address starts a deploy. */
    public const WEBHOOK = 'webhook';

    /** The panel checks the branch on a schedule, and deploys each new commit (FR-1004). */
    public const SCHEDULE = 'schedule';

    public const TRIGGERS = [self::MANUAL, self::WEBHOOK, self::SCHEDULE];

    /** The default interval of the branch check, in minutes (FR-1004). */
    public const CHECK_MINUTES = 5;

    protected function casts(): array
    {
        return [
            'build_commands' => 'array',
            'check_minutes' => 'integer',
            'deploy_queued' => 'boolean',
            'checked_at' => 'datetime',
            'private_key' => 'encrypted',
            'webhook_token' => 'encrypted',
            'webhook_secret' => 'encrypted',
            'secrets' => 'encrypted:array',
        ];
    }

    /**
     * The values of a new webhook address and secret. The address has the token, and the Git
     * host signs each request with the secret.
     *
     * @return array{webhook_token: string, webhook_token_hash: string, webhook_secret: string}
     */
    public static function newWebhook(): array
    {
        $token = Str::random(48);

        return ['webhook_token' => $token, 'webhook_token_hash' => hash('sha256', $token), 'webhook_secret' => Str::random(40)];
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
