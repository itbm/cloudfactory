<?php

namespace App\Models;

use Illuminate\Database\Eloquent\Attributes\Fillable;
use Illuminate\Database\Eloquent\Attributes\Hidden;
use Illuminate\Database\Eloquent\Concerns\HasUlids;
use Illuminate\Database\Eloquent\Model;
use Illuminate\Database\Eloquent\Relations\BelongsTo;
use Illuminate\Database\Eloquent\Relations\HasMany;

/**
 * An account at an external DNS provider, for example Cloudflare. A plugin offers the provider
 * (App\Services\Dns\Providers). The account has the credentials that the plugin needs. An
 * account with no customer belongs to the panel, and each customer can use it.
 */
#[Fillable(['customer_id', 'plugin_id', 'provider', 'name', 'credentials', 'account', 'checked_at'])]
#[Hidden(['credentials'])]
class DnsAccount extends Model
{
    use HasUlids;

    protected function casts(): array
    {
        return [
            'credentials' => 'encrypted:array',
            'checked_at' => 'datetime',
        ];
    }

    public function customer(): BelongsTo
    {
        return $this->belongsTo(Customer::class);
    }

    public function plugin(): BelongsTo
    {
        return $this->belongsTo(Plugin::class);
    }

    public function zones(): HasMany
    {
        return $this->hasMany(DnsZone::class);
    }

    /**
     * The provider in the manifest of the plugin, or null when the plugin does not offer it now.
     *
     * @return array{id: string, name: string, path: string, dnssec: bool, types: list<string>, credentials: list<array{id: string, label: string, secret: bool, optional: bool, hint: string}>}|null
     */
    public function definition(): ?array
    {
        return $this->plugin ? collect($this->plugin->dnsProviders())->firstWhere('id', $this->provider) : null;
    }

    /** The name of the provider for a user, for example "Cloudflare". */
    public function providerName(): string
    {
        return $this->definition()['name'] ?? $this->provider;
    }

    /** True when a zone of the customer can use this account. */
    public function usableBy(string $customerId): bool
    {
        return $this->customer_id === null || $this->customer_id === $customerId;
    }
}
