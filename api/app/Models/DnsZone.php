<?php

namespace App\Models;

use App\Services\Dns\Dnssec;
use Illuminate\Database\Eloquent\Attributes\Fillable;
use Illuminate\Database\Eloquent\Attributes\Hidden;
use Illuminate\Database\Eloquent\Concerns\HasUlids;
use Illuminate\Database\Eloquent\Model;
use Illuminate\Database\Eloquent\Relations\BelongsTo;
use Illuminate\Database\Eloquent\Relations\HasMany;

/**
 * A DNS zone of a customer, for example "example.com". The panel keeps the records. Each
 * server with the role "dns" gets the full zone and answers the queries for it.
 */
#[Fillable([
    'customer_id', 'name', 'serial', 'status', 'delegation', 'delegation_name_servers', 'delegation_checked_at',
    'dnssec', 'dnssec_key', 'dnssec_key_tag', 'dnssec_public_key', 'sync',
])]
#[Hidden(['dnssec_key'])]
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
        ];
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

    /** True when the public DNS sends the queries for the zone to the panel. */
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
        return $this->dnssec && $this->dnssec_public_key ? Dnssec::ds($this->name, $this->dnssec_public_key) : null;
    }
}
