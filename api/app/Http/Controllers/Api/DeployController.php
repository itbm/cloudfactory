<?php

namespace App\Http\Controllers\Api;

use App\Enums\AgentAction;
use App\Enums\WebsiteStatus;
use App\Http\Controllers\Controller;
use App\Http\Resources\JobResource;
use App\Models\AgentJob;
use App\Models\GitSource;
use App\Models\Release;
use App\Models\Website;
use App\Services\Audit;
use App\Services\JobDispatcher;
use App\Services\SshKey;
use App\Validation\Input;
use App\Validation\InputRules;
use Illuminate\Http\JsonResponse;
use Illuminate\Http\Request;
use Illuminate\Support\Str;
use Illuminate\Validation\Rule;
use Illuminate\Validation\ValidationException;

/**
 * The deploys of a website from a Git repository.
 *
 * Each deploy makes a release: the agent gets the code into "releases/<id>" in the files of
 * the website, runs the build commands in a build container of the website, and starts the
 * website from the release with no stop. If a step fails, the website continues from the
 * release that it had. A user can go back to an earlier release that is on the server.
 */
class DeployController extends Controller
{
    use Concerns;

    public function __construct(private JobDispatcher $jobs) {}

    public function show(Request $request, string $websiteId): JsonResponse
    {
        return response()->json(self::resource($request, $this->findWebsite($request, $websiteId)));
    }

    /** Connects the website to a repository, or changes the settings of the deploys. */
    public function update(Request $request, string $websiteId): JsonResponse
    {
        $website = $this->changeable($request, $websiteId);
        $data = $request->validate([
            'repository' => ['required', 'string', Input::rule('gitRepository')],
            'branch' => ['required', 'string', Input::rule('gitBranch')],
            // The directory in the repository that the web server serves. "." is the repository itself.
            'webDirectory' => ['sometimes', 'string', 'min:1', Input::rule('documentRoot')],
            'trigger' => ['sometimes', Rule::in([GitSource::MANUAL, GitSource::WEBHOOK])],
            'buildCommands' => ['sometimes', 'array', 'max:'.InputRules::MAX_BUILD_COMMANDS],
            'buildCommands.*' => ['string', Input::rule('commandLine')],
        ]);
        $source = $website->gitSource;
        // A Node.js or Python application is usually at the top of its repository.
        $webDirectory = $data['webDirectory'] ?? $source?->web_directory ?? ($website->image->isApp() ? '.' : 'public');
        // The document root of a release has the directory of the release before this directory.
        if ($webDirectory !== '.' && ! Input::valid('documentRoot', 'releases/'.$website->id.'/'.$webDirectory)) {
            throw ValidationException::withMessages(['webDirectory' => 'Enter a directory with at most 6 parts, for example public.']);
        }
        $values = [
            'repository' => $data['repository'],
            'branch' => $data['branch'],
            'web_directory' => $webDirectory,
            'trigger' => $data['trigger'] ?? $source?->trigger ?? GitSource::MANUAL,
            'build_commands' => array_values(array_map('trim', $data['buildCommands'] ?? $source?->build_commands ?? [])),
        ];
        if ($source) {
            $source->update($values);
        } else {
            $key = SshKey::generate('cloudfactory-'.$website->unix_user);
            $token = Str::random(48);
            $website->gitSource()->create($values + [
                'public_key' => $key['public'],
                'private_key' => $key['private'],
                'webhook_token' => $token,
                'webhook_token_hash' => hash('sha256', $token),
                'secrets' => [],
            ]);
        }
        Audit::record($request, $source ? 'Changed deploy settings' : 'Connected Git repository', $website->name, $website->customer_id);

        return response()->json(self::resource($request, $website->refresh()));
    }

    /**
     * Disconnects the repository. The website continues to serve the release that it has, and
     * its files stay on the server.
     */
    public function destroy(Request $request, string $websiteId): JsonResponse
    {
        $website = $this->changeable($request, $websiteId);
        abort_if($website->releases()->where('state', Release::BUILDING)->exists(), 409, 'A deploy is in progress. Wait until it is complete.');
        $website->gitSource()->delete();
        $website->releases()->delete();
        Audit::record($request, 'Disconnected Git repository', $website->name, $website->customer_id);

        return response()->json(null, 204);
    }

