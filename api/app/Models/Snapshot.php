<?php

namespace App\Models;

use Illuminate\Database\Eloquent\Attributes\Fillable;
use Illuminate\Database\Eloquent\Concerns\HasUlids;
use Illuminate\Database\Eloquent\Model;
use Illuminate\Database\Eloquent\Relations\BelongsTo;

#[Fillable(['website_id', 'kind', 'restic_id', 'size_bytes', 'databases', 'dump_hashes', 'shared_repository', 'server_id', 'part'])]
class Snapshot extends Model
{
    use HasUlids;

    /** The files of the website and the databases on the server of the website. */
    public const WEBSITE = 'website';

    /** The databases of the website in the database servers on one different server. */
    public const DATABASES = 'databases';

    protected function casts(): array
    {
        return ['databases' => 'array', 'dump_hashes' => 'array', 'shared_repository' => 'boolean'];
    }

    public function website(): BelongsTo
    {
        return $this->belongsTo(Website::class);
    }
}
