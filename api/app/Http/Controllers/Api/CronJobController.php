<?php

namespace App\Http\Controllers\Api;

use App\Enums\AgentAction;
use App\Enums\WebsiteStatus;
use App\Http\Controllers\Controller;
use App\Http\Resources\JobResource;
use App\Models\AgentJob;
use App\Models\CronJob;
use App\Models\Website;
use App\Services\Audit;
use App\Services\JobDispatcher;
use App\Validation\Input;
use App\Validation\InputRules;
use Illuminate\Http\JsonResponse;
use Illuminate\Http\Request;
use Illuminate\Validation\ValidationException;

/**
 * The cron jobs of a website. The agent runs each command in the container of the website, as
 * the Unix user of the website, at the times of the schedule. The time of a schedule is UTC.
 *
 * After each change, the panel sends all cron jobs of the website to the agent (SetCronJobs).
 */
class CronJobController extends Controller
{
    use Concerns;

    public function __construct(private JobDispatcher $jobs) {}

    public function index(Request $request, string $websiteId): JsonResponse
    {
        $website = $this->findWebsite($request, $websiteId);

        return response()->json(['data' => $website->cronJobs->map(fn (CronJob $c) => self::resource($c))->all()]);
    }

    public function store(Request $request, string $websiteId): JsonResponse
    {
        $website = $this->changeable($request, $websiteId);
        $data = $request->validate([
            'schedule' => ['required', 'string', Input::rule('cronSchedule')],
            'command' => ['required', 'string', Input::rule('commandLine')],
            'enabled' => ['sometimes', 'boolean'],
        ]);
        if ($website->cronJobs()->count() >= InputRules::MAX_CRON_JOBS) {
            throw ValidationException::withMessages(['schedule' => 'A website can have '.InputRules::MAX_CRON_JOBS.' cron jobs. Delete one first.']);
        }
        $cron = $website->cronJobs()->create(['schedule' => self::schedule($data['schedule']), 'command' => trim($data['command']), 'enabled' => $data['enabled'] ?? true]);
        $job = $this->sync($website, 'Add a cron job');
        Audit::record($request, 'Added cron job', "{$website->name}: {$cron->command}", $website->customer_id);

        return response()->json(['cronJob' => self::resource($cron), 'job' => new JobResource($job)], 202);
    }

    public function update(Request $request, string $websiteId, string $cronJobId): JsonResponse
    {
        $website = $this->changeable($request, $websiteId);
        $cron = $website->cronJobs()->findOrFail($cronJobId);
        $data = $request->validate([
            'schedule' => ['sometimes', 'string', Input::rule('cronSchedule')],
            'command' => ['sometimes', 'string', Input::rule('commandLine')],
            'enabled' => ['sometimes', 'boolean'],
        ]);
        $cron->update(array_filter([
            'schedule' => isset($data['schedule']) ? self::schedule($data['schedule']) : null,
            'command' => isset($data['command']) ? trim($data['command']) : null,
            'enabled' => $data['enabled'] ?? null,
        ], fn ($v) => $v !== null));
        $job = $this->sync($website, 'Change a cron job');
        Audit::record($request, 'Changed cron job', "{$website->name}: {$cron->command}", $website->customer_id);

        return response()->json(['cronJob' => self::resource($cron), 'job' => new JobResource($job)], 202);
    }

    public function destroy(Request $request, string $websiteId, string $cronJobId): JsonResponse
    {
        $website = $this->changeable($request, $websiteId);
        $cron = $website->cronJobs()->findOrFail($cronJobId);
        $cron->delete();
        $job = $this->sync($website, 'Delete a cron job');
        Audit::record($request, 'Deleted cron job', "{$website->name}: {$cron->command}", $website->customer_id);

        return response()->json(['job' => new JobResource($job)], 202);
    }

    /** Runs the command of a cron job now. The cron job gets the result when the job is complete. */
    public function run(Request $request, string $websiteId, string $cronJobId): JsonResponse
    {
        $website = $this->changeable($request, $websiteId);
        $cron = $website->cronJobs()->findOrFail($cronJobId);
        abort_if($website->status !== WebsiteStatus::Running, 409, 'The website does not run. Start it before you run a cron job.');
        $job = $this->jobs->dispatch($website->server, AgentAction::RunCronJob, [
            'websiteId' => $website->id,
            'cronJob' => self::payload($cron),
        ], "Run: {$cron->command}", $website);
        Audit::record($request, 'Ran cron job', "{$website->name}: {$cron->command}", $website->customer_id);

        return response()->json(['job' => new JobResource($job)], 202);
    }

    /** A website that a user can change the cron jobs of. */
    private function changeable(Request $request, string $websiteId): Website
    {
        $website = $this->findWebsite($request, $websiteId);
        WebsiteController::refuseFailed($website);
        abort_if($website->customer?->isSuspended() ?? false, 409, 'This customer is suspended. Lift the suspension before you change cron jobs.');

        return $website;
    }

    /** Sends all cron jobs of the website to the agent. */
    private function sync(Website $website, string $title): AgentJob
    {
        return $this->jobs->dispatch($website->server, AgentAction::SetCronJobs, [
            'websiteId' => $website->id,
            'cronJobs' => $website->cronJobs()->get()->map(fn (CronJob $c) => self::payload($c))->all(),
        ], $title, $website);
    }

    /** One space between the fields. */
    private static function schedule(string $schedule): string
    {
        $schedule = trim((string) preg_replace('/ +/', ' ', $schedule));
        // Adversarial review, finding A2-07: the agent refuses a range that goes down or that
        // is not in the limits of its field, and then refuses each later change of the cron
        // jobs of the website. The panel refuses such a schedule first.
        $limits = [[0, 59], [0, 23], [1, 31], [1, 12], [0, 7]];
        foreach (explode(' ', $schedule) as $i => $field) {
            [$min, $max] = $limits[$i] ?? [0, 0];
            foreach (explode(',', $field) as $part) {
                [$spec, $step] = array_pad(explode('/', $part, 2), 2, null);
                $from = $min;
                $to = $max;
                if ($spec !== '*') {
                    [$a, $b] = array_pad(explode('-', $spec, 2), 2, null);
                    $from = (int) $a;
                    $to = $b !== null ? (int) $b : ($step !== null ? $max : $from);
                }
                if (! isset($limits[$i]) || ($step !== null && (int) $step < 1) || $from < $min || $to > $max || $from > $to) {
                    throw ValidationException::withMessages(['schedule' => "The part \"{$part}\" of the schedule is not valid. Use values from {$min} to {$max}, and write a range from the small value to the large value."]);
                }
            }
        }

        return $schedule;
    }

    /** @return array<string, mixed> */
    /** @return array<string, mixed> The cron job for the agent. */
    public static function payload(CronJob $c): array
    {
        return ['id' => $c->id, 'schedule' => $c->schedule, 'command' => $c->command, 'enabled' => $c->enabled];
    }

    /** @return array<string, mixed> */
    public static function resource(CronJob $c): array
    {
        return [
            'id' => $c->id,
            'schedule' => $c->schedule,
            'command' => $c->command,
            'enabled' => $c->enabled,
            'lastRunAt' => $c->last_run_at?->toIso8601String(),
            // "ok" or "failed", or null before the first run.
            'lastResult' => $c->last_exit_code === null ? null : ($c->last_exit_code === 0 ? 'ok' : 'failed'),
            'lastExitCode' => $c->last_exit_code,
            'lastOutput' => $c->last_output,
        ];
    }
}
