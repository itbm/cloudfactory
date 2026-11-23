<?php

namespace App\Http\Controllers\Api;

use App\Enums\AgentAction;
use App\Enums\WebsiteStatus;
use App\Http\Controllers\Controller;
use App\Http\Resources\JobResource;
use App\Models\Website;
use App\Models\WebsiteCommand;
use App\Services\Audit;
use App\Services\JobDispatcher;
use App\Validation\Input;
use Illuminate\Http\JsonResponse;
use Illuminate\Http\Request;
use Illuminate\Support\Facades\DB;
use Illuminate\Validation\Rule;

/**
 * Run a command, and the command log of a website (FR-802). The log also has the commands of
 * the web terminal (TerminalSessionController).
 *
 * The agent runs the command in the container of the website, as the Unix user of the website,
 * in the gVisor sandbox, the same as a cron job. The panel records each command before it sends
 * the job: who ran it, when, from which address, and then the exit code and the end of the
 * output.
 */
class WebsiteCommandController extends Controller
{
    use Concerns;

    /** The commands of one website that run at the same time. The agent has the same limit. */
    public const RUNNING_MAX = 2;

    /** The time limit of a command when the request gives none, and the longest limit. */
    public const TIMEOUT = 600;

    public const TIMEOUT_MAX = 3600;

    public function __construct(private JobDispatcher $jobs) {}

    public function index(Request $request, string $websiteId): JsonResponse
    {
        $website = $this->findWebsite($request, $websiteId);
        $data = $request->validate([
            'source' => ['nullable', Rule::in([WebsiteCommand::SOURCE_RUN, WebsiteCommand::SOURCE_TERMINAL])],
            'sessionId' => ['nullable', 'string', 'size:26'],
            'q' => ['nullable', 'string', 'max:200'],
        ]);
        $page = $website->commands()
            ->when($data['source'] ?? null, fn ($q, $source) => $q->where('source', $source))
            ->when($data['sessionId'] ?? null, fn ($q, $id) => $q->where('terminal_session_id', $id))
            ->when($data['q'] ?? null, fn ($q, $s) => $q->whereLike('command', "%{$s}%"))
            ->latest('created_at')->latest('id')
            ->paginate(self::perPage($request, 50, 200));
        $items = collect($page->items());
        $hidden = Audit::hiddenActors($items, $request->user()->visibleCustomerIds());

        return response()->json([
            'data' => $items->map(fn (WebsiteCommand $c) => self::resource($c, $hidden[$c->id] ?? null))->all(),
            'meta' => ['currentPage' => $page->currentPage(), 'lastPage' => $page->lastPage(), 'total' => $page->total()],
        ]);
    }

    /** One entry of the command log, with the end of its output. */
    public function show(Request $request, string $websiteId, string $commandId): JsonResponse
    {
        $website = $this->findWebsite($request, $websiteId);
        $command = $website->commands()->findOrFail($commandId);
        $hidden = Audit::hiddenActors(collect([$command]), $request->user()->visibleCustomerIds());

        return response()->json(['command' => self::resource($command, $hidden[$command->id] ?? null, true)]);
    }

    /** Runs a command now. The entry of the command log gets the result when the job is complete. */
    public function store(Request $request, string $websiteId): JsonResponse
    {
        $website = $this->findWebsite($request, $websiteId);
        $data = $request->validate([
            'command' => ['required', 'string', Input::rule('commandLine')],
            'timeoutSeconds' => ['sometimes', 'integer', 'between:1,'.self::TIMEOUT_MAX],
        ]);
        WebsiteController::refuseFailed($website);
        abort_if($website->customer?->isSuspended() ?? false, 409, 'This customer is suspended. Lift the suspension before you run a command.');
        abort_if($website->status !== WebsiteStatus::Running, 409, 'The website does not run. Start it before you run a command.');
        $text = trim($data['command']);
        $timeout = (int) ($data['timeoutSeconds'] ?? self::TIMEOUT);

        [$command, $job] = DB::transaction(function () use ($request, $website, $text, $timeout) {
            // The lock on the website row makes two requests at the same time wait for each other.
            Website::whereKey($website->id)->lockForUpdate()->first();
            abort_if($website->commands()->where('state', WebsiteCommand::RUNNING)->count() >= self::RUNNING_MAX, 409,
                self::RUNNING_MAX.' commands of this website run now. Wait until one ends.');
            $command = $website->commands()->create([
                'customer_id' => $website->customer_id,
                'user_id' => $request->user()?->id,
                'actor' => Audit::actor($request),
                'ip' => $request->ip(),
                'source' => WebsiteCommand::SOURCE_RUN,
                'command' => $text,
                'state' => WebsiteCommand::RUNNING,
                'timeout_seconds' => $timeout,
            ]);
            $job = $this->jobs->dispatch($website->server, AgentAction::RunCommand, [
                'websiteId' => $website->id,
                'command' => $text,
                'timeoutSeconds' => $timeout,
            ], 'Run: '.mb_strimwidth($text, 0, 200, '…'), $website);
            $command->update(['agent_job_id' => $job->id]);

            return [$command, $job];
        });
        Audit::record($request, 'Ran command', "{$website->name}: {$text}", $website->customer_id);

        return response()->json(['command' => self::resource($command, null, true), 'job' => new JobResource($job)], 202);
    }

    /**
     * An entry of the command log. $hiddenActor replaces the name and the address of a person
     * that the user must not see (audit finding S9).
     *
     * @return array<string, mixed>
     */
    public static function resource(WebsiteCommand $c, ?string $hiddenActor = null, bool $withOutput = false): array
    {
        return [
            'id' => $c->id,
            'websiteId' => $c->website_id,
            // "run" (Run a command) or "terminal" (a command in the web terminal, with no exit
            // code and no output: the recording of the session has the output).
            'source' => $c->source,
            'sessionId' => $c->terminal_session_id,
            'command' => $c->command,
            // "running", "done" (the command ran and has an exit code) or "failed" (it did not start).
            'state' => $c->state,
            'exitCode' => $c->exit_code,
            'timedOut' => $c->timed_out,
            'timeoutSeconds' => $c->timeout_seconds,
            'actor' => $hiddenActor ?? $c->actor,
            'ip' => $hiddenActor === null ? $c->ip : null,
            'jobId' => $c->agent_job_id,
            'createdAt' => $c->created_at?->toIso8601String(),
            'startedAt' => $c->started_at?->toIso8601String(),
            'finishedAt' => $c->finished_at?->toIso8601String(),
            ...($withOutput ? ['output' => $c->output] : []),
        ];
    }
}
