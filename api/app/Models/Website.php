<?php

namespace App\Models;

use App\Enums\WebsiteStatus;
use App\Models\Concerns\GuardsState;
use App\Services\Mesh;
use App\Services\Networks;
use Illuminate\Database\Eloquent\Attributes\Fillable;
use Illuminate\Database\Eloquent\Attributes\Hidden;
use Illuminate\Database\Eloquent\Concerns\HasUlids;
use Illuminate\Database\Eloquent\Factories\HasFactory;
use Illuminate\Database\Eloquent\Model;
use Illuminate\Database\Eloquent\Relations\BelongsTo;
use Illuminate\Database\Eloquent\Relations\HasMany;
use Illuminate\Database\Eloquent\Relations\HasOne;

#[Fillable([
    'name', 'application', 'customer_id', 'server_id', 'plan_id', 'image_id', 'image_tag', 'previous_image_id',
    'previous_image_tag', 'unix_user', 'status', 'worker_mode', 'extensions', 'php_ini', 'limits', 'usage',
    'staging_of', 'managed_by_terraform', 'document_root', 'sftp_enabled', 'sftp_keys',
    'stopped_by_suspension', 'sftp_closed_by_suspension', 'kind', 'command', 'install_command', 'environment',
    'bandwidth_month', 'bandwidth_bytes', 'security', 'malware_scan_schedule',
])]
// The API never gives the values of the environment variables, or the password hashes of the
// protection (App\Services\WebsiteSecurity).
#[Hidden(['environment', 'security'])]
class Website extends Model
{
    use GuardsState, HasFactory, HasUlids;

    /** The defaults of the table, also for a new model before the panel reads it again. */
    protected $attributes = ['malware_scan_schedule' => 'weekly'];

    /** A website with domains. The edge proxy serves it. */
    public const PUBLIC = 'public';

    /** A website with no domain: only the websites of its networks can connect to it. */
    public const PRIVATE = 'private';

    /** A background worker: its container runs a command of the customer and serves no HTTP. */
    public const WORKER = 'worker';

    public const KINDS = [self::PUBLIC, self::PRIVATE, self::WORKER];

    /** The words for a kind in a message. */
    public const KIND_NAMES = [self::PUBLIC => 'website', self::PRIVATE => 'private website', self::WORKER => 'worker'];

    /** True for a website that the edge proxy serves on its domains. */
    public function isPublic(): bool
    {
        return ($this->kind ?? self::PUBLIC) === self::PUBLIC;
    }

    public function isWorker(): bool
    {
        return $this->kind === self::WORKER;
    }

    /**
     * True when the container runs a command of the customer: a worker, or a Node.js or
     * Python website. The two have a start command.
     */
    public function hasStartCommand(): bool
    {
        return $this->isWorker() || (bool) $this->image?->isApp();
    }

    /** The environment variables of the website container, by name. */
    public function variables(): array
    {
        $env = $this->environment ?? [];
        ksort($env);

        return $env;
    }

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
            'environment' => 'encrypted:array',
            'security' => 'array',
        ];
    }

    /**
     * A new website is a member of the network "default" of its customer. The mesh state of
     * the servers changes with the websites (Mesh::refresh).
     */
    protected static function booted(): void
    {
        static::created(function (Website $website) {
            Networks::join($website);
            Mesh::refreshLater();
        });
        static::updated(function (Website $website) {
            if ($website->wasChanged('customer_id')) {
                Networks::leave($website);
                Networks::join($website);
            }
            if ($website->wasChanged(['customer_id', 'server_id', 'kind'])) {
                Mesh::refreshLater();
            }
        });
        static::deleted(function (Website $website) {
            Networks::leave($website);
            Mesh::refreshLater();
        });
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

    /** The databases that a staging copy includes: not the Valkey databases. */
    public function sqlDatabases(): HasMany
    {
        return $this->databases()->whereIn('engine', Database::SQL_ENGINES);
    }

    /**
     * The databases that a backup includes: the SQL databases, and the Valkey databases in a
     * database server on a server whose agent has the feature "valkey-backups" on. The
     * feature is off by default, so a backup has the SQL databases only, as before.
     */
    public function backupDatabases(): HasMany
    {
        return $this->databases()->where(fn ($q) => $q->whereIn('engine', Database::SQL_ENGINES)
            ->orWhere(fn ($q) => $q->where('engine', Database::VALKEY)
                ->whereHas('databaseServer.server', fn ($q) => $q->whereJsonContains('agent_features', 'valkey-backups'))));
    }

    public function databaseUsers(): HasMany
    {
        return $this->hasMany(DatabaseUser::class);
    }

    public function releases(): HasMany
    {
        return $this->hasMany(Release::class)->latest();
    }

    /** The command log of the website (FR-802). */
    public function commands(): HasMany
    {
        return $this->hasMany(WebsiteCommand::class);
    }

    /** The malware scans of the website, the newest first (App\Services\MalwareScans). */
    public function malwareScans(): HasMany
    {
        return $this->hasMany(MalwareScan::class)->latest()->orderByDesc('id');
    }

    public function cronJobs(): HasMany
    {
        return $this->hasMany(CronJob::class)->oldest()->orderBy('id');
    }

    public function gitSource(): HasOne
    {
        return $this->hasOne(GitSource::class);
    }

    /** The previews of the pull requests of the Git source of this website. */
    public function previews(): HasMany
    {
        return $this->hasMany(Preview::class)->orderBy('number');
    }

    /** For a preview website: its pull request and the website of the Git source. */
    public function previewOf(): HasOne
    {
        return $this->hasOne(Preview::class, 'preview_website_id');
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
