<?php

namespace App\Models;

use Illuminate\Database\Eloquent\Attributes\Fillable;
use Illuminate\Database\Eloquent\Concerns\HasUlids;
use Illuminate\Database\Eloquent\Model;

#[Fillable(['website_id', 'ref', 'message', 'state', 'live', 'migration', 'duration_seconds'])]
class Release extends Model
{
    use HasUlids;

    protected function casts(): array
    {
        return ['live' => 'boolean', 'migration' => 'boolean'];
    }
}
