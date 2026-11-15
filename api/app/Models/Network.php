<?php

namespace App\Models;

use Illuminate\Database\Eloquent\Attributes\Fillable;
use Illuminate\Database\Eloquent\Concerns\HasUlids;
use Illuminate\Database\Eloquent\Model;
use Illuminate\Database\Eloquent\Relations\BelongsTo;
use Illuminate\Database\Eloquent\Relations\HasMany;

/**
 * A private network of a customer. Its members are websites and database servers of the
 * customer. A website can connect to each other member of a network that it is in.
 *
 * Each customer has the network "default". The plan of the customer gives the number of
 * named networks in addition to it.
 */
#[Fillable(['customer_id', 'name', 'is_default'])]
class Network extends Model
{
    use HasUlids;

    /** The name of the network that each customer has. */
    public const DEFAULT = 'default';

    protected function casts(): array
    {
        return ['is_default' => 'boolean'];
    }

    public function customer(): BelongsTo
    {
        return $this->belongsTo(Customer::class);
    }

    /** The name of a named network: lower-case letters, digits and "-". */
    public const NAME = '/^[a-z0-9]([a-z0-9-]{0,38}[a-z0-9])?$/';

    public function members(): HasMany
    {
        return $this->hasMany(NetworkMember::class);
    }
}
