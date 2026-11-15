<?php

namespace App\Models;

use Illuminate\Database\Eloquent\Attributes\Fillable;
use Illuminate\Database\Eloquent\Model;

/** A device or browser that a user signed in from. */
#[Fillable(['user_id', 'fingerprint', 'label', 'ip', 'last_seen_at'])]
class UserDevice extends Model
{
    protected function casts(): array
    {
        return ['last_seen_at' => 'datetime'];
    }
}
