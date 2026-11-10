<?php

namespace App\Models;

use Illuminate\Database\Eloquent\Attributes\Fillable;
use Illuminate\Database\Eloquent\Concerns\HasUlids;
use Illuminate\Database\Eloquent\Model;
use Illuminate\Database\Eloquent\Relations\BelongsTo;

#[Fillable(['website_id', 'kind', 'restic_id', 'size_bytes', 'databases', 'mailboxes', 'dump_hashes', 'shared_repository'])]
class Snapshot extends Model
{
    use HasUlids;

    protected function casts(): array
    {
        return ['databases' => 'array', 'mailboxes' => 'array', 'dump_hashes' => 'array', 'shared_repository' => 'boolean'];
    }

    public function website(): BelongsTo
    {
        return $this->belongsTo(Website::class);
    }
}
