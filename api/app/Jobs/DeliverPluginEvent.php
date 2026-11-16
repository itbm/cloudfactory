<?php

namespace App\Jobs;

use App\Models\Plugin;
use App\Models\PluginDelivery;
use App\Services\Plugins\Caller;
use App\Services\Plugins\Events;
use App\Services\Plugins\Outbound;
use Illuminate\Contracts\Queue\ShouldQueue;
use Illuminate\Foundation\Queue\Queueable;

/**
 * Sends one event to the webhook of a plugin. The body is JSON. The header
 * "Cloudfactory-Signature" has an HMAC of the body with the secret of the plugin.
 *
 * A delivery that fails is sent again after 1 minute, 5 minutes, 30 minutes, 2 hours and
 * 6 hours. After 6 attempts, the delivery is "failed". The administrator can send it again.
 */
class DeliverPluginEvent implements ShouldQueue
{
    use Queueable;

    /** The wait in seconds before each next attempt. */
    public const BACKOFF = [60, 300, 1800, 7200, 21600];

    public int $tries = 1;

    public function __construct(public string $deliveryId) {}

    public function handle(Outbound $outbound): void
    {
        $delivery = PluginDelivery::find($this->deliveryId);
        if (! $delivery || $delivery->state !== PluginDelivery::PENDING) {
            return;
        }
        $plugin = Plugin::find($delivery->plugin_id);
        if (! $plugin || (! $plugin->enabled && $delivery->event !== Events::TEST)) {
            $delivery->update(['state' => PluginDelivery::FAILED, 'error' => 'The plugin is off.']);

            return;
        }
        $url = $plugin->webhookUrl();
        if ($url === null) {
            $delivery->update(['state' => PluginDelivery::FAILED, 'error' => 'The manifest of the plugin has no webhook.']);

            return;
        }

        // The count is in the database before the request. A request that stops the worker
        // then leaves a delivery that the command "cloudfactory:prune-plugin-deliveries" ends.
        $delivery->attempts++;
        $delivery->save();
        [$status, $error] = $this->send($plugin, $delivery, $url, $outbound);
        if ($error === null) {
            $delivery->fill(['state' => PluginDelivery::DELIVERED, 'response_status' => $status, 'error' => null, 'delivered_at' => now()])->save();

            return;
        }
        $delivery->fill(['response_status' => $status, 'error' => mb_substr($error, 0, 500)]);
        $wait = self::BACKOFF[$delivery->attempts - 1] ?? null;
        $delivery->state = $wait === null ? PluginDelivery::FAILED : PluginDelivery::PENDING;
        $delivery->save();
        if ($wait !== null) {
            self::dispatch($delivery->id)->delay(now()->addSeconds($wait));
        }
    }

    /** @return array{0: ?int, 1: ?string} The HTTP status and the error, or null for no error. */
    private function send(Plugin $plugin, PluginDelivery $delivery, string $url, Outbound $outbound): array
    {
        $result = (new Caller($outbound))->post($plugin, $url, $delivery->event, $delivery->id, $delivery->payload, 10);
        $error = $result['error'] === null ? null : str_replace('The plugin answered', 'The webhook answered', $result['error']);

        return [$result['status'], $error];
    }
}
