<?php

namespace App\Jobs;

use App\Models\MailServer;
use App\Models\NotificationDelivery;
use App\Services\Notifications\MailFailed;
use App\Services\Notifications\MailTransport;
use App\Services\Notifications\Senders;
use App\Services\Notifications\SmtpHost;
use App\Services\Notifications\Templates;
use Illuminate\Contracts\Queue\ShouldQueue;
use Illuminate\Foundation\Queue\Queueable;

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

    public function __construct(public string $deliveryId) {}

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
        try {
            // The address of the host can change after the check when the reseller saved it.
            if ($server->customer_id && ($refusal = SmtpHost::refusalForReseller($server->host))) {
                throw new MailFailed($refusal);
            }
            $transport->send($server, $delivery->recipient, $delivery->subject, $delivery->body, Templates::html($delivery->body, $brand));
        } catch (MailFailed $e) {
            $last = $attempt >= self::ATTEMPTS;
            $delivery->update(['attempts' => $attempt, 'error' => $e->getMessage(), 'state' => $last ? 'failed' : 'queued']);
            if (! $last) {
                $this->release($this->backoff()[$attempt - 1] ?? 300);
            }

            return;
        }
        $delivery->update(['attempts' => $attempt, 'state' => 'sent', 'error' => null, 'sent_at' => now()]);
    }
}
