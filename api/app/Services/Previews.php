<?php

namespace App\Services;

use App\Enums\AgentAction;
use App\Enums\DomainKind;
use App\Enums\WebsiteStatus;
use App\Http\Controllers\Api\DeployController;
use App\Http\Controllers\Api\WebsiteController;
use App\Models\AgentJob;
use App\Models\Domain;
use App\Models\GitSource;
use App\Models\Preview;
use App\Models\Website;
use App\Services\Deploys\Deploys;
use App\Services\Deploys\PullRequest;
use App\Services\Dns\Zones;
use App\Services\Plugins\Hooks;
use App\Validation\Input;
use Illuminate\Http\Request;
use Illuminate\Support\Facades\DB;
use Illuminate\Support\Str;
use Illuminate\Validation\ValidationException;
use Symfony\Component\HttpKernel\Exception\HttpException;

/**
 * The preview websites of pull requests.
 *
 * A website that deploys from Git can make a preview for each pull request to its branch. A
 * preview is a website of its own, as a staging copy is: its own container, its own Unix user,
 * the plan of the customer and the server of the website. It gets the name
 * "pr-<number>.<preview domain>", and it deploys the head branch of the pull request.
 *
 * - A new pull request makes the preview website (CreateWebsite). When that job is complete,
 *   the panel starts the first deploy (jobFinished).
 * - New commits deploy the preview again. When a deploy is in progress, the panel deploys
 *   again after it.
 * - When the pull request closes, the panel removes the preview website (WebsiteRemoval).
 *
 * The panel makes no preview of a pull request from a different repository (a fork): its code
 * is not from the people who can push to the repository of the website.
 */
class Previews
{
    /** The first part of the name of a preview: "pr-<number>". */
    public const PREFIX = 'pr-';

    /** The largest number of previews of a website at the same time. */
    public const MAX_LIMIT = 10;

    /** The status of a request that made a preview or started a deploy. */
    public const STARTED = 'started';

    /** A deploy is in progress. The panel deploys again after it. */
    public const QUEUED = 'queued';

    /** The panel makes the preview website. Its first deploy gets the latest commit. */
    public const WAITING = 'waiting';

    /** The panel removes the preview website. */
    public const REMOVING = 'removing';

    /** The panel could not make the preview, or could not start the deploy. */
    public const FAILED = 'failed';

    public function __construct(private JobDispatcher $jobs, private DomainPolicy $domains, private Zones $zones) {}

    /** The name of the preview of a pull request. */
    public static function nameOf(string $domain, int $number): string
    {
        return self::PREFIX.$number.'.'.$domain;
    }

    /**
     * A pull request event from the webhook of a website.
     *
     * @return array{0: int, 1: array{status: string, message: string, previewWebsiteId?: string}} the HTTP status and the answer
     */
    public function hook(GitSource $source, PullRequest $event, Request $request): array
    {
        $website = $source->website()->with(['image', 'domains', 'server', 'customer'])->firstOrFail();
        $number = $event->number;
        if (! $source->previews) {
            return [200, ['status' => 'ignored', 'message' => 'The previews of this website are off.']];
        }
        // A pull request that closes removes its preview, also when the base branch changed.
        if ($event->action === PullRequest::CLOSED) {
            $preview = $website->previews()->where('number', $number)->first();
            if (! $preview) {
                return [200, ['status' => 'ignored', 'message' => "Pull request #{$number} has no preview."]];
            }
            $this->remove($preview, $request);

            return [202, ['status' => self::REMOVING, 'message' => "The panel removes the preview of pull request #{$number}."]];
        }
        if ($event->action === PullRequest::OTHER) {
            return [200, ['status' => 'ignored', 'message' => 'This action does not change a preview.']];
        }
        if ($event->base !== '' && $event->base !== $source->branch) {
            return [200, ['status' => 'ignored', 'message' => "The previews of this website are for pull requests to the branch {$source->branch}."]];
        }
        if ($event->fork) {
            return [200, ['status' => 'ignored', 'message' => 'The panel makes no preview of a pull request from a different repository, for example a fork.']];
        }
        if ($event->action === PullRequest::EDITED) {
            $changed = $website->previews()->where('number', $number)->update(['title' => $event->title] + ($event->url ? ['url' => $event->url] : []));

            return [200, $changed
                ? ['status' => 'updated', 'message' => "The panel changed the title of the preview of pull request #{$number}."]
                : ['status' => 'ignored', 'message' => "Pull request #{$number} has no preview."]];
        }
        if ($website->status === WebsiteStatus::Failed || ($website->customer?->isSuspended() ?? false)) {
            return [409, ['status' => 'refused', 'message' => 'The website cannot deploy now.']];
        }
        [$preview, $status, $message] = $this->open($source, $number, $event->branch, $event->title, $event->url, GitSource::WEBHOOK, $request);

        return [in_array($status, [self::STARTED, self::QUEUED, self::WAITING], true) ? 202 : 200, ['status' => $status, 'message' => $message]
            + ($preview->preview_website_id ? ['previewWebsiteId' => $preview->preview_website_id] : [])];
    }

