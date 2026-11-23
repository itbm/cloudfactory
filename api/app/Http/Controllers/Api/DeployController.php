<?php

namespace App\Http\Controllers\Api;

use App\Enums\AgentAction;
use App\Http\Controllers\Controller;
use App\Http\Resources\JobResource;
use App\Models\AgentJob;
use App\Models\GitSource;
use App\Models\Release;
use App\Models\Website;
use App\Services\Audit;
use App\Services\Deploys\Deploys;
use App\Services\Deploys\Webhook;
use App\Services\Deploys\WebhookEvent;
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
 * the website, runs the build commands and the migration command in a build container of the
 * website, and starts the website from the release with no stop. Then the agent checks the
 * website for 5 minutes (FR-1008). If a step or this check fails, the website goes back to the
 * release that it had. After a migration, the website keeps the new release, and the owner
 * gets an alert (FR-1009). A user can go back to an earlier release that is on the server.
 *
 * A deploy starts by hand, from a signed webhook of the Git host, or from a check of the
 * branch on a schedule (FR-1004, BranchChecks).
 */
class DeployController extends Controller
{
    use Concerns;

    /** The largest interval of the branch check: one day. */
    public const MAX_CHECK_MINUTES = 1440;

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
            'trigger' => ['sometimes', Rule::in(GitSource::TRIGGERS)],
            // FR-1004: the interval of the branch check, for the trigger "schedule".
            'checkMinutes' => ['sometimes', 'integer', 'min:1', 'max:'.self::MAX_CHECK_MINUTES],
            'buildCommands' => ['sometimes', 'array', 'max:'.InputRules::MAX_BUILD_COMMANDS],
            'buildCommands.*' => ['string', Input::rule('commandLine')],
            // FR-1009: a command that changes the database. Empty removes it.
            'migrationCommand' => ['sometimes', 'nullable', 'string', Input::rule('commandLine')],
        ]);
        $source = $website->gitSource;
        // A Node.js or Python application is usually at the top of its repository.
        $webDirectory = $data['webDirectory'] ?? $source?->web_directory ?? ($website->image->isApp() ? '.' : 'public');
        // The document root of a release has the directory of the release before this directory.
        if ($webDirectory !== '.' && ! Input::valid('documentRoot', 'releases/'.$website->id.'/'.$webDirectory)) {
            throw ValidationException::withMessages(['webDirectory' => 'Enter a directory with at most 6 parts, for example public.']);
        }
        $migration = array_key_exists('migrationCommand', $data) ? trim((string) $data['migrationCommand']) : $source?->migration_command;
        $values = [
            'repository' => $data['repository'],
            'branch' => $data['branch'],
            'web_directory' => $webDirectory,
            'trigger' => $data['trigger'] ?? $source?->trigger ?? GitSource::MANUAL,
            'check_minutes' => (int) ($data['checkMinutes'] ?? $source?->check_minutes ?? GitSource::CHECK_MINUTES),
            'build_commands' => array_values(array_map('trim', $data['buildCommands'] ?? $source?->build_commands ?? [])),
            'migration_command' => $migration === '' ? null : $migration,
        ];
        // A new branch, repository or trigger gets a new check soon. The result of the last
        // check is not about the new settings.
        if ($source && ($source->repository !== $values['repository'] || $source->branch !== $values['branch'] || $source->trigger !== $values['trigger'])) {
            $values += ['checked_at' => null, 'check_error' => null, 'deploy_queued' => false, 'queued_commit' => null];
        }
        if ($source) {
            $source->update($values);
        } else {
            $key = SshKey::generate('cloudfactory-'.$website->unix_user);
            $website->gitSource()->create($values + GitSource::newWebhook() + [
                'public_key' => $key['public'],
                'private_key' => $key['private'],
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

    /**
     * Makes a new webhook address and a new webhook secret. The old address stops at once. Give
     * the new address and secret to the Git host.
     */
    public function newWebhook(Request $request, string $websiteId): JsonResponse
    {
        $website = $this->changeable($request, $websiteId);
        $this->source($website)->update(GitSource::newWebhook());
        Audit::record($request, 'Changed webhook address', $website->name, $website->customer_id);

        return response()->json(self::resource($request, $website->refresh()));
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
        // The agent keeps the SSH host key of the Git server from the first deploy, and refuses
        // a different key. After a real change of the key, the user tells the agent to forget
        // the old key (adversarial review, finding A2-04).
        $data = $request->validate(['forgetHostKey' => ['sometimes', 'boolean']]);
        $forget = filter_var($data['forgetHostKey'] ?? false, FILTER_VALIDATE_BOOLEAN);
        [$release, $job] = self::deploy($this->jobs, $website, $this->source($website), GitSource::MANUAL, $forget);
        Audit::record($request, $forget ? 'Started deploy and removed the SSH host key of the Git server' : 'Started deploy', $website->name, $website->customer_id);

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
     * The webhook of a website (FR-1004). The token in the address finds the Git source, and
     * the request must have a correct signature from the webhook secret (Webhook::verify). A
     * push to the branch of the website starts a deploy when the trigger is "webhook". A push
     * during a deploy starts a deploy when that deploy is complete (Deploys::jobFinished).
     */
    public function hook(Request $request, string $token): JsonResponse
    {
        $source = GitSource::where('webhook_token_hash', hash('sha256', $token))->first();
        abort_if(! $source, 404);
        if (! Webhook::verify($request, (string) $source->webhook_secret)) {
            return response()->json(['message' => 'The request has no correct signature. Give the webhook secret of the website to the Git host.'], 401);
        }
        $event = Webhook::read($request);

        return match ($event->kind) {
            WebhookEvent::PING => response()->json(['status' => 'ok', 'message' => 'The webhook operates.']),
            WebhookEvent::PUSH => $this->pushed($request, $source, $event),
            default => response()->json(['status' => 'ignored', 'message' => 'The panel does not use this event.']),
        };
    }

    /** A push from the Git host. */
    private function pushed(Request $request, GitSource $source, WebhookEvent $event): JsonResponse
    {
        if ($source->trigger !== GitSource::WEBHOOK) {
            return response()->json(['status' => 'ignored', 'message' => 'The deploys of this website do not start with the webhook.']);
        }
        if (! $event->pushed($source->branch)) {
            return response()->json(['status' => 'ignored', 'message' => "The deploys of this website use the branch {$source->branch}."]);
        }
        $website = $source->website()->with(['image', 'domains', 'server', 'customer'])->firstOrFail();
        abort_unless(Deploys::allowed($website), 409, 'The website cannot deploy now.');
        $commit = $event->commit($source->branch);
        if (Deploys::busy($website)) {
            $source->update(['deploy_queued' => true, 'queued_commit' => $commit]);

            return response()->json(['status' => 'queued', 'message' => 'A deploy is in progress. The panel deploys this push when it is complete.'], 202);
        }
        [$release] = self::deploy($this->jobs, $website, $source, GitSource::WEBHOOK, commit: $commit);
        Audit::record($request, 'Started deploy from webhook', $website->name, $website->customer_id);

        return response()->json(['status' => 'started', 'releaseId' => $release->id], 202);
    }

    /**
     * Makes the release and sends the deploy job. The private key and the secrets are not in
     * the job: the panel adds them when it sends the job to the agent (AgentClient). $commit is
     * the commit that the webhook or the branch check gave. The agent gives the commit that it
     * got, which can be newer.
     *
     * @return array{Release, AgentJob}
     */
    public static function deploy(JobDispatcher $jobs, Website $website, GitSource $source, string $trigger, bool $forgetHostKey = false, ?string $commit = null): array
    {
        self::refuseBusy($website);
        $release = new Release(['website_id' => $website->id, 'ref' => $source->branch, 'state' => Release::BUILDING, 'trigger' => $trigger,
            'commit' => $commit !== null && preg_match(Webhook::COMMIT, $commit) ? $commit : null]);
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
            'migrationCommand' => (string) $source->migration_command,
            // FR-1008: the agent checks the website for 5 minutes after the switch, and goes
            // back to the previous release when the check fails.
            'healthCheck' => JobDispatcher::HEALTH_CHECK,
            // The earlier releases that stay on the server, for "Go back".
            'keepReleases' => $website->releases()->where('state', Release::READY)->limit(Release::KEEP)->pluck('id')->all(),
        ] + ($forgetHostKey ? ['forgetHostKey' => true] : []), "Deploy {$source->branch}", $website);
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
        // Adversarial review, finding X2-05: the panel made a command after the check of the
        // request. The agent refuses a command that is too long, so the panel checks it here.
        foreach ($commands as $command) {
            if (! Input::valid('commandLine', $command)) {
                throw ValidationException::withMessages(['buildCommands' => 'The install command and the first build command are too long together. Make one of them shorter.']);
            }
        }

        return $commands;
    }

    /** One deploy or one change of the release at a time. */
    private static function refuseBusy(Website $website): void
    {
        abort_if(Deploys::busy($website), 409, 'A deploy is in progress. Wait until it is complete.');
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

        $writer = self::canWrite($request);

        return [
            'source' => $source ? [
                'repository' => $source->repository,
                'branch' => $source->branch,
                'webDirectory' => $source->web_directory,
                'trigger' => $source->trigger,
                'checkMinutes' => $source->check_minutes,
                // The last check of the branch, for the trigger "schedule".
                'checkedAt' => $source->checked_at?->toIso8601String(),
                'checkError' => self::visibleError($request, $source->check_error),
                'buildCommands' => $source->build_commands ?? [],
                'migrationCommand' => $source->migration_command,
                // The owner adds this key to the repository as a read-only deploy key.
                'deployKey' => $source->public_key,
                // Adversarial review, finding A2-05: the address starts a deploy, so only a
                // token that can change websites gets it and the secret of its signatures.
                'webhookUrl' => $writer ? url('/api/v1/hooks/deploy/'.$source->webhook_token) : null,
                'webhookSecret' => $writer ? $source->webhook_secret : null,
                // True when a push came during a deploy: the panel deploys it next.
                'deployQueued' => $source->deploy_queued,
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
            // True when the deploy ran the migration command.
            'migration' => $r->migration,
            // FR-1009: the error of the health check after the switch of a deploy with a
            // migration. The website kept this release.
            'healthError' => self::visibleError($request, $r->health_error),
            'durationSeconds' => $r->duration_seconds,
            'error' => self::visibleError($request, $r->error),
            'jobId' => $r->agent_job_id,
            'createdAt' => $r->created_at?->toIso8601String(),
        ];
    }

    /**
     * The error of a deploy or of a branch check. The output of Git, of a build command and of
     * the migration command is from the build container of the website, so its owner sees it.
     * A failed health check shows the number of failed checks to the owner. A different error
     * can show the server from the inside: only an administrator sees that one (JobResource).
     */
    private static function visibleError(Request $request, ?string $error): ?string
    {
        if ($error === null || $request->user()?->isAdministrator()) {
            return $error;
        }
        if (preg_match('/^(a build command failed|the migration command failed|the agent cannot get the code|the agent cannot read the branch): /', $error)) {
            return $error;
        }
        if (preg_match('/^(the health check failed after the switch: \d+ of \d+ checks failed)/', $error, $m)) {
            // The last error of the check can have the internal address of the website.
            return $m[1].'.';
        }
        if (preg_match(JobResource::INTERNAL, $error)) {
            return 'A step failed on the server. Your provider can see the details.';
        }

        return Str::limit(trim((string) strtok($error, "\r\n")), 300);
    }

    /** True for a token with the scope to change websites. */
    private static function canWrite(Request $request): bool
    {
        $token = $request->user()?->currentAccessToken();

        return (bool) $token?->can('websites:write');
    }
}
