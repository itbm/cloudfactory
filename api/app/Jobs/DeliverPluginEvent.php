<?php

namespace App\Jobs;

use App\Models\Plugin;
use App\Models\PluginDelivery;
use App\Models\Setting;
use App\Services\Plugins\Events;
use App\Services\Plugins\Outbound;
use Illuminate\Contracts\Queue\ShouldQueue;
use Illuminate\Foundation\Queue\Queueable;
use Illuminate\Support\Facades\Http;
use Throwable;

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

        $delivery->attempts++;
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
        $developerMode = (bool) Setting::get('plugins.developer_mode', false);
        $parts = parse_url($url);
        $host = trim($parts['host'] ?? '', '[]');
        $port = $parts['port'] ?? (($parts['scheme'] ?? '') === 'http' ? 80 : 443);
        $addresses = $outbound->addresses($host);
        if ($addresses === []) {
            return [null, "The host name {$host} has no address."];
        }
        foreach ($addresses as $ip) {
            if ($reason = Outbound::refused($ip, $developerMode)) {
                return [null, "The panel does not send events to {$ip}: the address {$reason}."];
            }
        }
        $ip = $addresses[0];
        $body = json_encode($delivery->payload, JSON_UNESCAPED_SLASHES);
        $time = now()->getTimestamp();
        try {
            $response = Http::withOptions([
                'allow_redirects' => false,
                'curl' => [CURLOPT_RESOLVE => ["{$host}:{$port}:".(str_contains($ip, ':') ? "[{$ip}]" : $ip)]],
            ])->timeout(10)->connectTimeout(5)
                ->withHeaders([
                    'User-Agent' => 'Cloudfactory-Plugins/1',
                    'Cloudfactory-Event' => $delivery->event,
                    'Cloudfactory-Delivery' => $delivery->id,
                    'Cloudfactory-Signature' => Events::signature($plugin->webhook_secret, $time, $body),
                ])
                ->withBody($body, 'application/json')
                ->post($url);
        } catch (Throwable $e) {
            return [null, 'No connection: '.$e->getMessage()];
        }
        if ($response->successful()) {
            return [$response->status(), null];
        }

        return [$response->status(), "The webhook answered with HTTP status {$response->status()}."];
    }
}
