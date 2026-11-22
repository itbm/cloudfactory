<?php

namespace App\Jobs;

use App\Models\MailServer;
use App\Models\NotificationDelivery;
use App\Services\Notifications\MailFailed;
use App\Services\Notifications\MailTransport;
use App\Services\Notifications\Senders;
use App\Services\Notifications\Templates;
use Illuminate\Contracts\Queue\ShouldQueue;
use Illuminate\Foundation\Queue\Queueable;
use Illuminate\Support\Facades\Crypt;
use Throwable;

/**
 * Sends one email notification. When the SMTP server does not accept the email, the queue
 * tries again after 1 minute and after 5 minutes. Then the delivery is "failed", with the error
 * of the server.
 */
class SendEmailNotification implements ShouldQueue
{
    use Queueable;

    public const ATTEMPTS = 3;

    public int $tries = self::ATTEMPTS;

    /**
     * The secret values of the email are encrypted in $secrets: a JSON object of placeholder
     * and value (Notifier::placeholder). The copy of the email in the panel has only the
     * placeholders, so the panel cannot send the email again after this job.
     */
    public function __construct(public string $deliveryId, public ?string $secrets = null) {}

    /** @return list<int> */
    public function backoff(): array
    {
        return [60, 300];
    }

    public function handle(MailTransport $transport): void
    {
        $delivery = NotificationDelivery::find($this->deliveryId);
        if (! $delivery || $delivery->state !== 'queued') {
            return;
        }
        $server = MailServer::where('customer_id', $delivery->sender_id)->first();
        if (! $server || ! $server->enabled) {
            $delivery->update(['state' => 'skipped', 'error' => 'No SMTP server is set up, or it is turned off.']);

            return;
        }
        $reseller = $delivery->user ? Senders::brandOwner($delivery->user) : null;
        $brand = Senders::brand($reseller);
        // An email to a customer of a reseller has the name of the reseller, also when it goes
        // through the server of the panel.
        if (! $server->from_name || ($reseller && $server->customer_id === null)) {
            $server->from_name = $brand['name'];
        }
        // Replies from a customer of a reseller go to the support address of the reseller.
        if (! $server->reply_to && $reseller && ($support = $reseller->branding['supportEmail'] ?? null)) {
            $server->reply_to = (string) $support;
        }
        $attempt = $delivery->attempts + 1;
        $body = $delivery->body;
        if ($this->secrets !== null) {
            $body = strtr($body, (array) json_decode(Crypt::decryptString($this->secrets), true));
        }
        try {
            $transport->send($server, $delivery->recipient, $delivery->subject, $body, Templates::html($body, $brand));
        } catch (Throwable $e) {
            $last = $attempt >= self::ATTEMPTS;
            $delivery->update(['attempts' => $attempt, 'error' => $e instanceof MailFailed ? $e->getMessage() : 'The panel could not send the email.', 'state' => $last ? 'failed' : 'queued']);
            if (! $e instanceof MailFailed) {
                report($e);
            }
            if (! $last) {
                $this->release($this->backoff()[$attempt - 1] ?? 300);
            }

            return;
        }
        $delivery->update(['attempts' => $attempt, 'state' => 'sent', 'error' => null, 'sent_at' => now()]);
    }

    /** Adversarial review, finding A8-06: a delivery does not stay in the queue with no result. */
    public function failed(?Throwable $e): void
    {
        NotificationDelivery::where('id', $this->deliveryId)->where('state', 'queued')
            ->update(['state' => 'failed', 'error' => 'The panel could not send the email.']);
    }
}
