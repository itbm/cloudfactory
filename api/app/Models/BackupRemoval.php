<?php

namespace App\Models;

use Illuminate\Database\Eloquent\Attributes\Fillable;
use Illuminate\Database\Eloquent\Model;

/** Audit finding R8: a deleted website whose backups the weekly job removes after a set time. */
#[Fillable(['website_id', 'website_name', 'server_id', 'remove_after'])]
class BackupRemoval extends Model
{
    protected function casts(): array
    {
        return ['remove_after' => 'datetime'];
    }
}
