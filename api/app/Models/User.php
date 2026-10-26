<?php

namespace App\Models;

use App\Enums\Role;
use Database\Factories\UserFactory;
use Illuminate\Database\Eloquent\Attributes\Fillable;
use Illuminate\Database\Eloquent\Attributes\Hidden;
use Illuminate\Database\Eloquent\Factories\HasFactory;
use Illuminate\Database\Eloquent\Relations\BelongsTo;
use Illuminate\Foundation\Auth\User as Authenticatable;
use Illuminate\Notifications\Notifiable;
use Laravel\Sanctum\HasApiTokens;

#[Fillable(['name', 'email', 'password', 'role', 'customer_id'])]
#[Hidden(['password', 'remember_token', 'totp_secret'])]
class User extends Authenticatable
{
    /** @use HasFactory<UserFactory> */
    use HasApiTokens, HasFactory, Notifiable;

    protected function casts(): array
    {
        return [
            'email_verified_at' => 'datetime',
            'password' => 'hashed',
            'role' => Role::class,
            'totp_secret' => 'encrypted',
        ];
    }

    public function customer(): BelongsTo
    {
        return $this->belongsTo(Customer::class);
    }

    public function isAdministrator(): bool
    {
        return $this->role === Role::Administrator;
    }

    /**
     * The customer IDs that this user can see. Null means all customers.
     *
     * @return list<string>|null
     */
    public function visibleCustomerIds(): ?array
    {
        if ($this->isAdministrator()) {
            return null;
        }
        if ($this->role === Role::Reseller) {
            return Customer::query()
                ->where('id', $this->customer_id)
                ->orWhere('reseller_id', $this->customer_id)
                ->pluck('id')
                ->all();
        }

        return [$this->customer_id];
    }

    public function canSeeCustomer(?string $customerId): bool
    {
        $ids = $this->visibleCustomerIds();

        return $ids === null || in_array($customerId, $ids, true);
    }
}
