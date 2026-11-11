<?php

namespace App\Services;

use App\Http\Controllers\Agent\JoinController;
use App\Models\Setting;

/**
 * The TLS certificate of the panel domain. The edge proxy on the panel server gets it with one
 * of two methods:
 *
 * - "internal": the certificate authority (CA) of the panel signs it. This works for all
 *   domains, also localhost and panel.localhost. A browser trusts it only after the
 *   administrator adds the root certificate of the CA. A new panel uses this method.
 * - "letsencrypt": Let's Encrypt gives it with ACME, as for the websites. The domain must be
 *   public, and its DNS must point to the panel server.
 *
 * The agent on the panel server reports the method, the state of the certificate and the root
 * certificate of the CA (agent/internal/actions/panelcert.go).
 */
class PanelCertificate
{
    public const INTERNAL = 'internal';

    public const LETS_ENCRYPT = 'letsencrypt';

    public const METHODS = [self::INTERNAL, self::LETS_ENCRYPT];

    private const SETTING = 'panel_certificate';

    /** The domain of the panel. */
    public static function domain(): string
    {
        return (string) config('cloudfactory.panel_domain');
    }

    /**
     * Whether Let's Encrypt can give a certificate for the domain. The agent has the same rule
     * (caddy.PublicName).
     */
    public static function publicName(string $domain): bool
    {
        $domain = strtolower(rtrim($domain, '.'));
        if (! str_contains($domain, '.') || filter_var($domain, FILTER_VALIDATE_IP) !== false) {
            return false;
        }
        foreach (['.localhost', '.local', '.internal', '.home.arpa', '.test', '.example', '.invalid'] as $suffix) {
            if (str_ends_with($domain, $suffix)) {
                return false;
            }
        }

        return true;
    }

    /**
     * Keeps a report of the agent on the panel server.
     *
     * @param  array<string, mixed>  $report
     */
    public static function record(array $report): void
    {
        $ca = $report['caCertificate'] ?? null;
        Setting::put(self::SETTING, [
            'domain' => $report['domain'] ?? self::domain(),
            'method' => $report['method'] ?? null,
            'state' => $report['state'] ?? 'pending',
            'expiresAt' => $report['expiresAt'] ?? null,
            // Only a valid certificate in PEM format.
            'caCertificate' => is_string($ca) && JoinController::fingerprint($ca) !== null ? trim($ca)."\n" : null,
            'reportedAt' => now()->toIso8601String(),
        ]);
    }

    /** @return array{domain: string, method: ?string, state: string, expiresAt: ?string, caCertificate: ?string, caSha256: ?string, reportedAt: ?string} */
    public static function current(): array
    {
        $saved = (array) Setting::get(self::SETTING, []);
        $ca = $saved['caCertificate'] ?? null;

        return [
            'domain' => $saved['domain'] ?? self::domain(),
            // Null until the agent on the panel server reports.
            'method' => $saved['method'] ?? null,
            'state' => $saved['state'] ?? 'pending',
            'expiresAt' => $saved['expiresAt'] ?? null,
            'caCertificate' => $ca,
            'caSha256' => $ca ? JoinController::fingerprint($ca) : null,
            'reportedAt' => $saved['reportedAt'] ?? null,
        ];
    }

    /**
     * The fingerprint of the root certificate of the panel CA, for the join command of a new
     * server. Null when the panel has a public certificate.
     */
    public static function joinPin(): ?string
    {
        $current = self::current();

        return $current['method'] === self::INTERNAL ? $current['caSha256'] : null;
    }
}
