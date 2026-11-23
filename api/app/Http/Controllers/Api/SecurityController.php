<?php

namespace App\Http\Controllers\Api;

use App\Enums\AgentAction;
use App\Http\Controllers\Controller;
use App\Http\Resources\JobResource;
use App\Models\Website;
use App\Services\AgentCallException;
use App\Services\AgentClient;
use App\Services\Audit;
use App\Services\JobDispatcher;
use App\Services\WebsiteSecurity;
use Illuminate\Http\JsonResponse;
use Illuminate\Http\Request;

/**
 * The protection of a website in the edge proxy: the firewall, the IP address rules, the
 * password-protected paths and the sign-in limit (App\Services\WebsiteSecurity). The panel
 * saves a change at once, and the agent applies it to the edge route. The container of the
 * website does not restart.
 */
class SecurityController extends Controller
{
    use Concerns;

    public function __construct(private JobDispatcher $jobs, private AgentClient $agent) {}

    public function show(Request $request, string $websiteId): JsonResponse
    {
        $website = $this->findWebsite($request, $websiteId);

        return response()->json(['security' => WebsiteSecurity::forApi($website)]);
    }

    public function update(Request $request, string $websiteId): JsonResponse
    {
        $website = $this->findWebsite($request, $websiteId);
        WebsiteController::refuseFailed($website);
        abort_if($website->customer?->isSuspended() ?? false, 409, 'This customer is suspended. Lift the suspension first.');
        abort_unless($website->isPublic(), 409, 'Only a website with domains has protection in the edge proxy. A '.Website::KIND_NAMES[$website->kind].' gets no requests from the internet.');
        $data = $request->validate(WebsiteSecurity::rules());
        $before = WebsiteSecurity::of($website);
        $after = WebsiteSecurity::change($website, $data);
        if ($after === $before) {
            return response()->json(['security' => WebsiteSecurity::forApi($website), 'job' => null]);
        }
        $website->update(['security' => $after]);
        Audit::record($request, 'Changed website protection', $website->name.': '.self::summary($before, $after), $website->customer_id);
        // The panel adds the current value each time that it sends the job (AgentClient).
        $job = $this->jobs->dispatch($website->server, AgentAction::SetWebsiteSecurity, [
            'websiteId' => $website->id,
            'unixUser' => $website->unix_user,
        ], 'Set website protection', $website);

        return response()->json(['security' => WebsiteSecurity::forApi($website->refresh()), 'job' => new JobResource($job)], 202);
    }

    /** The last requests that the firewall of the edge proxy blocked or detected. */
    public function events(Request $request, string $websiteId): JsonResponse
    {
        $website = $this->findWebsite($request, $websiteId);
        WebsiteController::refuseFailed($website);
        $data = $request->validate(['limit' => ['sometimes', 'integer', 'between:1,200']]);
        try {
            $answer = $this->agent->call($website->server, 'GetFirewallEvents', ['websiteId' => $website->id, 'limit' => (int) ($data['limit'] ?? 50)]);
        } catch (AgentCallException $e) {
            abort($e->status(), $e->userMessage());
        }
        $events = [];
        foreach ($answer['events'] ?? [] as $e) {
            $events[] = [
                'at' => $e['at'] ?? null,
                'clientIp' => (string) ($e['clientIp'] ?? ''),
                'host' => ($e['host'] ?? '') !== '' ? $e['host'] : null,
                'uri' => (string) ($e['uri'] ?? ''),
                'action' => ($e['action'] ?? '') === 'blocked' ? 'blocked' : 'detected',
                'score' => (int) ($e['score'] ?? 0),
                'rules' => array_map(fn (array $r) => [
                    'id' => (int) ($r['id'] ?? 0),
                    'message' => (string) ($r['message'] ?? ''),
                    'data' => (string) ($r['data'] ?? ''),
                ], $e['rules'] ?? []),
            ];
        }

        return response()->json(['events' => $events]);
    }

    /**
     * The parts of the protection that changed, for the audit log. The log does not have the
     * passwords or the addresses.
     *
     * @param  array<string, mixed>  $before
     * @param  array<string, mixed>  $after
     */
    private static function summary(array $before, array $after): string
    {
        $parts = [];
        if ($before['firewall'] !== $after['firewall']) {
            $parts[] = "firewall {$after['firewall']}";
        } elseif ($before['firewallExclusions'] !== $after['firewallExclusions'] || $before['firewallSkipPaths'] !== $after['firewallSkipPaths']) {
            $parts[] = 'firewall exceptions';
        }
        if ($before['allow'] !== $after['allow'] || $before['block'] !== $after['block']) {
            $parts[] = sprintf('IP rules (%d allowed, %d blocked)', count($after['allow']), count($after['block']));
        }
        if ($before['protectedPaths'] !== $after['protectedPaths']) {
            $parts[] = 'protected paths ('.(implode(', ', array_column($after['protectedPaths'], 'path')) ?: 'none').')';
        }
        if ($before['signInLimit'] !== $after['signInLimit'] || $before['signInPaths'] !== $after['signInPaths']) {
            $parts[] = $after['signInLimit'] > 0 ? "sign-in limit {$after['signInLimit']} a minute" : 'no sign-in limit';
        }

        return implode('; ', $parts);
    }
}
