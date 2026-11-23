<?php

namespace App\Services\Deploys;

use App\Enums\AgentAction;
use App\Enums\WebsiteStatus;
use App\Http\Controllers\Api\DeployController;
use App\Models\AgentJob;
use App\Models\GitSource;
use App\Models\Release;
use App\Models\Website;
use App\Services\Audit;
use App\Services\JobDispatcher;

/** The rules of the deploys that the panel starts by itself: from a webhook, a check or a queue. */
final class Deploys
{
    /** True when a deploy or a change of the release of the website is in progress. */
    public static function busy(Website $website): bool
    {
        return $website->releases()->where('state', Release::BUILDING)->exists()
            || AgentJob::where('website_id', $website->id)->where('action', AgentAction::ActivateRelease)->whereNull('finished_at')->exists();
    }

    /**
     * True when the website can deploy now: it operates, it does not move to a different
     * server, and its customer is not suspended. A website that moves deploys when the move is
     * complete.
     */
    public static function allowed(?Website $website): bool
    {
        return $website !== null
            && ! in_array($website->status, [WebsiteStatus::Failed, WebsiteStatus::Moving], true)
            && ! ($website->customer?->isSuspended() ?? false);
    }

    /** The ID of the commit of the last release that has one, or null. */
    public static function lastCommit(Website $website): ?string
    {
        return $website->releases()->whereNotNull('commit')->value('commit');
    }

    /**
     * A deploy or a change of the release is complete. When a push came during it, the panel
     * deploys the branch now, unless the deploy that is complete got the commit of the push.
     */
    public static function jobFinished(AgentJob $job, JobDispatcher $jobs): ?Release
    {
        if (! $job->website_id || ! in_array($job->action, [AgentAction::Deploy, AgentAction::ActivateRelease], true)) {
            return null;
        }
        $source = GitSource::where('website_id', $job->website_id)->where('deploy_queued', true)->first();
        if (! $source) {
            return null;
        }
        $commit = $source->queued_commit;
        $source->update(['deploy_queued' => false, 'queued_commit' => null]);
        $website = $source->website()->with(['image', 'domains', 'server', 'customer'])->first();
        if ($source->trigger !== GitSource::WEBHOOK || ! self::allowed($website) || self::busy($website)) {
            return null;
        }
        if ($commit !== null && $job->action === AgentAction::Deploy && self::lastCommit($website) === $commit) {
            return null;
        }
        [$release] = DeployController::deploy($jobs, $website, $source, GitSource::WEBHOOK, commit: $commit);
        Audit::system('Started deploy from webhook', $website->name, $website->customer_id);

        return $release;
    }
}
