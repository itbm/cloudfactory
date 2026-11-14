<?php

namespace App\Models;

use Illuminate\Database\Eloquent\Attributes\Fillable;
use Illuminate\Database\Eloquent\Concerns\HasUlids;
use Illuminate\Database\Eloquent\Model;
use Illuminate\Database\Eloquent\Relations\BelongsTo;
use Illuminate\Database\Eloquent\Relations\HasMany;

/** A domain that receives and sends email on the mail server of one server. */
#[Fillable(['customer_id', 'server_id', 'name', 'status', 'records', 'dns_checked_at'])]
class MailDomain extends Model
{
    use HasUlids;

    /** The states of a mail domain, a mailbox and an alias. */
    public const PENDING = 'pending';

    public const ACTIVE = 'active';

    public const FAILED = 'failed';

    public const REMOVING = 'removing';

    protected function casts(): array
    {
        return ['records' => 'array', 'dns_checked_at' => 'datetime'];
    }

    public function customer(): BelongsTo
    {
        return $this->belongsTo(Customer::class);
    }

    public function server(): BelongsTo
    {
        return $this->belongsTo(Server::class);
    }

    public function mailboxes(): HasMany
    {
        return $this->hasMany(Mailbox::class);
    }

    public function aliases(): HasMany
    {
        return $this->hasMany(MailAlias::class);
    }
}
