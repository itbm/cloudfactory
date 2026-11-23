<?php

namespace App\Http\Controllers\Api;

use App\Enums\AgentAction;
use App\Enums\JobState;
use App\Enums\WebsiteStatus;
use App\Http\Controllers\Controller;
use App\Http\Resources\JobResource;
use App\Models\AgentJob;
use App\Models\Website;
use App\Models\WordPressCommand;
use App\Models\WordPressSite;
use App\Services\Audit;
use App\Services\WordPress;
use App\Validation\Input;
use App\Validation\InputRules;
use Illuminate\Http\JsonResponse;
use Illuminate\Http\Request;
use Illuminate\Support\Facades\DB;
use Illuminate\Validation\Rule;
use Illuminate\Validation\ValidationException;

/**
 * FR-206: WordPress on a website. The agent runs WP-CLI in the container of the website, as
 * the Unix user of the website. Each update has a restore point and a health check.
 */
class WordPressController extends Controller
{
    use Concerns;

    /** The WordPress site of a website, or null, and the last WP-CLI commands. */
    public function show(Request $request, string $websiteId): JsonResponse
    {
        $website = $this->findWebsite($request, $websiteId);
        $site = $website->wordpress;

        return response()->json([
            'wordpress' => $site ? WordPress::resource($site) : null,
            'commands' => $website->wordpressCommands()->with('user')->limit(20)->get()->map(fn (WordPressCommand $c) => WordPress::commandResource($c))->all(),
            'busy' => $this->busyJob($website)?->id,
        ]);
    }

    /**
     * Installs WordPress on a website that runs. The document root must be empty or have only
     * the default page. The answer has the password of the administrator, one time.
     */
    public function store(Request $request, string $websiteId): JsonResponse
    {
        $website = $this->changeable($request, $websiteId);
        self::requireDatabaseScope($request);
        $data = $request->validate(WordPress::installRules());
        WordPress::checkWebsite($website);
        abort_if($website->status !== WebsiteStatus::Running, 409, 'The website does not run. Start it before you install WordPress.');
        if ($website->wordpress && $website->wordpress->status !== WordPressSite::FAILED) {
            throw ValidationException::withMessages(['wordpress' => 'This website has WordPress.']);
        }
        $this->refuseBusy($website);
        [$site, $password] = DB::transaction(fn () => WordPress::prepare($website, $data));
        $job = WordPress::install($site);
        Audit::record($request, 'Installed WordPress', $website->name, $website->customer_id);

        return response()->json([
            'wordpress' => WordPress::resource($site->fresh()),
            'admin' => self::admin($website, $site, $password),
            'job' => new JobResource($job),
        ], 202);
    }

    /** The settings of the automatic updates. */
    public function update(Request $request, string $websiteId): JsonResponse
    {
        $website = $this->changeable($request, $websiteId);
        $site = $website->wordpress()->firstOrFail();
        $data = $request->validate([
            'autoUpdates' => ['required', 'array'],
            'autoUpdates.core' => ['sometimes', Rule::in(WordPressSite::CORE_POLICIES)],
            'autoUpdates.plugins' => ['sometimes', 'boolean'],
            'autoUpdates.themes' => ['sometimes', 'boolean'],
        ]);
        $auto = $data['autoUpdates'];
        $site->update(array_filter([
            'auto_update_core' => $auto['core'] ?? null,
            'auto_update_plugins' => $auto['plugins'] ?? null,
            'auto_update_themes' => $auto['themes'] ?? null,
        ], fn ($v) => $v !== null));
        $s = $site->fresh();
        Audit::record($request, 'Changed WordPress updates', "{$website->name}: WordPress {$s->auto_update_core}, plugins ".($s->auto_update_plugins ? 'on' : 'off').', themes '.($s->auto_update_themes ? 'on' : 'off'), $website->customer_id);

        return response()->json(['wordpress' => WordPress::resource($s)]);
    }

    /**
     * Reads the versions and the available updates. On a website with no WordPress record, the
     * panel looks for WordPress in the document root: a site from an import or from SFTP.
     */
    public function check(Request $request, string $websiteId): JsonResponse
    {
        $website = $this->changeable($request, $websiteId);
        WordPress::checkWebsite($website);
        abort_if($website->status !== WebsiteStatus::Running, 409, 'The website does not run. Start it first.');
        $this->refuseBusy($website);
        $job = DB::transaction(function () use ($website) {
            if (! $website->wordpress) {
                $website->wordpress()->create(['status' => WordPressSite::CHECKING]);
            }

            return WordPress::check($website);
        });
        Audit::record($request, 'Checked WordPress', $website->name, $website->customer_id);

        return response()->json(['wordpress' => WordPress::resource($website->wordpress()->first()), 'job' => new JobResource($job)], 202);
    }