    /** Sets a secret: an environment variable of the build commands. The API never gives its value. */
    public function setSecret(Request $request, string $websiteId, string $name): JsonResponse
    {
        $website = $this->changeable($request, $websiteId);
        $source = $this->source($website);
        if (! Input::valid('secretName', $name)) {
            throw ValidationException::withMessages(['name' => InputRules::MESSAGES['secretName']]);
        }
        $data = $request->validate(['value' => ['required', 'string', Input::rule('secretValue')]]);
        $secrets = $source->secrets ?? [];
        if (! isset($secrets[$name]) && count($secrets) >= InputRules::MAX_DEPLOY_SECRETS) {
            throw ValidationException::withMessages(['name' => 'A website can have '.InputRules::MAX_DEPLOY_SECRETS.' secrets. Remove one first.']);
        }
        $secrets[$name] = $data['value'];
        ksort($secrets);
        $source->update(['secrets' => $secrets]);
        Audit::record($request, 'Set deploy secret', "{$website->name}: {$name}", $website->customer_id);

        return response()->json(self::resource($request, $website->refresh()));
    }

    public function removeSecret(Request $request, string $websiteId, string $name): JsonResponse
    {
        $website = $this->changeable($request, $websiteId);
        $source = $this->source($website);
        $secrets = $source->secrets ?? [];
        abort_unless(isset($secrets[$name]), 404);
        unset($secrets[$name]);
        $source->update(['secrets' => $secrets]);
        Audit::record($request, 'Removed deploy secret', "{$website->name}: {$name}", $website->customer_id);

        return response()->json(self::resource($request, $website->refresh()));
    }

    /** Starts a deploy of the branch. */
    public function start(Request $request, string $websiteId): JsonResponse
    {
        $website = $this->changeable($request, $websiteId);
        $this->refuseSuspended($website->customer);
        [$release, $job] = self::deploy($this->jobs, $website, $this->source($website), GitSource::MANUAL);
        Audit::record($request, 'Started deploy', $website->name, $website->customer_id);

        return response()->json(['release' => self::release($request, $release), 'job' => new JobResource($job)], 202);
    }

    /** Goes back, or forward, to a release that is on the server. */
    public function activate(Request $request, string $websiteId, string $releaseId): JsonResponse
    {
        $website = $this->changeable($request, $websiteId);
        $this->refuseSuspended($website->customer);
        $release = $website->releases()->findOrFail($releaseId);
        if ($release->state !== Release::READY || $release->live || ! $release->document_root) {
            throw ValidationException::withMessages(['release' => $release->live ? 'The website serves this release already.' : 'This release is not on the server.']);
        }
        self::refuseBusy($website);
        $payload = JobDispatcher::websitePayload($website);
        $payload['website']['documentRoot'] = $release->document_root;
        $job = $this->jobs->dispatch($website->server, AgentAction::ActivateRelease, $payload + ['releaseId' => $release->id], 'Go back to release '.self::short($release), $website);
        Audit::record($request, 'Activated release', "{$website->name}: ".self::short($release), $website->customer_id);

        return response()->json(['release' => self::release($request, $release), 'job' => new JobResource($job)], 202);
    }

    /**
     * The webhook of a website: a request with the token of the website starts a deploy. The
     * token is the only credential. A Git host sends the branch of a push as "ref", for example
     * "refs/heads/main": a push to a different branch starts no deploy.
     */
    public function hook(Request $request, string $token): JsonResponse
    {
        $source = GitSource::where('webhook_token_hash', hash('sha256', $token))->first();
        abort_if(! $source || $source->trigger !== GitSource::WEBHOOK, 404);
        $website = $source->website()->with(['image', 'domains', 'server', 'customer'])->firstOrFail();
        $ref = $request->input('ref');
        if (is_string($ref) && $ref !== '' && $ref !== 'refs/heads/'.$source->branch && $ref !== $source->branch) {
            return response()->json(['status' => 'ignored', 'message' => "The deploys of this website use the branch {$source->branch}."]);
        }
        abort_if($website->status === WebsiteStatus::Failed || ($website->customer?->isSuspended() ?? false), 409, 'The website cannot deploy now.');
        if ($website->releases()->where('state', Release::BUILDING)->exists()) {
            return response()->json(['status' => 'busy', 'message' => 'A deploy is in progress.'], 202);
        }
        [$release] = self::deploy($this->jobs, $website, $source, GitSource::WEBHOOK);
        Audit::record($request, 'Started deploy from webhook', $website->name, $website->customer_id);

        return response()->json(['status' => 'started', 'releaseId' => $release->id], 202);
    }

