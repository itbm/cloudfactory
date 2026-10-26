<?php

namespace App\Models;

use Illuminate\Database\Eloquent\Attributes\Fillable;
use Illuminate\Database\Eloquent\Concerns\HasUlids;
use Illuminate\Database\Eloquent\Model;
use Illuminate\Database\Eloquent\Relations\BelongsTo;

#[Fillable(['website_id', 'engine', 'name', 'databases', 'access'])]
class DatabaseUser extends Model
{
    use HasUlids;

    protected function casts(): array
    {
        return ['databases' => 'array'];
    }

    public function website(): BelongsTo
    {
        return $this->belongsTo(Website::class);
    }
}
