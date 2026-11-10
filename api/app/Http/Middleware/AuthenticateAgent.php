<?php

namespace App\Http\Middleware;

use App\Models\Server;
use Closure;
use Illuminate\Http\Request;
use Symfony\Component\HttpFoundation\Response;

/**
 * Authenticates an agent that reports to the panel. The agent sends the token that it got when it
 * joined, and its client certificate in the TLS connection. The server that ends the TLS
 * connection sends the SHA-256 fingerprint of the certificate in a header, and replaces the value
 * that a client sends:
 *
 * - The edge proxy, for a request to the panel domain. It sets EDGE_HEADER and FINGERPRINT_HEADER.
 * - The HTTPS port of the API on the service address, for the agent on the panel server. It sets
 *   DIRECT_FINGERPRINT_HEADER and removes the two headers of the edge proxy.
 *
 * CF-10: the fingerprint must be the fingerprint of the server. Thus a leaked agent token alone
 * is not sufficient. Only the agent on the panel server ("local") can use the HTTPS port of the
 * API, because the firewall of the panel server opens that port only for root on that server.
 */
class AuthenticateAgent
{
    public const FINGERPRINT_HEADER = 'X-Client-Cert-Sha256';

    public const EDGE_HEADER = 'X-Cloudfactory-Edge';

    public const DIRECT_FINGERPRINT_HEADER = 'X-Cloudfactory-Direct-Cert-Sha256';

    public function handle(Request $request, Closure $next): Response
    {
        $token = $request->bearerToken();
        $server = $token ? Server::where('agent_token_hash', hash('sha256', $token))->first() : null;
        if (! $server) {
            return response()->json(['message' => 'Unknown agent.'], 401);
        }

        if (self::viaEdge($request)) {
            $fingerprint = strtolower((string) $request->header(self::FINGERPRINT_HEADER));
            if (! $server->certificate_fingerprint || ! hash_equals($server->certificate_fingerprint, $fingerprint)) {
                return response()->json(['message' => 'The client certificate does not agree with this agent.'], 401);
            }
        } elseif (! $server->local) {
            return response()->json(['message' => 'This agent must connect through the edge proxy with its client certificate.'], 401);
        } elseif (! self::fromDevelopmentAddress($request)) {
            $fingerprint = self::directFingerprint($request);
            if (! $server->certificate_fingerprint || ! hash_equals($server->certificate_fingerprint, $fingerprint)) {
                return response()->json(['message' => 'The agent on the panel server must connect to the HTTPS port of the API with its client certificate.'], 401);
            }
        }

        $server->forceFill(['last_seen_at' => now()])->save();
        $request->attributes->set('server', $server);

        return $next($request);
    }

    public static function viaEdge(Request $request): bool
    {
        return $request->hasHeader(self::EDGE_HEADER);
    }

    /**
     * The fingerprint of the client certificate of a request to the HTTPS port of the API, or ''.
     * The plain HTTP port of the API removes this header.
     */
    public static function directFingerprint(Request $request): string
    {
        $fingerprint = strtolower((string) $request->header(self::DIRECT_FINGERPRINT_HEADER));

        return preg_match('/^[0-9a-f]{64}$/', $fingerprint) ? $fingerprint : '';
    }

    /**
     * For development only: true when the request comes from an address in
     * cloudfactory.agent.direct_addresses. The list is empty on a server, so that each agent
     * must show its certificate. A developer who runs "php artisan serve" with no TLS can set
     * CF_AGENT_DIRECT_ADDRESSES=127.0.0.1 to let a local agent report with its token only.
     */
    public static function fromDevelopmentAddress(Request $request): bool
    {
        return in_array($request->server('REMOTE_ADDR'), config('cloudfactory.agent.direct_addresses'), true);
    }
}
