<?php

namespace App\Models;

use Illuminate\Database\Eloquent\Attributes\Fillable;
use Illuminate\Database\Eloquent\Model;

/** A changed subject and text of one event, for the panel (no customer) or for a reseller. */
#[Fillable(['customer_id', 'event', 'subject', 'body'])]
class NotificationTemplate extends Model {}
