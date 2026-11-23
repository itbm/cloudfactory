<?php

namespace App\Models;

use Illuminate\Database\Eloquent\Attributes\Fillable;
use Illuminate\Database\Eloquent\Concerns\HasUlids;
use Illuminate\Database\Eloquent\Model;
use Illuminate\Database\Eloquent\Relations\BelongsTo;

/** FR-103: a passkey (WebAuthn credential) of a user, for one panel domain. */
#[Fillable(['user_id', 'name', 'rp_id', 'credential_hash', 'user_handle', 'record', 'backup_eligible', 'backed_up', 'aaguid', 'last_used_at'])]
class Passkey extends Model
{
    use HasUlids;

    protected function casts(): array
    {
        return [
            'backup_eligible' => 'boolean',
            'backed_up' => 'boolean',
            'last_used_at' => 'datetime',
        ];
    }

    public function user(): BelongsTo
    {
        return $this->belongsTo(User::class);
    }
}
