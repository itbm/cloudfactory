<?php

namespace App\Events;

use App\Models\UsageAlert;
use Illuminate\Foundation\Events\Dispatchable;

/** A delivery hook: the use of the resource of an open alert went down again. */
class UsageAlertResolved
{
    use Dispatchable;

    public function __construct(public UsageAlert $alert) {}
}
