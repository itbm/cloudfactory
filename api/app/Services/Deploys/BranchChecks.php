<?php

namespace App\Services\Deploys;

use App\Http\Controllers\Api\DeployController;
use App\Models\GitSource;
use App\Models\Release;
use App\Models\Server;
use App\Services\AgentCallException;
use App\Services\AgentClient;
use App\Services\Audit;
use App\Services\JobDispatcher;
use Illuminate\Support\Collection;
use Illuminate\Support\Str;
use Symfony\Component\HttpKernel\Exception\HttpException;

/**
 * FR-1004: the deploys on a schedule. For each Git source with the trigger "schedule", the
 * panel asks the agent for the last commit of the branch each "check_minutes" minutes (the
 * default is 5). When the commit is not the commit of the last release, the panel deploys it.
 *
 * The panel deploys each commit one time. If the deploy of a commit fails, the next check does
 * not deploy the same commit again: push a new commit, or start a deploy by hand.
 */
final class BranchChecks
{
    /** The checks in one request to an agent (agent: actions.MaxBranchChecks). */
    public const PER_REQUEST = 20;

    /** The time limit of one request to an agent, in seconds. */
    public const TIMEOUT = 180;

    /**
     * The scheduler runs the command each minute. A check that is due in less than this
     * number of seconds runs now, so that an interval of 5 minutes does not become 6.
     */
    private const EARLY_SECONDS = 30;

    public function __construct(private AgentClient $agent, private JobDispatcher $jobs) {}

    /**
     * The Git sources whose check is due now, on a server that operates. A website that
     * cannot deploy, or that deploys now, has no check.
     *
     * @return Collection<int, GitSource>
     */
    public static function due(): Collection
    {
        return GitSource::with(['website.server', 'website.customer', 'website.image', 'website.domains'])
            ->where('trigger', GitSource::SCHEDULE)
            ->orderBy('checked_at')
            ->get()
            ->filter(fn (GitSource $s) => $s->checked_at === null
                || $s->checked_at->lte(now()->subMinutes(max(1, $s->check_minutes))->addSeconds(self::EARLY_SECONDS)))
            ->filter(fn (GitSource $s) => Deploys::allowed($s->website)
                && in_array($s->website->server?->state, Server::WORKING, true)
                && ! Deploys::busy($s->website))
            ->values();
    }

    /**
     * Checks all sources that are due. One request to each server, with at most PER_REQUEST
     * checks. Gives the number of checks and the number of deploys that started.
     *
     * @return array{checks: int, deploys: int}
     */
    public function run(): array
    {
        $checks = 0;
        $deploys = 0;
        foreach (self::due()->groupBy(fn (GitSource $s) => $s->website->server_id) as $sources) {
            $server = $sources->first()->website->server;
            foreach ($sources->chunk(self::PER_REQUEST) as $chunk) {
                $checks += $chunk->count();
                $deploys += count(array_filter($this->check($server, $chunk->values())));
            }
        }

        return ['checks' => $checks, 'deploys' => $deploys];
    }

    /**
     * Checks the sources of websites on one server.
     *
     * @param  Collection<int, GitSource>  $sources
     * @return list<Release|null> the release of each deploy that started
     */
    public function check(Server $server, Collection $sources): array
    {
        $request = ['checks' => $sources->map(fn (GitSource $s) => [
            'websiteId' => $s->website_id,
            'repository' => $s->repository,
            'branch' => $s->branch,
            'deployKey' => AgentClient::deployKeyFor($s),
        ])->all()];
        try {
            $answer = $this->agent->call($server, 'CheckBranches', $request, self::TIMEOUT);
        } catch (AgentCallException $e) {
            foreach ($sources as $source) {
                $source->update(['checked_at' => now(), 'check_error' => $e->getMessage() ?: 'The server did not answer.']);
            }

            return [];
        }
        $heads = collect(is_array($answer['heads'] ?? null) ? $answer['heads'] : [])
            ->filter(fn ($h) => is_array($h) && is_string($h['websiteId'] ?? null))
            ->keyBy('websiteId');
        $started = [];
        foreach ($sources as $source) {
            $head = $heads->get($source->website_id, []);
            $started[] = $this->record($source, is_string($head['commit'] ?? null) ? $head['commit'] : null, is_string($head['error'] ?? null) ? $head['error'] : null);
        }

        return $started;
    }

    /**
     * Records the result of one check, and deploys a new commit. Gives the release of the
     * deploy, or null.
     */
    public function record(GitSource $source, ?string $commit, ?string $error): ?Release
    {
        if ($commit === null || ! preg_match(Webhook::COMMIT, $commit)) {
            $source->update(['checked_at' => now(), 'check_error' => Str::limit($error ?: 'The server gave no commit for the branch.', 500)]);

            return null;
        }
        $source->update(['checked_at' => now(), 'check_error' => null]);
        $website = $source->website;
        if ($commit === Deploys::lastCommit($website) || ! Deploys::allowed($website) || Deploys::busy($website)) {
            return null;
        }
        try {
            [$release] = DeployController::deploy($this->jobs, $website, $source, GitSource::SCHEDULE, commit: $commit);
        } catch (HttpException) {
            // A user started a deploy at the same time.
            return null;
        }
        Audit::system('Started deploy from branch check', "{$website->name}: ".substr($commit, 0, 7), $website->customer_id);

        return $release;
    }
}