    /**
     * Updates WordPress, plugins and themes. The agent saves a restore point first, and puts
     * it back when an update or the health check fails. "all" updates each plugin and theme
     * with an update.
     */
    public function updates(Request $request, string $websiteId): JsonResponse
    {
        $website = $this->changeable($request, $websiteId);
        $site = $this->ready($website);
        $data = $request->validate([
            'core' => ['sometimes', 'nullable', Rule::in(['minor', 'latest'])],
            'plugins' => ['sometimes', 'array', 'max:'.InputRules::MAX_WORDPRESS_UPDATES],
            'plugins.*' => ['string', Input::rule('wordpressSlug')],
            'themes' => ['sometimes', 'array', 'max:'.InputRules::MAX_WORDPRESS_UPDATES],
            'themes.*' => ['string', Input::rule('wordpressSlug')],
            'all' => ['sometimes', 'boolean'],
        ]);
        $plugins = array_values(array_unique($data['plugins'] ?? []));
        $themes = array_values(array_unique($data['themes'] ?? []));
        $core = (string) ($data['core'] ?? '');
        if ($data['all'] ?? false) {
            $plugins = WordPress::withUpdates($site->plugins);
            $themes = WordPress::withUpdates($site->themes);
            $core = $site->core_latest ? 'latest' : ($site->core_minor ? 'minor' : '');
        }
        $known = fn (?array $items) => array_column($items ?? [], 'name');
        foreach (['plugins' => [$plugins, $known($site->plugins)], 'themes' => [$themes, $known($site->themes)]] as $field => [$names, $have]) {
            if ($missing = array_diff($names, $have)) {
                throw ValidationException::withMessages([$field => 'The site does not have '.implode(', ', $missing).'. Check WordPress, then try again.']);
            }
        }
        if ($core === '' && $plugins === [] && $themes === []) {
            throw ValidationException::withMessages(['core' => 'Select an update. Check WordPress to look for new updates.']);
        }
        if (count($plugins) + count($themes) > InputRules::MAX_WORDPRESS_UPDATES) {
            throw ValidationException::withMessages(['plugins' => 'One update can have '.InputRules::MAX_WORDPRESS_UPDATES.' plugins and themes.']);
        }
        $this->refuseBusy($website);
        $job = WordPress::update($site, $core, $plugins, $themes);
        Audit::record($request, 'Updated WordPress', $website->name.': '.implode(', ', array_filter([$core !== '' ? "WordPress ({$core})" : null, ...$plugins, ...$themes])), $website->customer_id);

        return response()->json(['job' => new JobResource($job)], 202);
    }

    /**
     * Puts back the restore point of the last update: the files with no uploads, and the
     * database. The changes to the database since the update go.
     */
    public function restore(Request $request, string $websiteId): JsonResponse
    {
        $website = $this->changeable($request, $websiteId);
        $site = $this->ready($website);
        if (! $site->restore_point) {
            throw ValidationException::withMessages(['wordpress' => 'This site has no restore point. The panel saves one before each update.']);
        }
        $this->refuseBusy($website);
        $job = WordPress::restore($site);
        Audit::record($request, 'Rolled back WordPress update', $website->name, $website->customer_id);

        return response()->json(['job' => new JobResource($job)], 202);
    }

    /**
     * Runs one WP-CLI command in the container of the website. Each argument goes to WP-CLI as
     * it is, with no shell. The audit log and the history have the command with no passwords.
     */
    public function cli(Request $request, string $websiteId): JsonResponse
    {
        $website = $this->changeable($request, $websiteId);
        $data = $request->validate(['args' => ['present', 'array']]);
        abort_if($website->status !== WebsiteStatus::Running, 409, 'The website does not run. Start it before you run a WP-CLI command.');
        WordPress::checkWebsite($website);
        $args = array_map(fn ($a) => is_scalar($a) ? (string) $a : "\0", array_values($data['args']));
        $command = WordPress::run($website, $args, $request->user()?->id);
        Audit::record($request, 'Ran WP-CLI command', "{$website->name}: {$command->command}", $website->customer_id);

        return response()->json(['command' => WordPress::commandResource($command), 'job' => new JobResource(AgentJob::findOrFail($command->agent_job_id))], 202);
    }

    /** The WP-CLI commands of the website, the newest first. */
    public function commands(Request $request, string $websiteId): JsonResponse
    {
        $website = $this->findWebsite($request, $websiteId);

        return response()->json(['data' => $website->wordpressCommands()->with('user')->limit(100)->get()->map(fn (WordPressCommand $c) => WordPress::commandResource($c))->all()]);
    }

    /** A website that a user can change. */
    private function changeable(Request $request, string $websiteId): Website
    {
        $website = $this->findWebsite($request, $websiteId);
        WebsiteController::refuseFailed($website);
        abort_if($website->customer?->isSuspended() ?? false, 409, 'This customer is suspended. Lift the suspension before you change WordPress.');

        return $website;
    }

    private function ready(Website $website): WordPressSite
    {
        $site = $website->wordpress()->firstOrFail();
        abort_if($site->status !== WordPressSite::READY, 409, 'WordPress is not ready on this website. Check WordPress first.');
        abort_if($website->status !== WebsiteStatus::Running, 409, 'The website does not run. Start it first.');

        return $site;
    }

    /** The open WordPress job of the website, if it has one. */
    private function busyJob(Website $website): ?AgentJob
    {
        return AgentJob::where('website_id', $website->id)
            ->whereIn('action', array_map(fn (AgentAction $a) => $a->value, [AgentAction::InstallWordPress, AgentAction::CheckWordPress, AgentAction::UpdateWordPress, AgentAction::RestoreWordPress, AgentAction::StageWordPress]))
            ->whereIn('state', [JobState::Queued->value, JobState::Running->value])->latest()->first();
    }

    /** One WordPress job at a time: an update and a roll back of the same site do not mix. */
    private function refuseBusy(Website $website): void
    {
        abort_if($this->busyJob($website) !== null, 409, 'WordPress has a job on this website now. Wait until it is complete.');
    }

    /**
     * A new WordPress site makes a database. A token needs the databases:write scope too.
     */
    public static function requireDatabaseScope(Request $request): void
    {
        $token = $request->user()?->currentAccessToken();
        abort_if($token && ! $token->can('databases:write'), 403, 'The token does not have the databases:write scope. WordPress needs a database.');
    }

    /**
     * The sign-in of the first administrator. The panel shows the password one time.
     *
     * @return array<string, string>
     */
    public static function admin(Website $website, WordPressSite $site, string $password): array
    {
        return [
            'user' => (string) $site->admin_user,
            'password' => $password,
            'loginUrl' => "https://{$website->name}/wp-login.php",
        ];
    }
}
