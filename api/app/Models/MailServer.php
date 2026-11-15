<?php

namespace App\Models;

use Illuminate\Database\Eloquent\Attributes\Fillable;
use Illuminate\Database\Eloquent\Attributes\Hidden;
use Illuminate\Database\Eloquent\Concerns\HasUlids;
use Illuminate\Database\Eloquent\Model;
use Illuminate\Database\Eloquent\Relations\BelongsTo;

/**
 * The SMTP server that sends the emails of the panel (no customer) or of a reseller. The panel
 * keeps the password encrypted, and never shows it again.
 */
#[Fillable(['customer_id', 'enabled', 'host', 'port', 'encryption', 'username', 'password', 'from_address', 'from_name', 'reply_to', 'tested_at', 'test_error'])]
#[Hidden(['password'])]
class MailServer extends Model
{
    use HasUlids;

    protected function casts(): array
    {
        return [
            'enabled' => 'boolean',
            'port' => 'integer',
            'password' => 'encrypted',
            'tested_at' => 'datetime',
        ];
    }

    public function customer(): BelongsTo
    {
        return $this->belongsTo(Customer::class);
    }

    /** The server of the panel, or of a reseller. */
    public static function of(?Customer $customer): ?self
    {
        return static::where('customer_id', $customer?->id)->first();
    }
}
