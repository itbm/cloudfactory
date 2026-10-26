<?php

namespace App\Models;

use Illuminate\Database\Eloquent\Attributes\Fillable;
use Illuminate\Database\Eloquent\Concerns\HasUlids;
use Illuminate\Database\Eloquent\Model;
use Illuminate\Database\Eloquent\Relations\BelongsTo;

#[Fillable(['website_id', 'engine', 'name', 'size_bytes'])]
class Database extends Model
{
    use HasUlids;

    public function website(): BelongsTo
    {
        return $this->belongsTo(Website::class);
    }
}
