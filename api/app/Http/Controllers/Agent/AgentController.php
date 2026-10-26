<?php

namespace App\Http\Controllers\Agent;

use App\Enums\JobState;
use App\Http\Controllers\Controller;
use App\Models\AgentJob;
use App\Models\Server;
use App\Services\JobOutcome;
use Illuminate\Http\JsonResponse;
use Illuminate\Http\Request;
use Illuminate\Validation\Rule;

/** Reports from the agents: job progress, results and the actual state of the server. */
class AgentController extends Controller
{
    public function __construct(private JobOutcome $outcome) {}

    public function jobEvent(Request $request, string $id): JsonResponse
    {
        /** @var Server $server */
        $server = $request->attributes->get('server');
        $job = AgentJob::where('server_id', $server->id)->findOrFail($id);
        $data = $request->validate([
            'state' => ['required', Rule::in(['running', 'succeeded', 'failed'])],
            'step' => ['nullable', 'integer', 'min:0'],
            'error' => ['nullable', 'string', 'max:4000'],
            'result' => ['nullable', 'array'],
        ]);

        if ($data['state'] === 'running') {
            $this->outcome->progress($job, $data['step'] ?? $job->step);
        } else {
            $this->outcome->finish($job, JobState::from($data['state']), $data['error'] ?? null, $data['result'] ?? []);
        }

        return response()->json(['ok' => true]);
    }

    /** FR-1118 and FR-1301: the agent reports the state of the host on a schedule. */
    public function report(Request $request): JsonResponse
    {
        /** @var Server $server */
        $server = $request->attributes->get('server');
        $data = $request->validate([
            'agentVersion' => ['required', 'string', 'max:32'],
            'kernel' => ['nullable', 'string', 'max:100'],
            'podmanVersion' => ['nullable', 'string', 'max:32'],
            'gvisorVersion' => ['nullable', 'string', 'max:64'],
            'pendingUpdates' => ['array'],
            'pendingUpdates.*.name' => ['required', 'string'],
            'pendingUpdates.*.from' => ['nullable', 'string'],
            'pendingUpdates.*.to' => ['required', 'string'],
            'pendingUpdates.*.security' => ['boolean'],
            'rebootRequired' => ['boolean'],
            'lastUpdateAt' => ['nullable', 'date'],
            'services' => ['array'],
            'services.*.name' => ['required', 'string'],
            'services.*.state' => ['required', Rule::in(['running', 'stopped', 'failed'])],
            'usage' => ['array'],
            'websites' => ['array'],
            'websites.*.id' => ['required', 'string'],
            'websites.*.cpuPercent' => ['numeric'],
            'websites.*.memoryBytes' => ['integer'],
            'websites.*.diskBytes' => ['integer'],
            'websites.*.processes' => ['integer'],
        ]);

        $failed = collect($data['services'] ?? [])->contains(fn ($s) => $s['state'] !== 'running');
        $server->update([
            'agent_version' => $data['agentVersion'],
            'kernel' => $data['kernel'] ?? $server->kernel,
            'podman_version' => $data['podmanVersion'] ?? $server->podman_version,
            'gvisor_version' => $data['gvisorVersion'] ?? $server->gvisor_version,
            'pending_updates' => $data['pendingUpdates'] ?? [],
            'reboot_required' => $data['rebootRequired'] ?? false,
            'last_update_at' => $data['lastUpdateAt'] ?? $server->last_update_at,
            'services' => $data['services'] ?? [],
            'usage' => $data['usage'] ?? null,
            'state' => in_array($server->state, ['rebooting', 'joining'], true) ? $server->state : ($failed ? 'degraded' : 'healthy'),
        ]);
        // FR-203: the current resource use of each website.
        foreach ($data['websites'] ?? [] as $use) {
            $server->websites()->whereKey($use['id'])->update(['usage' => json_encode(collect($use)->except('id'))]);
        }

        return response()->json(['ok' => true]);
    }
}
