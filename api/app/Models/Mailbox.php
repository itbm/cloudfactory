<?php

namespace App\Models;

use Illuminate\Database\Eloquent\Attributes\Fillable;
use Illuminate\Database\Eloquent\Concerns\HasUlids;
use Illuminate\Database\Eloquent\Model;
use Illuminate\Database\Eloquent\Relations\BelongsTo;

/** One mailbox. The panel keeps no password: the mail server has a hash of it. */
#[Fillable(['customer_id', 'mail_domain_id', 'local_part', 'quota_bytes', 'used_bytes', 'status'])]
class Mailbox extends Model
{
    use HasUlids;

    /** The smallest and the largest size of a mailbox: 100 MB and 1 TB. */
    public const MIN_QUOTA = 100 * 1024 ** 2;

    public const MAX_QUOTA = 1024 ** 4;

    protected function casts(): array
    {
        return ['quota_bytes' => 'integer', 'used_bytes' => 'integer'];
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