    /**
     * Makes the preview of a pull request, or deploys it again. A preview that the panel could
     * not make keeps the cause in "error", and the next event tries again.
     *
     * @return array{0: Preview, 1: string, 2: string} the preview, the status and a message
     */
    public function open(GitSource $source, int $number, string $branch, string $title, ?string $url, string $trigger, Request $request): array
    {
        $website = $source->website()->with(['image', 'domains', 'server', 'customer'])->firstOrFail();
        $preview = $website->previews()->where('number', $number)->first() ?? new Preview(['website_id' => $website->id, 'number' => $number]);
        $preview->fill(['branch' => $branch, 'title' => $title, 'url' => $url ?? $preview->url]);
        $site = $preview->preview_website_id ? Website::with(['image', 'domains', 'server'])->find($preview->preview_website_id) : null;
        if ($site) {
            $preview->save();

            return [$preview, ...$this->deploy($source, $preview, $site, $trigger)];
        }
        try {
            $site = $this->make($source, $website, $preview, $trigger, $request);
        } catch (ValidationException $e) {
            $preview->fill(['preview_website_id' => null, 'deploy_again' => false, 'error' => self::firstError($e)])->save();

            return [$preview, self::FAILED, $preview->error];
        }

        return [$preview, self::STARTED, "The panel makes the preview {$site->name}."];
    }

    /**
     * Checks the domain of the previews of a website. The customer must control the names of
     * the previews, because the panel makes them with no user and verifies them at once.
     *
     * @throws ValidationException
     */
    public function checkDomain(Website $website, string $domain, string $field = 'domain'): void
    {
        // The longest name of a preview.
        $longest = self::nameOf($domain, PullRequest::MAX_NUMBER);
        if (! Input::valid('domain', $domain) || ! Input::valid('domain', $longest)) {
            throw ValidationException::withMessages([$field => 'Enter a domain name that is shorter, for example preview.example.com.']);
        }
        if ($this->domains->isReserved($longest) || str_ends_with($domain, '.invalid')) {
            throw ValidationException::withMessages([$field => 'This domain is reserved for the panel or a server.']);
        }
        if (! $this->domains->controls($website->customer_id, $longest)) {
            throw ValidationException::withMessages([$field => 'Use a sub-domain of a verified domain of this customer, for example '.self::suggestedDomain($website).'. A DNS zone of the customer in the panel also proves control.']);
        }
    }

    /** A domain for the previews of a website: "preview." and the name of the website with no "www.". */
    public static function suggestedDomain(Website $website): string
    {
        $apex = preg_replace('/^www\./', '', $website->name);
        $policy = app(DomainPolicy::class);

        return $apex !== $website->name && ! $policy->controls($website->customer_id, self::nameOf('preview.'.$apex, 1))
            ? 'preview.'.$website->name
            : 'preview.'.$apex;
    }

    /**
     * Removes the preview of a pull request. A preview with no preview website goes at once.
     * Gives the first job of the removal, or null.
     */
    public function remove(Preview $preview, Request $request): ?AgentJob
    {
        $site = $preview->previewWebsite;
        if (! $site) {
            $preview->delete();

            return null;
        }
        // The preview row goes with the preview website (JobOutcome::deleteWebsite).
        $open = self::openJob($site, AgentAction::DeleteWebsite);
        if ($open) {
            return $open;
        }
        $job = app(WebsiteRemoval::class)->start($site);
        Audit::record($request, 'Removed preview', $site->name, $site->customer_id);

        return $job;
    }

    /** Removes all previews of a website: when the previews go off, or the Git source or the website goes. */
    public function removeAll(Website $website, Request $request): void
    {
        foreach ($website->previews()->with('previewWebsite')->get() as $preview) {
            $this->remove($preview, $request);
        }
    }

