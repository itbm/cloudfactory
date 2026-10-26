<?php

namespace App\Http\Controllers\Agent;

use App\Http\Controllers\Controller;
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
            'token' => ['required', 'string'],
            'name' => ['required', 'regex:/^[a-z][a-z0-9-]{1,30}$/', 'unique:servers,name'],
            'agentUrl' => ['required', 'url:https'],
            'certificateSha256' => ['required', 'regex:/^[0-9a-f]{64}$/'],
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

        return DB::transaction(function () use ($data) {
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
                'agent_token_hash' => hash('sha256', $agentToken),
                'os' => $data['osName'] ?? $data['os'],
                'kernel' => $data['kernel'] ?? null,
                'arch' => $data['arch'],
                'bare_metal' => $data['bareMetal'] ?? false,
                'podman_version' => $data['podmanVersion'] ?? null,
                'gvisor_version' => $data['gvisorVersion'] ?? null,
                'last_seen_at' => now(),
            ]);

            return response()->json(['serverId' => $server->id, 'agentToken' => $agentToken, 'panelCertificateSha256' => self::panelFingerprint()], 201);
        });
    }

    /** The agent accepts calls only from a client with this certificate (FR-1706). */
    private static function panelFingerprint(): ?string
    {
        $path = config('cloudfactory.agent.client_certificate');
        if (! is_readable($path)) {
            return null;
        }
        $der = base64_decode(preg_replace('/-----[^-]+-----|\s/', '', file_get_contents($path)));

        return hash('sha256', $der);
    }
}
