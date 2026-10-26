<?php

namespace App\Models;

use Illuminate\Database\Eloquent\Attributes\Fillable;
use Illuminate\Database\Eloquent\Concerns\HasUlids;
use Illuminate\Database\Eloquent\Model;
use Illuminate\Database\Eloquent\Relations\BelongsTo;
use Illuminate\Database\Eloquent\Relations\BelongsToMany;

/**
 * FR-502: a database user of a website. A user can have access to more than one database of the
 * website, with a permission for each database ("readonly" or "readwrite").
 */
#[Fillable(['website_id', 'engine', 'name', 'access'])]
class DatabaseUser extends Model
{
    use HasUlids;

    public function website(): BelongsTo
    {
        return $this->belongsTo(Website::class);
    }

    public function databases(): BelongsToMany
    {
        return $this->belongsToMany(Database::class, 'database_grants')->withPivot('privileges');
    }

    /** The grants in the form that the agent and the API use. */
    public function grants(): array
    {
        return $this->databases->sortBy('name')->map(fn (Database $d) => [
            'databaseId' => $d->id,
            'database' => $d->name,
            'privileges' => $d->pivot->privileges,
        ])->values()->all();
    }
}
