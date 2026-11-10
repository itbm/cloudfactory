<?php

namespace App\Http\Controllers\Agent;

use App\Http\Controllers\Controller;
use App\Http\Middleware\AuthenticateAgent;
use App\Models\JoinToken;
use App\Models\Server;
use Illuminate\Http\JsonResponse;
use Illuminate\Http\Request;
use Illuminate\Support\Facades\DB;
use Illuminate\Support\Str;
use Illuminate\Validation\Rule;

/**
 * FR-1201: a new server joins the panel with one command. The install script runs
 * "cf-agent join", and the agent sends its facts and the SHA-256 fingerprint of its certificate.
 */
class JoinController extends Controller
{
    public function __invoke(Request $request): JsonResponse
    {
        $data = $request->validate([
            'token' => ['required', 'string', 'max:200'],
            'name' => ['required', 'regex:/^[a-z][a-z0-9-]{1,30}$/', 'unique:servers,name'],
            'agentUrl' => ['required', 'url:https', 'max:255'],
            'certificateSha256' => ['required', 'regex:/^[0-9a-f]{64}$/'],
            'certificatePem' => ['required', 'string', 'max:8000'],
            'agentVersion' => ['required', 'string', 'max:32'],
            'os' => ['required', Rule::in(['debian-13', 'ubuntu-26.04'])],
            'osName' => ['nullable', 'string', 'max:100'],
            'kernel' => ['nullable', 'string', 'max:100'],
            'arch' => ['required', Rule::in(['x86-64', 'arm64'])],
            'bareMetal' => ['boolean'],
            'podmanVersion' => ['nullable', 'string', 'max:32'],
            'gvisorVersion' => ['nullable', 'string', 'max:64'],
            'roles' => ['nullable', 'array'],
            'roles.*' => [Rule::in(['web', 'database', 'email', 'dns', 'backup'])],
        ]);

        // The fingerprint must be the fingerprint of the certificate that the agent sent.
        if (self::fingerprint($data['certificatePem']) !== $data['certificateSha256']) {
            return response()->json(['message' => 'The certificate does not agree with its fingerprint.'], 422);
        }

        // CF-10: only the agent on the panel server joins directly, not through the edge proxy.
        // It uses the HTTPS port of the API, and its TLS connection has the certificate of the
        // request. Thus the agent has the private key of the certificate that the panel keeps.
        $direct = AuthenticateAgent::directFingerprint($request);
        $local = ! AuthenticateAgent::viaEdge($request)
            && (($direct !== '' && hash_equals($data['certificateSha256'], $direct)) || AuthenticateAgent::fromDevelopmentAddress($request));

        return DB::transaction(function () use ($data, $local) {
            $join = JoinToken::where('token_hash', hash('sha256', $data['token']))
                ->whereNull('used_at')->where('expires_at', '>', now())->lockForUpdate()->first();
            if (! $join) {
                return response()->json(['message' => 'The join token is not valid or has expired.'], 401);
            }
            $join->update(['used_at' => now()]);

            $agentToken = 'cfa_'.Str::random(48);
            $server = Server::create([
                'name' => $data['name'],
                'roles' => $data['roles'] ?? ['web'],
                'state' => 'healthy',
                'agent_url' => $data['agentUrl'],
                'agent_version' => $data['agentVersion'],
                'certificate_fingerprint' => $data['certificateSha256'],
                'certificate_pem' => $data['certificatePem'],
                'agent_token_hash' => hash('sha256', $agentToken),
                'os' => $data['osName'] ?? $data['os'],
                'kernel' => $data['kernel'] ?? null,
                'arch' => $data['arch'],
                'bare_metal' => $data['bareMetal'] ?? false,
                'podman_version' => $data['podmanVersion'] ?? null,
                'gvisor_version' => $data['gvisorVersion'] ?? null,
                'last_seen_at' => now(),
                'local' => $local,
            ]);

            return response()->json(['serverId' => $server->id, 'agentToken' => $agentToken, 'panelCertificateSha256' => self::panelFingerprint()], 201);
        });
    }

    /** The agent accepts calls only from a client with this certificate (FR-1706). */
    private static function panelFingerprint(): ?string
    {
        $path = config('cloudfactory.agent.client_certificate');

        return is_readable($path) ? self::fingerprint(file_get_contents($path)) : null;
    }

    /** The SHA-256 fingerprint of the first certificate in a PEM text. */
    public static function fingerprint(string $pem): ?string
    {
        if (! preg_match('/-----BEGIN CERTIFICATE-----(.+?)-----END CERTIFICATE-----/s', $pem, $m)) {
            return null;
        }
        $der = base64_decode(preg_replace('/\s/', '', $m[1]), true);

        return $der === false ? null : hash('sha256', $der);
    }
}