    /**
     * Makes the release and sends the deploy job. The private key and the secrets are not in
     * the job: the panel adds them when it sends the job to the agent (AgentClient).
     *
     * @return array{Release, AgentJob}
     */
    public static function deploy(JobDispatcher $jobs, Website $website, GitSource $source, string $trigger): array
    {
        self::refuseBusy($website);
        $release = new Release(['website_id' => $website->id, 'ref' => $source->branch, 'state' => Release::BUILDING, 'trigger' => $trigger]);
        $release->id = $release->newUniqueId();
        $release->document_root = $source->documentRootOf($release->id);
        $release->save();

        $payload = JobDispatcher::websitePayload($website);
        $payload['website']['documentRoot'] = $release->document_root;
        $job = $jobs->dispatch($website->server, AgentAction::Deploy, $payload + [
            'releaseId' => $release->id,
            'repository' => $source->repository,
            'branch' => $source->branch,
            // A Node.js or Python website installs its dependencies in the new release first.
            'buildCommands' => self::buildCommands($website, $source),
            // The earlier releases that stay on the server, for "Go back".
            'keepReleases' => $website->releases()->where('state', Release::READY)->limit(Release::KEEP)->pluck('id')->all(),
        ], "Deploy {$source->branch}", $website);
        $release->update(['agent_job_id' => $job->id]);

        return [$release, $job];
    }

    /**
     * The build commands of a deploy. The install command of a Node.js or Python website comes
     * first, in the web directory of the release. The other commands run in the release.
     *
     * @return list<string>
     */
    public static function buildCommands(Website $website, GitSource $source): array
    {
        $commands = $source->build_commands ?? [];
        if ($website->image->isApp() && $website->install_command && ! in_array($website->install_command, $commands, true)) {
            $dir = $source->web_directory === '.' ? '' : 'cd '.escapeshellarg($source->web_directory).' && ';
            // In a subshell, so that the next commands run in the release.
            $install = '('.$dir.$website->install_command.')';
            // The agent takes at most MAX_BUILD_COMMANDS lines. "&&" keeps the order and stops
            // at the first error, as "set -e" of the build script does.
            if (count($commands) >= InputRules::MAX_BUILD_COMMANDS) {
                $commands[0] = $install.' && '.$commands[0];
            } else {
                array_unshift($commands, $install);
            }
        }

        return $commands;
    }

    /** One deploy or one change of the release at a time. */
    private static function refuseBusy(Website $website): void
    {
        $busy = $website->releases()->where('state', Release::BUILDING)->exists()
            || AgentJob::where('website_id', $website->id)->where('action', AgentAction::ActivateRelease)->whereNull('finished_at')->exists();
        abort_if($busy, 409, 'A deploy is in progress. Wait until it is complete.');
    }

    private function changeable(Request $request, string $websiteId): Website
    {
        $website = $this->findWebsite($request, $websiteId);
        WebsiteController::refuseFailed($website);
        abort_if($website->customer?->isSuspended() ?? false, 409, 'This customer is suspended. Lift the suspension before you change the deploys.');

        return $website;
    }

    private function source(Website $website): GitSource
    {
        return $website->gitSource ?? throw ValidationException::withMessages(['repository' => 'Connect a Git repository first.']);
    }

    private static function short(Release $release): string
    {
        return $release->commit ? substr($release->commit, 0, 7) : substr($release->id, -6);
    }

    /** @return array<string, mixed> */
    public static function resource(Request $request, Website $website): array
    {
        $source = $website->gitSource;

        return [
            'source' => $source ? [
                'repository' => $source->repository,
                'branch' => $source->branch,
                'webDirectory' => $source->web_directory,
                'trigger' => $source->trigger,
                'buildCommands' => $source->build_commands ?? [],
                // The owner adds this key to the repository as a read-only deploy key.
                'deployKey' => $source->public_key,
                'webhookUrl' => url('/api/v1/hooks/deploy/'.$source->webhook_token),
                // The names only. The API never gives the value of a secret.
                'secrets' => array_keys($source->secrets ?? []),
            ] : null,
            'releases' => $website->releases()->limit(20)->get()->map(fn (Release $r) => self::release($request, $r))->all(),
        ];
    }

    /** @return array<string, mixed> */
    public static function release(Request $request, Release $r): array
    {
        return [
            'id' => $r->id,
            'branch' => $r->ref,
            'commit' => $r->commit,
            'message' => $r->message,
            'state' => $r->state,
            'live' => $r->live,
            'trigger' => $r->trigger,
            'durationSeconds' => $r->duration_seconds,
            'error' => self::errorFor($request, $r),
            'jobId' => $r->agent_job_id,
            'createdAt' => $r->created_at?->toIso8601String(),
        ];
    }

    /**
     * The error of a deploy that failed. The output of Git and of a build command is from the
     * build container of the website, so its owner sees it. A different error can show the
     * server from the inside: only an administrator sees that one (JobResource).
     */
    private static function errorFor(Request $request, Release $r): ?string
    {
        $error = $r->error;
        if ($error === null || $request->user()?->isAdministrator()) {
            return $error;
        }
        if (preg_match('/^(a build command failed|the agent cannot get the code): /', $error)) {
            return $error;
        }
        if (preg_match(JobResource::INTERNAL, $error)) {
            return 'A step failed on the server. Your provider can see the details.';
        }

        return Str::limit(trim((string) strtok($error, "\r\n")), 300);
    }
}
