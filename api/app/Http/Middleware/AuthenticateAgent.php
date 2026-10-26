<?php

namespace App\Http\Middleware;

use App\Models\Server;
use Closure;
use Illuminate\Http\Request;
use Symfony\Component\HttpFoundation\Response;

/**
 * Authenticates an agent that reports to the panel. The agent sends the token that it got when it
 * joined. The edge proxy also checks the client certificate of the agent and sends its SHA-256
 * fingerprint in a header. When the server has a fingerprint, the two must agree.
 */
class AuthenticateAgent
{
    public const FINGERPRINT_HEADER = 'X-Client-Cert-Sha256';

    public function handle(Request $request, Closure $next): Response
    {
        $token = $request->bearerToken();
        $server = $token ? Server::where('agent_token_hash', hash('sha256', $token))->first() : null;
        if (! $server) {
            return response()->json(['message' => 'Unknown agent.'], 401);
        }

        $fingerprint = $request->header(self::FINGERPRINT_HEADER);
        if ($server->certificate_fingerprint && ! hash_equals($server->certificate_fingerprint, (string) $fingerprint)) {
            return response()->json(['message' => 'The client certificate does not agree with this agent.'], 401);
        }

        $server->forceFill(['last_seen_at' => now()])->save();
        $request->attributes->set('server', $server);

        return $next($request);
    }
}
