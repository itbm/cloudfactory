<?php

namespace App\Models;

use Illuminate\Database\Eloquent\Attributes\Fillable;
use Illuminate\Database\Eloquent\Model;

/**
 * Audit finding R8: a deleted website whose backups the weekly job removes after a set time.
 * A row with "files_only" is a website that moved to a different server: the old server
 * removes only the backups of its files.
 */
#[Fillable(['website_id', 'website_name', 'server_id', 'remove_after', 'files_only'])]
class BackupRemoval extends Model
{
    protected function casts(): array
    {
        return ['remove_after' => 'datetime', 'files_only' => 'boolean'];
    }
}
