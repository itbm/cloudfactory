<?php

namespace App\Models;

use Illuminate\Database\Eloquent\Attributes\Fillable;
use Illuminate\Database\Eloquent\Concerns\HasUlids;
use Illuminate\Database\Eloquent\Model;

/**
 * A public key that an administrator trusts for plugins. The panel installs a plugin only when
 * one of these keys signed its manifest, unless the developer mode is on.
 */
#[Fillable(['name', 'public_key', 'fingerprint'])]
class PluginKey extends Model
{
    use HasUlids;
}
