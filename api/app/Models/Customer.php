<?php

namespace App\Models;

use Illuminate\Database\Eloquent\Attributes\Fillable;
use Illuminate\Database\Eloquent\Concerns\HasUlids;
use Illuminate\Database\Eloquent\Factories\HasFactory;
use Illuminate\Database\Eloquent\Model;
use Illuminate\Database\Eloquent\Relations\BelongsTo;
use Illuminate\Database\Eloquent\Relations\HasMany;

#[Fillable(['name', 'email', 'role', 'plan_id', 'reseller_id', 'branding', 'suspended', 'suspended_by', 'plan_ids'])]
class Customer extends Model
{
    use HasFactory, HasUlids;

    protected function casts(): array
    {
        return ['branding' => 'array', 'suspended' => 'boolean', 'plan_ids' => 'array'];
    }

    public function plan(): BelongsTo
    {
        return $this->belongsTo(Plan::class);
    }

    public function websites(): HasMany
    {
        return $this->hasMany(Website::class);
    }

    public function users(): HasMany
    {
        return $this->hasMany(User::class);
    }

    public function reseller(): BelongsTo
    {
        return $this->belongsTo(Customer::class, 'reseller_id');
    }

    /**
     * R-07: the plans that this reseller can give to its customers. The administrator sets them.
     * With no list, the reseller can give only its own plan.
     *
     * @return list<string>
     */
    public function allowedPlanIds(): array
    {
        return array_values(array_unique([$this->plan_id, ...($this->plan_ids ?? [])]));
    }

    /** CF-08: suspended by itself, or through its reseller. */
    public function isSuspended(): bool
    {
        return $this->suspended || ($this->reseller_id !== null && ($this->reseller?->suspended ?? false));
    }

    /**
     * Removes all tokens of the users of this customer and, for a reseller, of the users of
     * its customers. Then a suspension takes effect at once.
     */
    public function revokeTokens(): void
    {
        $customerIds = Customer::where('id', $this->id)->orWhere('reseller_id', $this->id)->pluck('id');
        User::whereIn('customer_id', $customerIds)->each(fn (User $user) => $user->tokens()->delete());
    }
}
