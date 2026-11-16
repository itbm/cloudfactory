<?php

namespace App\Models;

use Illuminate\Database\Eloquent\Attributes\Fillable;
use Illuminate\Database\Eloquent\Concerns\HasUlids;
use Illuminate\Database\Eloquent\Model;
use Illuminate\Database\Eloquent\Relations\BelongsTo;

/**
 * An address with no mailbox. An alias is a second address of one mailbox of the same customer.
 * A forwarder sends each message to a list of addresses, which can be on other mail servers.
 */
#[Fillable(['customer_id', 'mail_domain_id', 'local_part', 'kind', 'destinations', 'status'])]
class MailAlias extends Model
{
    use HasUlids;

    public const ALIAS = 'alias';

    public const FORWARDER = 'forwarder';

    public const MAX_DESTINATIONS = 50;

    /** The aliases and forwarders of one customer. */
    public const MAX_PER_CUSTOMER = 500;

    /** The destinations of all forwarders of one customer. */
    public const MAX_FORWARD_DESTINATIONS = 500;

    protected function casts(): array
    {
        return ['destinations' => 'array'];
    }

    public function domain(): BelongsTo
    {
        return $this->belongsTo(MailDomain::class, 'mail_domain_id');
    }

    public function customer(): BelongsTo
    {
        return $this->belongsTo(Customer::class);
    }

    public function address(): string
    {
        return $this->local_part.'@'.$this->domain->name;
    }
}
