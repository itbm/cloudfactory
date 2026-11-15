<?php

namespace App\Models;

use Illuminate\Database\Eloquent\Attributes\Fillable;
use Illuminate\Database\Eloquent\Attributes\Hidden;
use Illuminate\Database\Eloquent\Concerns\HasUlids;
use Illuminate\Database\Eloquent\Model;
use Illuminate\Database\Eloquent\Relations\BelongsTo;

/**
 * One mailbox. The panel keeps no password: the mail server has a hash of it.
 *
 * "encryption" is not null when the mail server encrypts each new message with the OpenPGP
 * public key of the mailbox ("public_key"). The panel can make the keys: "private_key" then
 * has the private keys, each protected with a password of the mailbox.
 */
#[Fillable(['customer_id', 'mail_domain_id', 'local_part', 'quota_bytes', 'used_bytes', 'status', 'public_key', 'encryption', 'private_key', 'key_password_current'])]
#[Hidden(['private_key'])]
class Mailbox extends Model
{
    use HasUlids;

    /** The smallest and the largest size of a mailbox: 100 MB and 1 TB. */
    public const MIN_QUOTA = 100 * 1024 ** 2;

    public const MAX_QUOTA = 1024 ** 4;

    /** The largest public key block: 64 KB. A block can have more than one key. */
    public const MAX_PUBLIC_KEY = 64 * 1024;

    protected function casts(): array
    {
        return ['quota_bytes' => 'integer', 'used_bytes' => 'integer', 'encryption' => 'array', 'private_key' => 'encrypted', 'key_password_current' => 'boolean'];
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
