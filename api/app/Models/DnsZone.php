<?php

namespace App\Models;

use App\Services\Dns\Dnssec;
use Illuminate\Database\Eloquent\Attributes\Fillable;
use Illuminate\Database\Eloquent\Attributes\Hidden;
use Illuminate\Database\Eloquent\Builder;
use Illuminate\Database\Eloquent\Concerns\HasUlids;
use Illuminate\Database\Eloquent\Model;
use Illuminate\Database\Eloquent\Relations\BelongsTo;
use Illuminate\Database\Eloquent\Relations\HasMany;

/**
 * A DNS zone of a customer, for example "example.com". The panel keeps the records. Each
 * server with the role "dns" gets the full zone and answers the queries for it. A zone with a
 * DNS account goes to an external DNS provider instead (App\Services\Dns\Providers).
 */
#[Fillable([
    'customer_id', 'name', 'serial', 'status', 'delegation', 'delegation_name_servers', 'delegation_checked_at',
    'dnssec', 'dnssec_key', 'dnssec_key_tag', 'dnssec_public_key', 'sync',
    'dns_account_id', 'provider_state', 'handover', 'handover_ends_at',
    'transfer_addresses', 'transfer_notify', 'transfer_tsig_algorithm', 'transfer_tsig_secret', 'transfer_name_servers',
])]
#[Hidden(['dnssec_key', 'transfer_tsig_secret'])]
class DnsZone extends Model
{
    use HasUlids;

    public const ACTIVE = 'active';

    /** The DNS servers remove the zone. The panel removes the record when they are done. */
    public const REMOVING = 'removing';

    /** The public DNS gives the name servers of the panel for the zone. */
    public const HERE = 'here';

    /** The public DNS gives other name servers for the zone. */
    public const ELSEWHERE = 'elsewhere';

    /** The public DNS has no name servers for the zone. */
    public const NONE = 'none';

    public const UNKNOWN = 'unknown';

    protected function casts(): array
    {
        return [
            'serial' => 'integer',
            'dnssec' => 'boolean',
            // The private key of the zone. Each DNS server gets it with the zone.
            'dnssec_key' => 'encrypted',
            'dnssec_key_tag' => 'integer',
            'delegation_name_servers' => 'array',
            'delegation_checked_at' => 'datetime',
            'sync' => 'array',
            'provider_state' => 'array',
            'handover' => 'boolean',
            'handover_ends_at' => 'datetime',
            // The secondaries of the owner of the zone (App\Services\Dns\Secondaries).
            'transfer_addresses' => 'array',
            'transfer_notify' => 'boolean',
            'transfer_tsig_secret' => 'encrypted',
            'transfer_name_servers' => 'array',
        ];
    }

    /** The name of the TSIG key of the transfers of the zone. */
    public function transferKeyName(): string
    {
        return 'transfer.'.$this->name;
    }

    /** The account at the external DNS provider of the zone, or null for the DNS servers of the panel. */
    public function account(): BelongsTo
    {
        return $this->belongsTo(DnsAccount::class, 'dns_account_id');
    }

    /**
     * The zones that the DNS servers of the panel answer for: the zones with no DNS account,
     * and the zones that moved to a provider and are in their handover time.
     */
    public function scopeOnPanelServers(Builder $query): void
    {
        $query->where(fn (Builder $q) => $q->whereNull('dns_account_id')
            ->orWhere(fn (Builder $q) => $q->where('handover', true)
                ->where(fn (Builder $q) => $q->whereNull('handover_ends_at')->orWhere('handover_ends_at', '>', now()))));
    }

    /** True when the DNS servers of the panel answer for the zone. Refer to scopeOnPanelServers. */
    public function onPanelServers(): bool
    {
        return $this->dns_account_id === null || ($this->handover && ($this->handover_ends_at === null || $this->handover_ends_at->isFuture()));
    }

    public function customer(): BelongsTo
    {
        return $this->belongsTo(Customer::class);
    }

    public function records(): HasMany
    {
        return $this->hasMany(DnsRecord::class);
    }

    /** The jobs that sent this zone to a DNS server, or removed it there. */
    public function jobs(): HasMany
    {
        return $this->hasMany(AgentJob::class);
    }

    /** True when the name is the zone or a name in the zone. */
    public function contains(string $name): bool
    {
        return $name === $this->name || str_ends_with($name, '.'.$this->name);
    }

    /** The name of a record for a full name in the zone: "@" or, for example, "www". */
    public function relative(string $name): string
    {
        return $name === $this->name ? '@' : substr($name, 0, -strlen($this->name) - 1);
    }

    /** The full name of a record of the zone. */
    public function absolute(string $record): string
    {
        return $record === '@' ? $this->name : "{$record}.{$this->name}";
    }

    /**
     * True when the public DNS sends the queries for the zone to the panel: to the DNS servers of
     * the panel, or to the name servers that the external DNS provider of the zone gives.
     */
    public function delegatedHere(): bool
    {
        return $this->delegation === self::HERE;
    }

    /**
     * The DS record that the customer gives to the registrar of the domain, or null when
     * DNSSEC is off.
     *
     * @return array{keyTag: int, algorithm: int, digestType: int, digest: string, record: string}|null
     */
    public function ds(): ?array
    {
        if ($this->dns_account_id !== null) {
            // The provider signs the zone with its own key, and gives the DS record.
            $ds = $this->dnssec ? ($this->provider_state['dnssec']['ds'][0] ?? null) : null;

            return $ds ? $ds + ['record' => "{$this->name}. IN DS {$ds['keyTag']} {$ds['algorithm']} {$ds['digestType']} {$ds['digest']}"] : null;
        }

        return $this->dnssec && $this->dnssec_public_key ? Dnssec::ds($this->name, $this->dnssec_public_key) : null;
    }
}
