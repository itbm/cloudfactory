<?php

namespace App\Services\Notifications;

use App\Enums\AgentAction;
use App\Enums\NotificationEvent;
use App\Enums\Role;
use App\Http\Resources\JobResource;
use App\Jobs\SendEmailNotification;
use App\Models\AgentJob;
use App\Models\Customer;
use App\Models\DatabaseServer;
use App\Models\DnsZone;
use App\Models\Domain;
use App\Models\NotificationDelivery;
use App\Models\Release;
use App\Models\Server;
use App\Models\User;
use App\Models\Website;
use App\Services\Certificates\Certificates;
use Illuminate\Database\UniqueConstraintViolationException;
use Illuminate\Support\Collection;
use Illuminate\Support\Facades\Log;
use Illuminate\Support\Str;
use Throwable;

/**
 * Sends the email of an event to the users who want it. Each email is a delivery record. The
 * queue sends it (SendEmailNotification), and tries again when the SMTP server does not answer.
 *
 * A notification never stops the change that caused it: each error is in the log only.
 */
class Notifier
{
    /** The emails of one event that one user gets in one hour. */
    public const MAX_PER_HOUR = 20;

    /**
     * @param  iterable<User>  $users
     * @param  array<string, scalar|null>  $values
     * @return int the number of emails in the queue
     */
    public function send(NotificationEvent $event, iterable $users, array $values, ?string $dedupeKey = null): int
    {
        $count = 0;
        $seen = [];
        foreach ($users as $user) {
            if (isset($seen[$user->id]) || ! Preferences::wants($user, $event)) {
                continue;
            }
            $seen[$user->id] = true;
            try {
                $count += $this->queue($event, $user, $values, $dedupeKey) ? 1 : 0;
            } catch (Throwable $e) {
                Log::warning("The panel could not queue the email {$event->value} for user {$user->id}: {$e->getMessage()}");
            }
        }

        return $count;
    }

    /** @param array<string, scalar|null> $values */
    private function queue(NotificationEvent $event, User $user, array $values, ?string $dedupeKey): bool
    {
        if ($dedupeKey !== null && NotificationDelivery::where('user_id', $user->id)->where('dedupe_key', $dedupeKey)->exists()) {
            return false;
        }
        // Adversarial review, finding A8-03: a customer can make many jobs fail. Each user gets
        // a limited number of emails of one event in one hour.
        if (NotificationDelivery::where('user_id', $user->id)->where('event', $event->value)->where('created_at', '>=', now()->subHour())->count() >= self::MAX_PER_HOUR) {
            return false;
        }
        $reseller = Senders::brandOwner($user);
        $template = Templates::effective($event, $reseller);
        $filled = Templates::valuesFor($user, $values);
        $server = Senders::server($reseller);
        try {
            $delivery = NotificationDelivery::create([
                'user_id' => $user->id,
                'customer_id' => $user->customer_id,
                'sender_id' => $server?->customer_id,
                'event' => $event->value,
                'recipient' => $user->email,
                'subject' => Templates::fill($template['subject'], $filled, oneLine: true),
                'body' => Templates::fill($template['body'], $filled),
                'state' => $server ? 'queued' : 'skipped',
                'error' => $server ? null : 'No SMTP server is set up, or it is turned off.',
                'dedupe_key' => $dedupeKey,
            ]);
        } catch (UniqueConstraintViolationException) {
            // A different process sent the same email at the same time.
            return false;
        }
        if ($server) {
            SendEmailNotification::dispatch($delivery->id)->afterCommit();
        }

        return true;
    }

    /** The users of a customer, and for a customer of a reseller, the users of the reseller. */
    public static function usersOf(?Customer $customer, bool $withReseller = true): Collection
    {
        if (! $customer) {
            return collect();
        }
        $ids = [$customer->id];
        if ($withReseller && $customer->reseller_id) {
            $ids[] = $customer->reseller_id;
        }

        return User::whereIn('customer_id', $ids)->get();
    }

    public static function administrators(): Collection
    {
        // The user of a plugin has the role, but it is not a person and has no mailbox.
        return User::where('role', Role::Administrator->value)->whereNull('plugin_id')->get();
    }

    /**
     * A job failed. A failed backup has its own event. The customer of the website (or of the
     * database server or the DNS zone), its reseller and the administrators get the email.
     */
    public function jobFailed(AgentJob $job, ?Website $website): void
    {
        $customer = $website?->customer
            ?? ($job->database_server_id ? DatabaseServer::find($job->database_server_id)?->customer : null)
            ?? ($job->dns_zone_id ? DnsZone::find($job->dns_zone_id)?->customer : null);
        $server = $job->server?->name ?? '';
        $time = ($job->finished_at ?? now())->format('j M Y H:i T');
        // Adversarial review, finding A3-05: the error text comes from the agent. A customer
        // gets the text that the panel shows to the customer. An administrator gets one line
        // of the full text. The administrators are first: an administrator gets one email.
        $forAdministrators = $job->error === null ? null : Str::limit(trim((string) strtok($job->error, "\r\n")), 300);
        foreach ([[self::administrators(), $forAdministrators], [self::usersOf($customer), JobResource::publicError($job)]] as [$users, $error]) {
            if ($job->action === AgentAction::Backup) {
                $this->send(NotificationEvent::BackupFailed, $users, [
                    'website' => $website?->name ?? '', 'server' => $server, 'error' => $error ?? '', 'time' => $time,
                ], "job:{$job->id}");

                continue;
            }
            $this->send(NotificationEvent::JobFailed, $users, [
                'job' => $job->title, 'website' => $website?->name ?? '-', 'server' => $server, 'error' => $error ?? 'No error message.', 'time' => $time,
            ], "job:{$job->id}");
        }
    }

