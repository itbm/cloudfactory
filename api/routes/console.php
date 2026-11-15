<?php

use Illuminate\Support\Facades\Schedule;

// FR-1110: check the registry of each image for a new signed version.
Schedule::command('cloudfactory:check-images')->hourly()->withoutOverlapping();

// FR-901 and FR-902: the command starts the nightly backups once, at the time of the policy.
Schedule::command('cloudfactory:backups')->everyMinute()->withoutOverlapping();

// FR-1112: host updates in the maintenance window of each server.
Schedule::command('cloudfactory:host-updates')->everyTenMinutes()->withoutOverlapping();

// DNS hosting: the name servers of each zone in the public DNS.
Schedule::command('cloudfactory:check-dns')->everyFifteenMinutes()->withoutOverlapping();

// Batch 1, finding 2: servers that do not report, and jobs with no result.
Schedule::command('cloudfactory:check-jobs')->everyMinute()->withoutOverlapping();

// Email notifications: the TLS certificates that expire soon. One email for each certificate.
Schedule::command('cloudfactory:check-certificates')->dailyAt('08:00')->withoutOverlapping();
