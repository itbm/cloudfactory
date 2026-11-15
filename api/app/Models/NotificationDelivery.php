<?php

namespace App\Models;

use Illuminate\Database\Eloquent\Attributes\Fillable;
use Illuminate\Database\Eloquent\Concerns\HasUlids;
use Illuminate\Database\Eloquent\Model;
use Illuminate\Database\Eloquent\Relations\BelongsTo;

/** One email that the panel sent, or tried to send, to one user. */
#[Fillable(['user_id', 'customer_id', 'sender_id', 'event', 'recipient', 'subject', 'body', 'state', 'error', 'attempts', 'dedupe_key', 'sent_at'])]
class NotificationDelivery extends Model
{
    use HasUlids;

    protected function casts(): array
    {
        return ['attempts' => 'integer', 'sent_at' => 'datetime'];
    }

    public function user(): BelongsTo
    {
        return $this->belongsTo(User::class);
    }
}
