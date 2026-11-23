<?php

namespace App\Http\Controllers\Api;

use App\Enums\AgentAction;
use App\Enums\WebsiteStatus;
use App\Http\Controllers\Controller;
use App\Http\Resources\JobResource;
use App\Models\TerminalSession;
use App\Models\Website;
use App\Services\Audit;
use App\Services\JobDispatcher;
use App\Services\TerminalTickets;
use Illuminate\Http\JsonResponse;
use Illuminate\Http\Request;
use Illuminate\Http\Response;
use Illuminate\Support\Facades\DB;

/**
 * The web terminal of a website (FR-802).
 *
 * The browser and the agent of the server connect to the terminal relay of the panel with a
 * WebSocket and a ticket. The shell runs in the container of the website, as the Unix user of
 * the website, in the gVisor sandbox. The agent adds each command of the session to the command
 * log of the website, and at the end it sends the recording of the output.
 */
class TerminalSessionController extends Controller
{
    use Concerns;

    public function __construct(private JobDispatcher $jobs) {}

    public function index(Request $request, string $websiteId): JsonResponse
    {
        $website = $this->findWebsite($request, $websiteId);
        $page = $website->terminalSessions()->withoutRecording()->latest('created_at')->latest('id')->paginate(self::perPage($request, 25, 100));
        $items = collect($page->items());
        $hidden = Audit::hiddenActors($items, $request->user()->visibleCustomerIds());

        return response()->json([
            'data' => $items->map(fn (TerminalSession $s) => self::resource($s, $hidden[$s->id] ?? null))->all(),
            'meta' => ['currentPage' => $page->currentPage(), 'lastPage' => $page->lastPage(), 'total' => $page->total()],
        ]);
    }

    public function show(Request $request, string $websiteId, string $sessionId): JsonResponse
    {
        $website = $this->findWebsite($request, $websiteId);
        $session = $website->terminalSessions()->withoutRecording()->findOrFail($sessionId);
        $hidden = Audit::hiddenActors(collect([$session]), $request->user()->visibleCustomerIds());

        return response()->json(['session' => self::resource($session, $hidden[$session->id] ?? null)]);
    }

    /** The recording of the output of a session, in the asciicast v2 format. */
    public function recording(Request $request, string $websiteId, string $sessionId): Response
    {
        $website = $this->findWebsite($request, $websiteId);
        $session = $website->terminalSessions()->findOrFail($sessionId);
        abort_if($session->recording === null, 404, 'This session has no recording.');

        return response($session->recording, 200, [
            'Content-Type' => 'application/x-asciicast',
            'Content-Disposition' => 'attachment; filename="terminal-'.$session->id.'.cast"',
        ]);
    }

    /**
     * Opens the terminal. The answer has the ticket of the browser. The browser connects at once
     * to wss://<panel domain><path>?ticket=<ticket>.
     */
    public function store(Request $request, string $websiteId): JsonResponse
    {
        $website = $this->findWebsite($request, $websiteId);
        $data = $request->validate([
            'cols' => ['required', 'integer', 'between:10,1000'],
            'rows' => ['required', 'integer', 'between:2,500'],
        ]);
        WebsiteController::refuseFailed($website);
        abort_if($website->customer?->isSuspended() ?? false, 409, 'This customer is suspended. Lift the suspension before you open a terminal.');
        abort_if($website->status !== WebsiteStatus::Running, 409, 'The website does not run. Start it before you open a terminal.');

        [$session, $job] = DB::transaction(function () use ($request, $website, $data) {
            // The lock on the website row makes two requests at the same time wait for each other.
            Website::whereKey($website->id)->lockForUpdate()->first();
            abort_if($website->terminalSessions()->active()->count() >= TerminalSession::OPEN_MAX, 409,
                TerminalSession::OPEN_MAX.' terminals of this website are open now. Close one first.');
            $session = $website->terminalSessions()->create([
                'customer_id' => $website->customer_id,
                'user_id' => $request->user()?->id,
                'actor' => Audit::actor($request),
                'ip' => $request->ip(),
                'state' => TerminalSession::OPENING,
                'cols' => $data['cols'],
                'rows' => $data['rows'],
            ]);
            // The address of the relay and the ticket of the agent are not in the job
            // (AgentClient). The panel adds them each time that it sends the job.
            $job = $this->jobs->dispatch($website->server, AgentAction::OpenTerminal, [
                'websiteId' => $website->id,
                'sessionId' => $session->id,
                'cols' => $data['cols'],
                'rows' => $data['rows'],
            ], 'Open the terminal', $website);
            $session->update(['agent_job_id' => $job->id]);

            return [$session, $job];
        });
        Audit::record($request, 'Opened terminal', $website->name, $website->customer_id);

        return response()->json([
            'session' => self::resource($session),
            'job' => new JobResource($job),
            'path' => TerminalTickets::browserPath($session),
            'ticket' => TerminalTickets::browser($session),
        ], 202);
    }

    /**
     * A session of the terminal. $hiddenActor replaces the name and the address of a person that
     * the user must not see (audit finding S9).
     *
     * @return array<string, mixed>
     */
    public static function resource(TerminalSession $s, ?string $hiddenActor = null): array
    {
        return [
            'id' => $s->id,
            'websiteId' => $s->website_id,
            // "opening", "open", "ended" or "failed".
            'state' => $s->visibleState(),
            'actor' => $hiddenActor ?? $s->actor,
            'ip' => $hiddenActor === null ? $s->ip : null,
            'jobId' => $s->agent_job_id,
            'endReason' => $s->end_reason,
            'exitCode' => $s->exit_code,
            'commandCount' => $s->command_count,
            'hasRecording' => $s->hasRecording(),
            'recordingCut' => $s->recording_cut,
            'createdAt' => $s->created_at?->toIso8601String(),
            'startedAt' => $s->started_at?->toIso8601String(),
            'endedAt' => $s->ended_at?->toIso8601String(),
        ];
    }
}
