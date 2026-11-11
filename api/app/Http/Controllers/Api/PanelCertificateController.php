<?php

namespace App\Http\Controllers\Api;

use App\Enums\AgentAction;
use App\Enums\JobState;
use App\Http\Controllers\Controller;
use App\Http\Resources\JobResource;
use App\Models\AgentJob;
use App\Models\Server;
use App\Services\Audit;
use App\Services\JobDispatcher;
use App\Services\PanelCertificate;
use Illuminate\Http\JsonResponse;
use Illuminate\Http\Request;
use Illuminate\Validation\Rule;
use Illuminate\Validation\ValidationException;

/**
 * The TLS certificate of the panel domain: the CA of the panel or Let's Encrypt. Only an
 * administrator can see or change it.
 */
class PanelCertificateController extends Controller
{
    public function __construct(private JobDispatcher $jobs) {}

    public function show(): JsonResponse
    {
        return response()->json(self::present());
    }

    /**
     * The agent on the panel server changes the method. If the new certificate does not come
     * in time, the agent goes back to the previous method, and the job fails.
     */
    public function update(Request $request): JsonResponse
    {
        $method = $request->validate(['method' => ['required', Rule::in(PanelCertificate::METHODS)]])['method'];
        $domain = PanelCertificate::domain();
        if ($method === PanelCertificate::LETS_ENCRYPT && ! PanelCertificate::publicName($domain)) {
            throw ValidationException::withMessages(['method' => "Let's Encrypt gives certificates only for public domains. {$domain} is not a public domain."]);
        }
        $server = Server::where('local', true)->first();
        if (! $server) {
            throw ValidationException::withMessages(['method' => 'The panel server has not joined the panel.']);
        }
        if (PanelCertificate::current()['method'] === null) {
            throw ValidationException::withMessages(['method' => 'The agent on the panel server has not reported the panel certificate. Update the agent on the panel server, and wait one minute.']);
        }
        // The agents on the other servers must trust the panel CA before the panel uses it.
        // Else they cannot connect to the panel.
        if ($method === PanelCertificate::INTERNAL && ($missing = self::serversWithoutCa()) !== []) {
            throw ValidationException::withMessages(['method' => 'These servers do not trust the panel CA yet: '.implode(', ', $missing).'. Update their agent, and wait one minute.']);
        }
        if (self::busy()) {
            throw ValidationException::withMessages(['method' => 'A change of the panel certificate is in progress. Wait until it is complete.']);
        }
        $name = $method === PanelCertificate::LETS_ENCRYPT ? "Let's Encrypt" : 'the panel CA';
        $this->jobs->dispatch($server, AgentAction::SetPanelCertificate, ['method' => $method], "Change the panel certificate to {$name}", ref: $method);
        Audit::record($request, 'Changed the panel certificate method', $method);

        return response()->json(self::present(), 202);
    }

    /**
     * The names of the other servers that do not trust the panel CA.
     *
     * @return list<string>
     */
    private static function serversWithoutCa(): array
    {
        $pin = PanelCertificate::current()['caSha256'];

        return Server::where('local', false)
            ->where(fn ($q) => $q->whereNull('panel_ca_sha256')->when($pin, fn ($q) => $q->orWhere('panel_ca_sha256', '!=', $pin)))
            ->orderBy('name')->pluck('name')->all();
    }

    private static function busy(): ?AgentJob
    {
        return AgentJob::where('action', AgentAction::SetPanelCertificate->value)
            ->whereIn('state', [JobState::Queued->value, JobState::Running->value])->latest()->first();
    }

    /** @return array<string, mixed> */
    private static function present(): array
    {
        $current = PanelCertificate::current();
        $job = self::busy() ?? AgentJob::where('action', AgentAction::SetPanelCertificate->value)->latest()->first();

        return $current + [
            'letsEncryptAvailable' => PanelCertificate::publicName($current['domain']),
            // The panel changes to its own CA only when this list is empty.
            'serversWithoutCa' => self::serversWithoutCa(),
            // The current change, or the last change.
            'job' => $job ? new JobResource($job) : null,
        ];
    }
}
