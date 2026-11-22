<?php

namespace App\Models;

use Illuminate\Database\Eloquent\Attributes\Fillable;
use Illuminate\Database\Eloquent\Concerns\HasUlids;
use Illuminate\Database\Eloquent\Model;

/**
 * One backup of one mailbox: a restic snapshot in the repository of the server "server_id".
 * The snapshot has all folders and messages of the mailbox (agent/internal/stalwart/backup.go).
 *
 * "remove_after" is set when the mailbox is removed. The weekly job then removes the
 * snapshots of the mailbox from the repository, and this record.
 */
#[Fillable(['mailbox_id', 'address', 'server_id', 'kind', 'restic_id', 'size_bytes', 'messages', 'last_restore', 'remove_after'])]
class MailboxSnapshot extends Model
{
    use HasUlids;

    protected function casts(): array
    {
        return ['size_bytes' => 'integer', 'messages' => 'integer', 'last_restore' => 'array', 'remove_after' => 'datetime'];
    }
}
