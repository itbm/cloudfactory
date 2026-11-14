<?php

namespace App\Models;

use Illuminate\Database\Eloquent\Attributes\Fillable;
use Illuminate\Database\Eloquent\Concerns\HasUlids;
use Illuminate\Database\Eloquent\Model;
use Illuminate\Database\Eloquent\Relations\BelongsTo;
use Illuminate\Database\Eloquent\Relations\BelongsToMany;

#[Fillable(['website_id', 'engine', 'name', 'size_bytes', 'number', 'key_count'])]
class Database extends Model
{
    use HasUlids;

    /** The engines of the shared database containers of a server. */
    public const ENGINES = ['mariadb', 'postgresql', 'valkey'];

    /**
     * The engines with SQL. A backup has a dump of each of their databases, and a staging copy
     * copies them. A user of these engines can have access to more than one database.
     */
    public const SQL_ENGINES = ['mariadb', 'postgresql'];

    /**
     * Valkey: a database is one database number of the Valkey container of its customer, with
     * one user that has the name of the database. A backup and a staging copy do not include
     * its data.
     */
    public const VALKEY = 'valkey';

    protected function casts(): array
    {
        return ['number' => 'integer', 'key_count' => 'integer', 'size_bytes' => 'integer'];
    }

    public function website(): BelongsTo
    {
        return $this->belongsTo(Website::class);
    }

    public function users(): BelongsToMany
    {
        return $this->belongsToMany(DatabaseUser::class, 'database_grants')->withPivot('privileges');
    }
}