    /**
     * A user cannot change the Git source of a preview: the panel copies it from the website of
     * the pull request before each deploy.
     *
     * @throws ValidationException
     */
    public static function refuseChange(Website $website): void
    {
        $of = $website->previewOf()->with('website:id,name')->first();
        if ($of) {
            throw ValidationException::withMessages(['repository' => "This website is the preview of pull request #{$of->number}. Change the deploy settings of {$of->website?->name}."]);
        }
    }

    /**
     * A job of a preview website is complete. The first deploy starts when the website is made,
     * and a deploy starts again when a push came during the previous one.
     */
    public static function jobFinished(AgentJob $job, bool $succeeded): void
    {
        if (! $job->website_id || ! in_array($job->action, [AgentAction::CreateWebsite, AgentAction::Deploy, AgentAction::ActivateRelease], true)) {
            return;
        }
        $preview = Preview::where('preview_website_id', $job->website_id)->first();
        if (! $preview) {
            return;
        }
        if ($job->action === AgentAction::CreateWebsite && ! $succeeded) {
            return;
        }
        if ($job->action !== AgentAction::CreateWebsite && ! $preview->deploy_again) {
            return;
        }
        $source = GitSource::where('website_id', $preview->website_id)->first();
        $site = Website::with(['image', 'domains', 'server', 'customer'])->find($job->website_id);
        if (! $source?->previews || ! $site || ($site->customer?->isSuspended() ?? false)) {
            return;
        }
        $trigger = $job->action === AgentAction::CreateWebsite ? (string) ($job->payload['previewTrigger'] ?? GitSource::WEBHOOK) : GitSource::WEBHOOK;
        app(self::class)->deploy($source, $preview, $site, $trigger);
    }

    /**
     * Makes the preview website and its Git source, and sends the job that makes it on the
     * server. The first deploy starts when the job is complete.
     *
     * @throws ValidationException
     */
    private function make(GitSource $source, Website $website, Preview $preview, string $trigger, Request $request): Website
    {
        $customer = $website->customer;
        $domain = (string) $source->preview_domain;
        $name = self::nameOf($domain, $preview->number);
        // The customer can lose control of the domain after the settings, for example when a
        // different customer proves control of it.
        $this->checkDomain($website, $domain, 'name');
        if (Website::where('name', $name)->exists() || Domain::where('name', $name)->exists()) {
            throw ValidationException::withMessages(['name' => "The name {$name} is in use."]);
        }
        $open = $website->previews()->whereNotNull('preview_website_id')->count();
        if ($open >= $source->preview_limit) {
            throw ValidationException::withMessages(['name' => "The limit of the previews of this website is {$source->preview_limit}. Close a pull request, or change the limit."]);
        }
        // A preview is a website of the plan. The plugins with the before hook can refuse it.
        app(Hooks::class)->check('website.creating', [
            'name' => $name, 'kind' => Website::PUBLIC, 'customerId' => $customer->id, 'customerName' => $customer->name,
            'planId' => $website->plan_id, 'imageId' => $website->image_id, 'serverId' => $website->server_id,
        ], $request->user());

        $site = DB::transaction(function () use ($source, $website, $preview, $customer, $name) {
            WebsiteController::checkWebsiteLimit($customer);
            // As for a staging copy, the preview gets the runtime, the limits and the server of
            // the website, but not its relations, counters, flags and SFTP access.
            $site = $website->replicate(['usage', 'managed_by_terraform', 'sftp_enabled', 'sftp_keys', 'bandwidth_month', 'bandwidth_bytes',
                'stopped_by_suspension', 'sftp_closed_by_suspension', 'previous_image_id', 'previous_image_tag', 'staging_of', 'environment'])->withoutRelations();
            $site->fill([
                'name' => $name,
                'application' => Str::limit("Preview of {$website->name}, pull request #{$preview->number}", 100, ''),
                'unix_user' => WebsiteController::unixUser($name),
                'status' => WebsiteStatus::Starting,
                'managed_by_terraform' => false,
                // The first deploy sets the document root of its release.
                'document_root' => $website->image->isApp() ? '.' : 'public',
                'environment' => $source->preview_environment ? $website->variables() : [],
            ])->save();
            // The customer controls the domain of the previews (checkDomain), so the name is
            // verified at once.
            $site->domains()->create(['name' => $name, 'kind' => DomainKind::Primary, 'verification_token' => 'cf-'.Str::lower(Str::random(40)), 'verified_at' => now()]);
            $preview->fill(['preview_website_id' => $site->id, 'deploy_again' => false, 'error' => null])->save();
            self::copySource($source, $preview, $site);

            return $site;
        });
        $this->jobs->dispatch($site->server, AgentAction::CreateWebsite, JobDispatcher::websitePayload($site) + ['previewTrigger' => $trigger],
            "Create preview of pull request #{$preview->number}", $site);
        // A DNS zone of the customer with the name in it gets the address records of the preview.
        $this->zones->websiteDomainAdded($site, $name);
        Audit::record($request, 'Made preview', $name, $customer->id);

        return $site;
    }

