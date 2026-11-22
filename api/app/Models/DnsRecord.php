<?php

namespace App\Models;

use Illuminate\Database\Eloquent\Attributes\Fillable;
use Illuminate\Database\Eloquent\Concerns\HasUlids;
use Illuminate\Database\Eloquent\Model;
use Illuminate\Database\Eloquent\Relations\BelongsTo;

/**
 * One record of a DNS zone. "name" is the name in the zone, without the name of the zone:
 * "@" is the zone itself. "value" has the form of its type, for example an address for an A
 * record. An MX record and an SRV record also have a priority.
 */
#[Fillable(['dns_zone_id', 'name', 'type', 'ttl', 'value', 'priority', 'managed'])]
class DnsRecord extends Model
{
    use HasUlids;

    /** The panel keeps the name servers of the zone. A user cannot change these records. */
    public const NS = 'ns';

    /** The panel made the record for a domain of a website. */
    public const WEBSITE = 'website';

    /**
     * The panel made the record for the DNS validation of a certificate order. The panel
     * removes it after the order. A user cannot change it.
     */
    public const ACME = 'acme';

    protected function casts(): array
    {
        return ['ttl' => 'integer', 'priority' => 'integer'];
    }

    public function zone(): BelongsTo
    {
        return $this->belongsTo(DnsZone::class, 'dns_zone_id');
    }

    /** The record as the agent gets it. */
    public function forAgent(): array
    {
        return [
            'name' => $this->name,
            'type' => $this->type,
            'ttl' => $this->ttl,
            'value' => $this->value,
            'priority' => (int) ($this->priority ?? 0),
        ];
    }
}
