<?php

namespace App\Models;

use App\Enums\Role;
use Database\Factories\UserFactory;
use Illuminate\Database\Eloquent\Attributes\Fillable;
use Illuminate\Database\Eloquent\Attributes\Hidden;
use Illuminate\Database\Eloquent\Casts\Attribute;
use Illuminate\Database\Eloquent\Factories\HasFactory;
use Illuminate\Database\Eloquent\Relations\BelongsTo;
use Illuminate\Foundation\Auth\User as Authenticatable;
use Illuminate\Notifications\Notifiable;
use Laravel\Sanctum\HasApiTokens;

#[Fillable(['name', 'email', 'password', 'role', 'customer_id', 'plugin_id'])]
#[Hidden(['password', 'remember_token', 'totp_secret', 'totp_last_step'])]
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
            'must_change_password' => 'boolean',
            'temporary_password_at' => 'datetime',
            'notification_preferences' => 'array',
        ];
    }

    /**
     * The form of an email in the panel: lower case, with no space at the start or the end.
     * With one form, "Ann@example.com" and "ann@example.com" are one account with one sign-in
     * lock (adversarial review, finding A1-02).
     */
    public static function normalEmail(string $email): string
    {
        return mb_strtolower(trim($email));
    }

    /** The panel keeps each email in its normal form. */
    protected function email(): Attribute
    {
        return Attribute::make(set: fn (?string $value) => $value === null ? null : self::normalEmail($value));
    }

    public function customer(): BelongsTo
    {
        return $this->belongsTo(Customer::class);
    }

    /** The user of a plugin. Nobody signs in as this user. Only the token of the plugin uses it. */
    public function isPlugin(): bool
    {
        return $this->plugin_id !== null;
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

    /** CF-08: the user belongs to a suspended customer, or to a customer of a suspended reseller. */
    public function isSuspended(): bool
    {
        return $this->customer_id !== null && ($this->customer?->isSuspended() ?? false);
    }

    public function canSeeCustomer(?string $customerId): bool
    {
        $ids = $this->visibleCustomerIds();

        return $ids === null || in_array($customerId, $ids, true);
    }
}
