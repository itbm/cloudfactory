<?php

namespace App\Models;

use Illuminate\Database\Eloquent\Attributes\Fillable;
use Illuminate\Database\Eloquent\Attributes\Hidden;
use Illuminate\Database\Eloquent\Concerns\HasUlids;
use Illuminate\Database\Eloquent\Model;
use Illuminate\Database\Eloquent\Relations\BelongsTo;
use Illuminate\Support\Facades\Storage;

/**
 * The import of an account from a different hosting panel (docs/imports.md). The web server of
 * the import downloads the backup archive and reads it. The panel then makes the plan, and the
 * import makes the websites, databases, mail and DNS zones of the account.
 */
#[Fillable([
    'customer_id', 'server_id', 'user_id', 'source', 'url', 'file_name', 'size_bytes', 'received_bytes', 'sha256',
    'status', 'panel', 'account', 'description', 'items', 'servers', 'error', 'started_at', 'finished_at',
])]
#[Hidden(['url', 'items', 'description'])]
class Import extends Model
{
    use HasUlids;

    /** The panel receives the archive in parts (source "upload"). */
    public const UPLOADING = 'uploading';

    /** The web server downloads and reads the archive. */
    public const FETCHING = 'fetching';

    /** The plan is ready. The user selects the parts of the account and starts the import. */
    public const READY = 'ready';

    /** The panel makes the records of the selected parts. */
    public const STARTING = 'starting';

    public const RUNNING = 'running';

    /** Each part of the import ended. Some parts can have failed. */
    public const DONE = 'done';

    /** The server could not download or read the archive. */
    public const FAILED = 'failed';

    public const UPLOAD = 'upload';

    public const URL = 'url';

    protected function casts(): array
    {
        return [
            'url' => 'encrypted',
            'size_bytes' => 'integer',
            'received_bytes' => 'integer',
            'description' => 'encrypted:array',
            'items' => 'encrypted:array',
            'servers' => 'array',
            'started_at' => 'datetime',
            'finished_at' => 'datetime',
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

    public function user(): BelongsTo
    {
        return $this->belongsTo(User::class);
    }

    /** The path of the uploaded archive in the local storage of the panel. */
    public function archivePath(): string
    {
        return "imports/{$this->id}/archive";
    }

    /** Removes the uploaded archive from the storage of the panel. */
    public function removeArchive(): void
    {
        Storage::disk('local')->deleteDirectory("imports/{$this->id}");
    }
}
