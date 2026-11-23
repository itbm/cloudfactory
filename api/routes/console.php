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

// FR-1004: the deploys on a schedule. The command calls the agents, so it runs in its own
// process and does not delay the other commands.
Schedule::command('cloudfactory:check-branches')->everyMinute()->withoutOverlapping()->runInBackground();

// Batch 1, finding 2: servers that do not report, and jobs with no result.
Schedule::command('cloudfactory:check-jobs')->everyMinute()->withoutOverlapping();

// The database tool: remove the temporary database users of Adminer that expired.
Schedule::command('cloudfactory:expire-adminer-logins')->everyMinute()->withoutOverlapping();

// Usage history: the panel keeps the buckets of 5 minutes for 8 days and of 1 hour for 400 days.
Schedule::command('cloudfactory:prune-usage')->daily()->withoutOverlapping();

// Plugins: the panel keeps the deliveries of events for 30 days.
Schedule::command('cloudfactory:prune-plugin-deliveries')->daily()->withoutOverlapping();

// The weekly malware scan of each website, in the hour of the week of the website.
Schedule::command('cloudfactory:malware-scans')->hourly()->withoutOverlapping();

// Email notifications: the TLS certificates that expire soon. One email for each certificate.
Schedule::command('cloudfactory:check-certificates')->dailyAt('08:00')->withoutOverlapping();

// Account exports: the panel removes an archive when it expires (7 days by default).
Schedule::command('cloudfactory:expire-exports')->hourly()->withoutOverlapping();

// Uptime checks: the checks that are due go to the queue. The queue requests the addresses.
Schedule::command('cloudfactory:uptime')->everyMinute()->withoutOverlapping();

// Uptime checks: the panel keeps the results for 8 days and the closed incidents for 90 days.
Schedule::command('cloudfactory:prune-uptime')->daily()->withoutOverlapping();

// The certificates with DNS validation: the renewals, and the stages of the ACME orders. The
// command runs for up to 50 seconds, so it runs in the background.
Schedule::command('cloudfactory:certificates')->everyMinute()->withoutOverlapping()->runInBackground();

// Imports: the archives of the imports that did not start (docs/imports.md).
Schedule::command('cloudfactory:prune-imports')->hourly()->withoutOverlapping();
