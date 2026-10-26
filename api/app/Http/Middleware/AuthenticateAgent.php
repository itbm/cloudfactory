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
 * A request through the edge proxy must have the fingerprint of the server. A request that does not
 * come through the edge proxy can only come from the panel host itself, because the API port listens
 * only on 127.0.0.1. The agent on the panel server uses this path.
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

        $fingerprint = (string) $request->header(self::FINGERPRINT_HEADER);
        $viaEdge = $request->hasHeader(self::EDGE_HEADER);
        if ($viaEdge && ! hash_equals((string) $server->certificate_fingerprint, strtolower($fingerprint))) {
            return response()->json(['message' => 'The client certificate does not agree with this agent.'], 401);
        }

        $server->forceFill(['last_seen_at' => now()])->save();
        $request->attributes->set('server', $server);

        return $next($request);
    }
}
