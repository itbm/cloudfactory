<?php

namespace App\Models;

use App\Enums\WebsiteStatus;
use App\Models\Concerns\GuardsState;
use Illuminate\Database\Eloquent\Attributes\Fillable;
use Illuminate\Database\Eloquent\Concerns\HasUlids;
use Illuminate\Database\Eloquent\Factories\HasFactory;
use Illuminate\Database\Eloquent\Model;
use Illuminate\Database\Eloquent\Relations\BelongsTo;
use Illuminate\Database\Eloquent\Relations\HasMany;

#[Fillable([
    'name', 'application', 'customer_id', 'server_id', 'plan_id', 'image_id', 'image_tag', 'previous_image_id',
    'previous_image_tag', 'unix_user', 'status', 'worker_mode', 'extensions', 'php_ini', 'limits', 'usage',
    'staging_of', 'managed_by_terraform', 'document_root', 'sftp_enabled', 'sftp_keys',
    'stopped_by_suspension', 'sftp_closed_by_suspension', 'database_instance',
])]
class Website extends Model
{
    use GuardsState, HasFactory, HasUlids;

    /** The table of the permitted changes is in App\Enums\WebsiteStatus. */
    public static function stateAttribute(): string
    {
        return 'status';
    }

    protected function casts(): array
    {
        return [
            'status' => WebsiteStatus::class,
            'worker_mode' => 'boolean',
            'extensions' => 'array',
            'php_ini' => 'array',
            'limits' => 'array',
            'usage' => 'array',
            'managed_by_terraform' => 'boolean',
            'sftp_enabled' => 'boolean',
            'sftp_keys' => 'array',
            'stopped_by_suspension' => 'boolean',
            'sftp_closed_by_suspension' => 'boolean',
        ];
    }

    public function customer(): BelongsTo
    {
        return $this->belongsTo(Customer::class);
    }

    public function server(): BelongsTo
    {
        return $this->belongsTo(Server::class);
    }

    public function plan(): BelongsTo
    {
        return $this->belongsTo(Plan::class);
    }

    public function image(): BelongsTo
    {
        return $this->belongsTo(Image::class);
    }

    public function previousImage(): BelongsTo
    {
        return $this->belongsTo(Image::class, 'previous_image_id');
    }

    public function domains(): HasMany
    {
        return $this->hasMany(Domain::class);
    }

    public function databases(): HasMany
    {
        return $this->hasMany(Database::class);
    }

    /** The databases that a backup and a staging copy include: not the Valkey databases. */
    public function sqlDatabases(): HasMany
    {
        return $this->databases()->whereIn('engine', Database::SQL_ENGINES);
    }

    public function databaseUsers(): HasMany
    {
        return $this->hasMany(DatabaseUser::class);
    }

    public function releases(): HasMany
    {
        return $this->hasMany(Release::class)->latest();
    }

    public function snapshots(): HasMany
    {
        return $this->hasMany(Snapshot::class)->latest();
    }

    public function jobs(): HasMany
    {
        return $this->hasMany(AgentJob::class)->latest();
    }

    /** The image tag that the website uses. */
    public function currentTag(): string
    {
        return $this->image_tag ?? $this->image->tag;
    }

    /** True when a newer signed image is ready for this website. */
    public function updateReady(): bool
    {
        $available = $this->image->available_tag;
        // A website that the panel could not make has no container to update.
        if ($available === null || in_array($this->status, [WebsiteStatus::Stopped, WebsiteStatus::Failed], true)) {
            return false;
        }
        // FR-1110: an image that an administrator added can get a new digest for the same tag.
        // A rollout sets image_tag on each website that it updates.
        $sameTag = $available === $this->image->tag && $this->image_tag === null;

        return $this->currentTag() !== $available || $sameTag;
    }
}