    /**
     * Deploys the head branch of the pull request to the preview website.
     *
     * @return array{0: string, 1: string} the status and a message
     */
    private function deploy(GitSource $source, Preview $preview, Website $site, string $trigger): array
    {
        if (self::openJob($site, AgentAction::DeleteWebsite)) {
            return [self::REMOVING, 'The panel removes this preview. Try again when the removal is complete.'];
        }
        if ($site->status === WebsiteStatus::Failed) {
            // The panel could not make the preview website. It tries again, and the first
            // deploy follows.
            self::copySource($source, $preview, $site);
            $preview->update(['error' => null, 'deploy_again' => false]);
            WebsiteController::createAgain($this->jobs, $site);

            return [self::STARTED, "The panel makes the preview {$site->name} again."];
        }
        if (self::openJob($site, AgentAction::CreateWebsite)) {
            return [self::WAITING, 'The panel makes the preview. Its first deploy gets the latest commit.'];
        }
        if (Deploys::busy($site)) {
            $preview->update(['deploy_again' => true]);

            return [self::QUEUED, 'A deploy of the preview is in progress. The panel deploys the new commits after it.'];
        }
        $own = self::copySource($source, $preview, $site);
        if ($source->preview_environment) {
            $site->update(['environment' => $source->website->variables()]);
        }
        try {
            DeployController::deploy($this->jobs, $site, $own, $trigger);
        } catch (ValidationException $e) {
            $preview->update(['error' => self::firstError($e), 'deploy_again' => false]);

            return [self::FAILED, $preview->error];
        } catch (HttpException $e) {
            // A different request started a deploy at the same time.
            $preview->update(['deploy_again' => true]);

            return [self::QUEUED, $e->getMessage()];
        }
        $preview->update(['error' => null, 'deploy_again' => false]);

        return [self::STARTED, "The panel deploys the preview {$site->name}."];
    }

    /**
     * Writes the Git source of a preview website: the settings of the website of the pull
     * request, with the head branch. The secrets go to the preview only when the settings
     * permit it.
     */
    private static function copySource(GitSource $source, Preview $preview, Website $site): GitSource
    {
        $own = GitSource::where('website_id', $site->id)->first();
        if (! $own) {
            // The preview has its own webhook token and secret, but its trigger is "manual": the
            // webhook of the website of the pull request starts its deploys.
            $own = $source->replicate(['webhook_token', 'webhook_token_hash', 'webhook_secret']);
            $own->fill(['website_id' => $site->id] + GitSource::newWebhook());
        }
        $own->fill([
            'repository' => $source->repository,
            'branch' => $preview->branch,
            'web_directory' => $source->web_directory,
            'trigger' => GitSource::MANUAL,
            'deploy_queued' => false,
            'queued_commit' => null,
            'checked_at' => null,
            'check_error' => null,
            'build_commands' => $source->build_commands ?? [],
            // A preview does not change a database: the code of a pull request is not reviewed,
            // and the variables can point to the live database.
            'migration_command' => null,
            'public_key' => $source->public_key,
            'private_key' => $source->private_key,
            'secrets' => $source->preview_environment ? ($source->secrets ?? []) : [],
            'previews' => false,
            'preview_domain' => null,
            'preview_environment' => false,
        ])->save();

        return $own;
    }

    private static function openJob(Website $website, AgentAction $action): ?AgentJob
    {
        return AgentJob::where('website_id', $website->id)->where('action', $action)->whereNull('finished_at')->first();
    }

    private static function firstError(ValidationException $e): string
    {
        return (string) collect($e->errors())->flatten()->first();
    }
}
