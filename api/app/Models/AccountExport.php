<?php

namespace App\Models;

use Illuminate\Database\Eloquent\Attributes\Fillable;
use Illuminate\Database\Eloquent\Attributes\Hidden;
use Illuminate\Database\Eloquent\Concerns\HasUlids;
use Illuminate\Database\Eloquent\Model;
use Illuminate\Database\Eloquent\Relations\BelongsTo;
use Illuminate\Database\Eloquent\Relations\HasMany;

/**
 * An account export (docs/account-export.md): an archive of the account of a customer that its
 * users download. App\Services\Exports\AccountExports makes it.
 */
#[Fillable(['customer_id', 'user_id', 'state', 'files', 'databases', 'description', 'size_bytes', 'error', 'finished_at', 'expires_at'])]
#[Hidden(['description'])]
class AccountExport extends Model
{
    use HasUlids;

    /** The agents send the parts. */
    public const RUNNING = 'running';

    /** The archive is complete. The users can download it until it expires. */
    public const READY = 'ready';

    /** A part did not come. The panel has no archive. */
    public const FAILED = 'failed';

    protected function casts(): array
    {
        return [
            'files' => 'boolean',
            'databases' => 'boolean',
            'description' => 'encrypted:array',
            'size_bytes' => 'integer',
            'finished_at' => 'datetime',
            'expires_at' => 'datetime',
        ];
    }

    public function customer(): BelongsTo
    {
        return $this->belongsTo(Customer::class);
    }

    public function user(): BelongsTo
    {
        return $this->belongsTo(User::class);
    }

    public function parts(): HasMany
    {
        return $this->hasMany(AccountExportPart::class)->orderBy('path');
    }
}
