<?php

use Illuminate\Support\Facades\Schedule;

// FR-1110: check the registry of each image for a new signed version.
Schedule::command('cloudfactory:check-images')->hourly()->withoutOverlapping();
