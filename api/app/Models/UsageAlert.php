<?php

namespace App\Models;

use Illuminate\Database\Eloquent\Attributes\Fillable;
use Illuminate\Database\Eloquent\Concerns\HasUlids;
use Illuminate\Database\Eloquent\Model;
use Illuminate\Database\Eloquent\Relations\BelongsTo;

/**
 * A resource of a website, a server, a database server or a mailbox that is near its limit
 * (App\Services\UsageRecorder). The alert is open while "resolved_at" is null.
 */
#[Fillable(['subject_type', 'subject_id', 'customer_id', 'server_id', 'resource', 'level', 'used', 'limit', 'percent', 'notified_level', 'opened_at', 'resolved_at'])]
class UsageAlert extends Model
{
    use HasUlids;

    public const WEBSITE = 'website';

    public const SERVER = 'server';

    public const DATABASE_SERVER = 'database_server';

    /** FR-603: a mailbox that is almost full. Its resource is "storage". */
    public const MAILBOX = 'mailbox';

    public const WARNING = 'warning';

    public const CRITICAL = 'critical';

    /** The names of the resources for people, for example in an email. */
    public const RESOURCE_NAMES = [
        'cpu' => 'CPU',
        'memory' => 'Memory',
        'disk' => 'Disk',
        'bandwidth' => 'Bandwidth',
        'processes' => 'Processes',
        'storage' => 'Mailbox storage',
    ];

    protected function casts(): array
    {
        return ['used' => 'float', 'limit' => 'float', 'percent' => 'float', 'opened_at' => 'datetime', 'resolved_at' => 'datetime'];
    }

    public function customer(): BelongsTo
    {
        return $this->belongsTo(Customer::class);
    }

    public function server(): BelongsTo
    {
        return $this->belongsTo(Server::class);
    }

    public function isOpen(): bool
    {
        return $this->resolved_at === null;
    }

    /** The website, the server, the database server or the mailbox of the alert, or null when it was removed. */
    public function subject(): ?Model
    {
        return match ($this->subject_type) {
            self::WEBSITE => Website::find($this->subject_id),
            self::SERVER => Server::find($this->subject_id),
            self::DATABASE_SERVER => DatabaseServer::find($this->subject_id),
            self::MAILBOX => Mailbox::find($this->subject_id),
            default => null,
        };
    }
}
