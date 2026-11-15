<?php

namespace App\Events;

use App\Models\UsageAlert;
use Illuminate\Foundation\Events\Dispatchable;

/**
 * A delivery hook: a resource went near its limit ("opened" is true), or an open alert went
 * from "warning" to "critical". A listener can send an email or a message. The panel shows
 * the alert also with no listener.
 */
class UsageAlertRaised
{
    use Dispatchable;

    public function __construct(public UsageAlert $alert, public bool $opened) {}
}
