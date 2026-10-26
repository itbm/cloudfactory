<?php

namespace App\Http\Middleware;

use App\Models\Server;
use Closure;
use Illuminate\Http\Request;
use Symfony\Component\HttpFoundation\Response;

/**
 * Authenticates an agent that reports to the panel. The agent sends the token that it got when it
 * joined. The edge proxy also asks for the client certificate of the agent and sends its SHA-256
 * fingerprint in a header. The edge proxy sets both headers on each request, and replaces the
 * values that a client sends.
 *
 * CF-10: a request through the edge proxy must have the fingerprint of the server. Only the agent
 * on the panel server ("local") can call the API directly, and only from a loopback address.
 * Thus a leaked agent token alone is not sufficient.
 */
class AuthenticateAgent
{
    public const FINGERPRINT_HEADER = 'X-Client-Cert-Sha256';

    public const EDGE_HEADER = 'X-Cloudfactory-Edge';

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
        } elseif (! $server->local || ! self::fromLoopback($request)) {
            return response()->json(['message' => 'This agent must connect through the edge proxy with its client certificate.'], 401);
        }

        $server->forceFill(['last_seen_at' => now()])->save();
        $request->attributes->set('server', $server);

        return $next($request);
    }

    public static function viaEdge(Request $request): bool
    {
        return $request->hasHeader(self::EDGE_HEADER);
    }

    /** True when the request comes from an address in cloudfactory.agent.direct_addresses. */
    public static function fromLoopback(Request $request): bool
    {
        return in_array($request->server('REMOTE_ADDR'), config('cloudfactory.agent.direct_addresses'), true);
    }
}
