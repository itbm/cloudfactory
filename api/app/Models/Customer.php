<?php

namespace App\Models;

use Illuminate\Database\Eloquent\Attributes\Fillable;
use Illuminate\Database\Eloquent\Concerns\HasUlids;
use Illuminate\Database\Eloquent\Factories\HasFactory;
use Illuminate\Database\Eloquent\Model;
use Illuminate\Database\Eloquent\Relations\BelongsTo;
use Illuminate\Database\Eloquent\Relations\HasMany;

#[Fillable(['name', 'email', 'role', 'plan_id', 'reseller_id', 'branding', 'suspended'])]
class Customer extends Model
{
    use HasFactory, HasUlids;

    protected function casts(): array
    {
        return ['branding' => 'array', 'suspended' => 'boolean'];
    }

    public function plan(): BelongsTo
    {
        return $this->belongsTo(Plan::class);
    }

    public function websites(): HasMany
    {
        return $this->hasMany(Website::class);
    }
}