    /**
     * A customer is suspended, or its suspension is lifted. For a reseller, its customers that
     * the suspension stops also get the email.
     */
    public function suspension(Customer $customer, bool $suspended): void
    {
        $customers = Customer::where('id', $customer->id)->orWhere('reseller_id', $customer->id)->get()
            ->filter(fn (Customer $c) => $c->id === $customer->id || $suspended || ! $c->isSuspended());
        $stamp = now()->format('YmdHis');
        foreach ($customers as $c) {
            $brand = Senders::brand($c->reseller?->role === 'reseller' ? $c->reseller : null);
            $provider = $c->reseller?->role === 'reseller' ? $c->reseller : null;
            $this->send($suspended ? NotificationEvent::AccountSuspended : NotificationEvent::AccountRestored, self::usersOf($c, withReseller: false), [
                'account' => $c->name,
                'provider' => $brand['name'],
                'supportEmail' => (string) ($provider?->branding['supportEmail'] ?? $provider?->email ?? ''),
            ], ($suspended ? 'suspended:' : 'restored:')."{$c->id}:{$stamp}");
        }
    }

    public function newDevice(User $user, string $device, string $ip): void
    {
        $this->send(NotificationEvent::NewDeviceSignIn, [$user], [
            'device' => $device, 'ip' => $ip, 'time' => now()->format('j M Y H:i T'),
        ]);
    }

    /**
     * A usage alert (the "usage and alerts" feature makes the alert). The users of the customer
     * and of its reseller get the email. The key stops a second email for the same alert.
     *
     * @param  array<string, scalar|null>  $values  resource, website, used, limit, percent, level
     */
    public function usageAlert(Customer $customer, array $values, string $key): int
    {
        return $this->send(NotificationEvent::UsageAlert, self::usersOf($customer), ['account' => $customer->name] + $values, $key);
    }

    /**
     * FR-1009: a deploy ran a database migration, and then the website failed its health check.
     * The website kept the new release. The users of the customer and of its reseller get the
     * email, one time for each release.
     */
    public function deployNeedsAttention(Website $website, Release $release): void
    {
        $this->send(NotificationEvent::DeployNeedsAttention, self::usersOf($website->customer), [
            'website' => $website->name,
            'websiteId' => $website->id,
            'branch' => $release->ref,
            'commit' => $release->commit ? substr($release->commit, 0, 7) : '',
            'message' => (string) $release->message,
            // The text that the owner sees (DeployController::visibleError).
            'error' => preg_match('/^the health check failed after the switch: \d+ of \d+ checks failed/', (string) $release->health_error, $m) ? $m[0].'.' : 'The health check failed.',
            'time' => ($release->updated_at ?? now())->format('j M Y H:i T'),
        ], "deploy:{$release->id}");
    }

    public function serverOffline(Server $server): void
    {
        $this->send(NotificationEvent::ServerOffline, self::administrators(), [
            'server' => $server->name,
            'lastSeen' => $server->last_seen_at?->format('j M Y H:i T') ?? 'never',
        ], "offline:{$server->id}:".($server->last_seen_at?->timestamp ?? 0));
    }

    /**
     * The certificate of a domain expires soon, or the panel could not get one. One email for
     * each domain and expiry date, and one for each day that the certificate is missing.
     */
    public function certificate(Domain $domain): void
    {
        $website = $domain->website;
        if (! $website) {
            return;
        }
        $expires = $domain->certificate_expires_at;
        $days = $expires ? max(0, (int) ceil(now()->diffInDays($expires, false))) : 0;
        $key = $expires && $domain->certificate_state === 'valid'
            ? "certificate:{$domain->id}:{$expires->toDateString()}"
            : "certificate:{$domain->id}:{$domain->certificate_state}:".now()->toDateString();
        $this->send(NotificationEvent::CertificateExpiring, self::usersOf($website->customer)->concat(self::administrators()), [
            'domain' => $domain->name,
            'website' => $website->name,
            'expiresAt' => $expires?->format('j M Y') ?? 'no certificate',
            'days' => (string) $days,
            'state' => (string) $domain->certificate_state,
            'advice' => NotificationEvent::certificateAdvice($domain->name, (Certificates::ofWebsite($website)[$domain->name] ?? null)?->source ?? 'automatic'),
        ], $key);
    }
}
