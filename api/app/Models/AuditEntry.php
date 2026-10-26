<?php

namespace App\Models;

use Illuminate\Database\Eloquent\Attributes\Fillable;
use Illuminate\Database\Eloquent\Model;

/** FR-105: who made the change, when, and from which IP address. */
#[Fillable(['user_id', 'actor', 'ip', 'action', 'target', 'customer_id'])]
class AuditEntry extends Model
{
    public const UPDATED_AT = null;
}
