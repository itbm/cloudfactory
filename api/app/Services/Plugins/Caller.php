<?php

namespace App\Services\Plugins;

use App\Models\Plugin;
use App\Models\Setting;
use Illuminate\Http\Client\Response;
use Illuminate\Support\Facades\Http;
use Throwable;

/**
 * Sends one signed request from the panel to a plugin: an event, a before hook or an action.
 * The body is JSON. "Cloudfactory-Signature" has an HMAC of the body with the webhook secret of
 * the plugin. The panel resolves the host name one time, refuses the blocked addresses
 * (Outbound::refused), connects to the address that it checked, and does not follow a redirect.
 */
class Caller
{
    public function __construct(private Outbound $outbound) {}

    /**
     * @param  array<string, mixed>  $payload
     * @return array{status: ?int, response: ?Response, error: ?string} The error is null when
     *                                                                  the plugin answered 2xx.
     */
    public function post(Plugin $plugin, string $url, string $event, string $id, array $payload, int $timeout): array
    {
        $developerMode = (bool) Setting::get('plugins.developer_mode', false) || $this->outbound->permitsLoopback();
        $parts = parse_url($url);
        $host = trim($parts['host'] ?? '', '[]');
        $port = $parts['port'] ?? (($parts['scheme'] ?? '') === 'http' ? 80 : 443);
        $addresses = $this->outbound->addresses($host);
        if ($addresses === []) {
            return ['status' => null, 'response' => null, 'error' => "The host name {$host} has no address."];
        }
        foreach ($addresses as $ip) {
            if ($reason = Outbound::refused($ip, $developerMode)) {
                return ['status' => null, 'response' => null, 'error' => "The panel does not connect to {$ip}: the address {$reason}."];
            }
        }
        $ip = $addresses[0];
        $body = json_encode($payload, JSON_UNESCAPED_SLASHES);
        $time = now()->getTimestamp();
        try {
            $response = Http::withOptions([
                'allow_redirects' => false,
                'curl' => [CURLOPT_RESOLVE => ["{$host}:{$port}:".(str_contains($ip, ':') ? "[{$ip}]" : $ip)]],
            ])->timeout($timeout)->connectTimeout(min(5, $timeout))
                ->withHeaders([
                    'User-Agent' => 'Cloudfactory-Plugins/1',
                    'Cloudfactory-Event' => $event,
                    'Cloudfactory-Delivery' => $id,
                    'Cloudfactory-Signature' => Events::signature($plugin->webhook_secret, $time, $body),
                ])
                ->withBody($body, 'application/json')
                ->post($url);
        } catch (Throwable $e) {
            return ['status' => null, 'response' => null, 'error' => 'No connection: '.$e->getMessage()];
        }
        if ($response->successful()) {
            return ['status' => $response->status(), 'response' => $response, 'error' => null];
        }

        return ['status' => $response->status(), 'response' => $response, 'error' => "The plugin answered with HTTP status {$response->status()}."];
    }
}
