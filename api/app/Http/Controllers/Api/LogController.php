<?php

namespace App\Http\Controllers\Api;

use App\Http\Controllers\Controller;
use App\Services\AgentCallException;
use App\Services\AgentClient;
use Illuminate\Http\JsonResponse;
use Illuminate\Http\Request;
use Illuminate\Validation\Rule;

/**
 * The logs of a website: the last lines of the output of its container. The standard output
 * has the access log, and the standard error has the errors of the web server and of PHP.
 * The agent reads the log file as the Unix user of the website.
 */
class LogController extends Controller
{
    use Concerns;

    public function __construct(private AgentClient $agent) {}

    public function index(Request $request, string $websiteId): JsonResponse
    {
        $website = $this->findWebsite($request, $websiteId);
        WebsiteController::refuseFailed($website);
        $data = $request->validate([
            'lines' => ['sometimes', 'integer', 'between:1,2000'],
            'stream' => ['sometimes', Rule::in(['access', 'error'])],
        ]);
        try {
            $logs = $this->agent->call($website->server, 'GetLogs', ['websiteId' => $website->id, 'lines' => (int) ($data['lines'] ?? 200)]);
        } catch (AgentCallException $e) {
            abort($e->status(), $e->userMessage());
        }
        $lines = [];
        foreach ($logs['lines'] ?? [] as $line) {
            $stream = ($line['stream'] ?? '') === 'error' ? 'error' : 'access';
            if (isset($data['stream']) && $data['stream'] !== $stream) {
                continue;
            }
            $lines[] = ['at' => $line['at'] ?? null, 'stream' => $stream, 'text' => (string) ($line['text'] ?? '')];
        }

        return response()->json(['lines' => $lines]);
    }
}
