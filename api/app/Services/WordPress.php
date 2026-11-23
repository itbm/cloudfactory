<?php

namespace App\Services;

use App\Console\Commands\InstallScheduledUpdates;
use App\Enums\AgentAction;
use App\Enums\JobState;
use App\Enums\WebsiteStatus;
use App\Http\Resources\JobResource;
use App\Models\AgentJob;
use App\Models\Database;
use App\Models\DatabaseServer;
use App\Models\DatabaseUser;
use App\Models\Server;
use App\Models\Website;
use App\Models\WordPressCommand;
use App\Models\WordPressSite;
use App\Validation\Input;
use App\Validation\InputRules;
use Illuminate\Support\Carbon;
use Illuminate\Support\Facades\DB;
use Illuminate\Support\Str;
use Illuminate\Validation\ValidationException;

/**
 * FR-206: WordPress sites. The agent installs WordPress with WP-CLI in the document root of a
 * PHP website, reads the versions and the available updates, and updates WordPress, plugins
 * and themes with a restore point and a health check. The panel keeps what the agent reports.
 */
final class WordPress
{
    /** The agent feature of an agent that runs WP-CLI. */
    public const FEATURE = 'wordpress';

    /** The WordPress actions of the agent. */
    public const ACTIONS = [AgentAction::InstallWordPress, AgentAction::CheckWordPress, AgentAction::UpdateWordPress, AgentAction::RestoreWordPress, AgentAction::RunWpCli, AgentAction::StageWordPress];

    /** The health check after an update: most answers of the site in 60 seconds are not server errors. */
    public const HEALTH_CHECK = ['status' => 0, 'maxErrorRate' => 0.1, 'seconds' => 60];

    /** The options of WP-CLI whose value is a password. The panel does not keep the value. */
    private const SECRET_OPTIONS = ['user_pass', 'admin_password', 'dbpass', 'password', 'pass', 'new_password'];

    /** The WP-CLI commands that need a terminal. The agent runs WP-CLI with no terminal. */
    private const INTERACTIVE = [['shell'], ['db', 'cli']];

    /**
     * Refuses a website that cannot get WordPress: a website that is not a public PHP website,
     * or a server whose agent does not run WP-CLI.
     *
     * @throws ValidationException
     */
    public static function checkWebsite(Website $website, string $field = 'wordpress'): void
    {
        if (! $website->isPublic() || ! $website->image?->isPhp()) {
            throw ValidationException::withMessages([$field => 'WordPress needs a public website with a PHP runtime.']);
        }
        if ($website->gitSource()->exists()) {
            throw ValidationException::withMessages([$field => 'This website gets its files from a Git repository. Install WordPress on a website with no deploys.']);
        }
        if (! $website->server->hasAgentFeature(self::FEATURE)) {
            throw ValidationException::withMessages([$field => "The agent on the server {$website->server->name} cannot install WordPress. Update the agent of the server, then try again."]);
        }
    }

    /**
     * Makes the database and the user of a new WordPress site, and the record of the site with
     * the install request. The install request has the passwords: it is encrypted, and the
     * panel removes it when it sends the install job. The caller runs this in a transaction.
     *
     * @param  array{title: string, adminUser: string, adminEmail: string, locale?: string|null}  $input
     * @return array{0: WordPressSite, 1: string} The site and the password of the administrator.
     *
     * @throws ValidationException
     */
    public static function prepare(Website $website, array $input): array
    {
        $server = self::databaseServer($website);
        $old = $website->wordpress;
        // An install that failed keeps its database. The new install uses it again.
        $database = $old?->database;
        if (! $database) {
            DatabaseServers::checkSpace($server, 1, 'wordpress');
            $name = self::databaseName($website);
            $database = $website->databases()->create(['engine' => 'mariadb', 'name' => $name, 'database_server_id' => $server->id]);
            $user = $website->databaseUsers()->create(['engine' => 'mariadb', 'name' => $name, 'access' => 'website', 'database_server_id' => $server->id]);
            $user->databases()->attach($database->id, ['privileges' => 'readwrite']);
        } else {
            $server = DatabaseServer::findOrFail($database->database_server_id);
        }
        $password = Input::databasePassword();
        $install = [
            'websiteId' => $website->id,
            'databaseServer' => $server->forAgent(),
            'databaseName' => $database->name,
            'databaseUser' => $database->name,
            'databasePassword' => Input::databasePassword(),
            'title' => $input['title'],
            'adminUser' => $input['adminUser'],
            'adminEmail' => Str::lower($input['adminEmail']),
            'adminPassword' => $password,
            'locale' => $input['locale'] ?? 'en_GB',
            'url' => "https://{$website->name}",
        ];
        $site = WordPressSite::updateOrCreate(['website_id' => $website->id], [
            'database_id' => $database->id,
            'status' => WordPressSite::INSTALLING,
            'admin_user' => $input['adminUser'],
            'error' => null,
            'pending_install' => $install,
        ]);

        return [$site, $password];
    }

    /**
     * The validation rules of an install request.
     *
     * @return array<string, mixed>
     */
    public static function installRules(string $prefix = ''): array
    {
        return [
            $prefix.'title' => ['required', 'string', Input::rule('wordpressTitle')],
            $prefix.'adminUser' => ['required', 'string', Input::rule('wordpressUser')],
            $prefix.'adminEmail' => ['required', 'string', 'max:254', fn (string $attribute, mixed $value, \Closure $fail) => Input::valid('mailAddress', Str::lower((string) $value)) ? null : $fail(InputRules::MESSAGES['mailAddress'])],
            $prefix.'locale' => ['sometimes', 'nullable', 'string', Input::rule('wordpressLocale')],
        ];
    }

    /**
     * A MariaDB database server of the customer on the server of the website, with space for one
     * more database. With none, the panel makes one. WP-CLI connects to it from the container of
     * the website, so a database server on a different server is not for a new site.
     *
     * @throws ValidationException
     */
    private static function databaseServer(Website $website): DatabaseServer
    {
        $local = DatabaseServer::with('plan')->withCount('databases')
            ->where(['customer_id' => $website->customer_id, 'server_id' => $website->server_id, 'engine' => 'mariadb'])
            ->whereNotIn('status', [DatabaseServer::REMOVING, DatabaseServer::MOVING])
            ->orderBy('port')->get()
            ->first(fn (DatabaseServer $s) => $s->databases_count < $s->plan->databases);

        return $local ?? DatabaseServers::make($website->customer, $website->server, 'mariadb', field: 'wordpress');
    }

    /** The first free name of the form <unix user>_wp, <unix user>_wp2 and so on. */
    private static function databaseName(Website $website): string
    {
        for ($n = 1; $n < 100; $n++) {
            $name = DatabaseNames::full($website, 'wordpress', $n === 1 ? 'wp' : "wp{$n}");
            $used = Database::where(['engine' => 'mariadb', 'name' => $name])->exists()
                || DatabaseUser::where(['engine' => 'mariadb', 'name' => $name])->exists();
            if (! $used) {
                return $name;
            }
        }
        throw ValidationException::withMessages(['wordpress' => 'The website has no free database name for WordPress.']);
    }

    /** Sends the install request of a site to the agent, and removes the passwords from the panel. */
    public static function install(WordPressSite $site): AgentJob
    {
        $website = $site->website;
        $job = app(JobDispatcher::class)->dispatch($website->server, AgentAction::InstallWordPress, $site->pending_install,
            "Install WordPress on {$website->name}", $website);
        $site->update(['pending_install' => null]);

        return $job;
    }

    /**
     * FR-205: the staging copy of a website with a WordPress site. The copy gets its own record
     * of the site, with its database copy. In the job that makes the copy, the agent sets the
     * database copy and its user in wp-config.php of the copy. When the copy runs, the panel
     * sends the job that changes the address of the site (stage). The caller runs this in the
     * transaction that makes the copy.
     *
     * @param  list<array{engine: string, name: string, password: string, databaseServer: string}>  $users  The database users of the copy.
     * @return array{database: string, user: string}|null The WordPress copy for the agent, or null when the copy has no WordPress site.
     */
    public static function prepareCopy(Website $source, Website $copy, array $users): ?array
    {
        $site = $source->wordpress()->with('database')->first();
        $database = $site?->database;
        // A site with no known database, or with a database that the copy does not get, stays
        // as the files only. The owner sets its database in wp-config.php of the copy.
        if (! $site || $site->status !== WordPressSite::READY || ! $database || $database->engine !== 'mariadb') {
            return null;
        }
        $name = DatabaseNames::prefix($copy).substr($database->name, strlen(DatabaseNames::prefix($source)));
        $new = $copy->databases()->where(['engine' => 'mariadb', 'name' => $name])->first();
        $user = collect($users)->first(fn (array $u) => $u['engine'] === 'mariadb' && $u['databaseServer'] === $database->database_server_id);
        if (! $new || ! $user) {
            return null;
        }
        WordPressSite::create([
            'website_id' => $copy->id,
            'database_id' => $new->id,
            'status' => WordPressSite::COPYING,
            'version' => $site->version,
            'plugins' => $site->plugins,
            'themes' => $site->themes,
            'admin_user' => $site->admin_user,
            // The copy is for tests: the panel does not update it automatically.
            'auto_update_core' => 'off',
            'auto_update_plugins' => false,
            'auto_update_themes' => false,
        ]);

        return ['database' => $name, 'user' => $user['name']];
    }

    /**
     * Sends the job that gives the WordPress site of a new staging copy the address of the copy.
     * The agent also reads the address in the database copy.
     */
    public static function stage(WordPressSite $site): AgentJob
    {
        $website = $site->website;
        $source = $website->staging_of ? Website::with('wordpress')->find($website->staging_of) : null;
        $from = array_filter([$source?->wordpress?->site_url, $source ? "https://{$source->name}" : null],
            fn (?string $url) => is_string($url) && preg_match('~^https?://([^/:?#]+)(/[^?#]*)?$~i', $url, $m) === 1 && Input::valid('domain', Str::lower($m[1])));

        return app(JobDispatcher::class)->dispatch($website->server, AgentAction::StageWordPress, [
            'websiteId' => $website->id,
            'url' => "https://{$website->name}",
            'fromUrls' => array_values(array_unique($from)),
        ], "Set up WordPress on the staging copy {$website->name}", $website);
    }

    /** Asks the agent for the versions and the available updates of a site. */
    public static function check(Website $website, bool $automatic = false): AgentJob
    {
        return app(JobDispatcher::class)->dispatch($website->server, AgentAction::CheckWordPress,
            ['websiteId' => $website->id, ...($automatic ? ['automatic' => true] : [])],
            ($automatic ? 'Look for WordPress updates on ' : 'Check WordPress on ').$website->name, $website);
    }

    /**
     * Starts an update. $core is "", "minor" or "latest". The panel sends only the plugins and
     * themes with an update.
     *
     * @param  list<string>  $plugins
     * @param  list<string>  $themes
     */
    public static function update(WordPressSite $site, string $core, array $plugins, array $themes, bool $automatic = false): AgentJob
    {
        $website = $site->website;
        $parts = array_filter([
            $core !== '' ? 'WordPress' : null,
            $plugins ? count($plugins).' '.Str::plural('plugin', count($plugins)) : null,
            $themes ? count($themes).' '.Str::plural('theme', count($themes)) : null,
        ]);

        return app(JobDispatcher::class)->dispatch($website->server, AgentAction::UpdateWordPress, [
            'websiteId' => $website->id,
            'core' => $core,
            'plugins' => array_values($plugins),
            'themes' => array_values($themes),
            ...self::databaseRef($site),
            'healthCheck' => self::HEALTH_CHECK,
            ...($automatic ? ['automatic' => true] : []),
        ], ($automatic ? 'Automatic update of ' : 'Update ').implode(', ', $parts)." on {$website->name}", $website);
    }

    /** Puts back the restore point of the last update. */
    public static function restore(WordPressSite $site): AgentJob
    {
        $website = $site->website;

        return app(JobDispatcher::class)->dispatch($website->server, AgentAction::RestoreWordPress, [
            'websiteId' => $website->id,
            ...self::databaseRef($site),
        ], "Roll back the last WordPress update on {$website->name}", $website);
    }

    /**
     * The database of a site for the restore point. A database on a different server is not in
     * the restore point: the restore point then has only the files.
     *
     * @return array<string, mixed>
     */
    private static function databaseRef(WordPressSite $site): array
    {
        $database = $site->database;
        if (! $database || $database->engine !== 'mariadb' || ! $database->database_server_id) {
            return [];
        }
        $server = DatabaseServer::find($database->database_server_id);
        if (! $server || $server->server_id !== $site->website->server_id) {
            return [];
        }

        return ['database' => ['engine' => 'mariadb', 'name' => $database->name, 'databaseServer' => $server->id]];
    }

    /**
     * Runs one WP-CLI command. The panel keeps the command with no passwords, and the result.
     *
     * @param  list<string>  $args
     *
     * @throws ValidationException
     */
    public static function run(Website $website, array $args, ?int $userId): WordPressCommand
    {
        self::checkArguments($args);
        $command = self::redact($args);
        $job = app(JobDispatcher::class)->dispatch($website->server, AgentAction::RunWpCli,
            ['websiteId' => $website->id, 'args' => array_values($args)], 'WP-CLI: '.Str::limit($command, 80), $website);
        // The job has the arguments. The panel removes them when the job ends (withoutPasswords).

        return $website->wordpressCommands()->create(['agent_job_id' => $job->id, 'user_id' => $userId, 'command' => Str::limit($command, 1990), 'status' => 'running']);
    }

    /**
     * @param  list<mixed>  $args
     *
     * @throws ValidationException
     */
    public static function checkArguments(array $args): void
    {
        if ($args === []) {
            throw ValidationException::withMessages(['args' => 'Enter a WP-CLI command, for example plugin list.']);
        }
        if (count($args) > InputRules::MAX_WP_CLI_ARGUMENTS) {
            throw ValidationException::withMessages(['args' => 'A WP-CLI command can have '.InputRules::MAX_WP_CLI_ARGUMENTS.' arguments.']);
        }
        foreach ($args as $arg) {
            if (! Input::valid('wpCliArgument', $arg)) {
                throw ValidationException::withMessages(['args' => InputRules::MESSAGES['wpCliArgument']]);
            }
        }
        $words = array_values(array_filter($args, fn (string $a) => ! str_starts_with($a, '-')));
        foreach (self::INTERACTIVE as $interactive) {
            if (array_slice($words, 0, count($interactive)) === $interactive) {
                throw ValidationException::withMessages(['args' => 'WP-CLI runs with no terminal here. "wp '.implode(' ', $interactive).'" waits for input, so use a command that does not.']);
            }
        }
    }

    /**
     * The command as one line, with each password option as "***". An argument with a space
     * or a quote is in single quotes.
     *
     * @param  list<string>  $args
     */
    public static function redact(array $args): string
    {
        $out = [];
        foreach ($args as $arg) {
            if (preg_match('/^--([a-z_-]+)=/', $arg, $m) && in_array(str_replace('-', '_', $m[1]), self::SECRET_OPTIONS, true)) {
                $out[] = "--{$m[1]}=***";

                continue;
            }
            $out[] = $arg === '' || preg_match('/[\s\'"\\\\$`;&|<>(){}*?!#~]/', $arg) ? "'".str_replace("'", "'\\''", $arg)."'" : $arg;
        }

        return 'wp '.implode(' ', $out);
    }

    /**
     * The panel applies the result of a job. The JobOutcome transaction runs this.
     *
     * @param  array<string, mixed>  $result
     */
    public static function jobFinished(AgentJob $job, bool $succeeded, array $result): void
    {
        if ($job->action === AgentAction::CreateWebsite) {
            self::websiteMade($job, $succeeded);

            return;
        }
        if (! in_array($job->action, self::ACTIONS, true) || ! $job->website_id) {
            return;
        }
        $site = WordPressSite::where('website_id', $job->website_id)->first();
        $p = $job->payload ?? [];
        match ($job->action) {
            AgentAction::RunWpCli => self::commandRan($job, $site, $succeeded, $result),
            AgentAction::InstallWordPress => $site && ($succeeded
                ? self::apply($site, $result['wordpress'] ?? [], ['status' => WordPressSite::READY, 'error' => null])
                : $site->update(['status' => WordPressSite::FAILED, 'error' => JobResource::publicError($job)])),
            AgentAction::CheckWordPress => $site && self::checked($job, $site, $succeeded, $result, (bool) ($p['automatic'] ?? false)),
            AgentAction::UpdateWordPress => $site && self::updated($job, $site, $succeeded, $result),
            AgentAction::RestoreWordPress => $site && ($succeeded
                ? self::apply($site, $result['wordpress'] ?? [], ['restore_point' => [...($site->restore_point ?? []), 'restoredAt' => now()->toIso8601String()], 'error' => null])
                : $site->update(['error' => JobResource::publicError($job)])),
            // The copy uses its database copy also when the job fails: the owner can use WP-CLI.
            AgentAction::StageWordPress => $site && ($succeeded
                ? self::apply($site, $result['wordpress'] ?? [], ['status' => WordPressSite::READY, 'error' => null])
                : $site->update(['status' => WordPressSite::READY, 'error' => 'The panel did not change the site address of the copy: '.JobResource::publicError($job)])),
            default => null,
        };
    }

    /**
     * A new website with WordPress: the install starts when the website runs. A staging copy of
     * a WordPress site gets the address of the copy.
     */
    private static function websiteMade(AgentJob $job, bool $succeeded): void
    {
        $site = $job->website_id ? WordPressSite::where('website_id', $job->website_id)->first() : null;
        if ($site?->status === WordPressSite::COPYING) {
            $succeeded
                ? self::stage($site)
                : $site->update(['status' => WordPressSite::FAILED, 'error' => 'The panel did not make the staging copy of WordPress, because the website was not made.']);

            return;
        }
        if (! $site || $site->pending_install === null) {
            return;
        }
        if (! $succeeded) {
            // The passwords do not stay in the panel. "Install WordPress" makes new ones.
            $site->update(['status' => WordPressSite::FAILED, 'pending_install' => null, 'error' => 'The panel did not install WordPress, because the website was not made.']);

            return;
        }
        self::install($site);
    }

    /** @param array<string, mixed> $result */
    private static function commandRan(AgentJob $job, ?WordPressSite $site, bool $succeeded, array $result): void
    {
        WordPressCommand::where('agent_job_id', $job->id)->update($succeeded
            ? ['status' => 'done', 'exit_code' => (int) ($result['exitCode'] ?? -1), 'output' => Str::limit((string) ($result['output'] ?? ''), 16000), 'ran_at' => self::time($result['ranAt'] ?? null) ?? now()]
            : ['status' => 'failed', 'output' => (string) JobResource::publicError($job), 'ran_at' => now()]);
        if ($site && $succeeded && is_array($result['wordpress'] ?? null)) {
            self::apply($site, $result['wordpress'], $site->status === WordPressSite::FAILED ? [] : ['status' => WordPressSite::READY]);
        }
    }

    /** @param array<string, mixed> $result */
    private static function checked(AgentJob $job, WordPressSite $site, bool $succeeded, array $result, bool $automatic): bool
    {
        if (! $succeeded) {
            // The panel looked for WordPress on a website and did not find it.
            if ($site->status === WordPressSite::CHECKING) {
                $site->delete();

                return true;
            }

            return $site->update(['error' => JobResource::publicError($job), ...($automatic ? ['auto_checked_at' => now()] : [])]);
        }
        $changes = ['status' => WordPressSite::READY, 'error' => null, ...($automatic ? ['auto_checked_at' => now()] : [])];
        // A site that the panel did not install: the database in wp-config.php, when it is a
        // database of the website.
        $name = $result['wordpress']['databaseName'] ?? null;
        if (! $site->database_id && is_string($name)) {
            $database = $site->website->databases()->where(['engine' => 'mariadb', 'name' => $name])->first();
            $changes['database_id'] = $database?->id;
        }
        self::apply($site, $result['wordpress'] ?? [], $changes);
        if ($automatic) {
            self::automaticUpdate($site->fresh());
        }

        return true;
    }

    /** Starts the automatic update of a site after a check, as its settings allow. */
    private static function automaticUpdate(WordPressSite $site): void
    {
        $core = match ($site->auto_update_core) {
            'minor' => $site->core_minor ? 'minor' : '',
            'all' => $site->core_latest || $site->core_minor ? 'latest' : '',
            default => '',
        };
        $plugins = $site->auto_update_plugins ? self::withUpdates($site->plugins) : [];
        $themes = $site->auto_update_themes ? self::withUpdates($site->themes) : [];
        if ($core === '' && $plugins === [] && $themes === []) {
            return;
        }
        self::update($site, $core, array_slice($plugins, 0, InputRules::MAX_WORDPRESS_UPDATES), array_slice($themes, 0, max(0, InputRules::MAX_WORDPRESS_UPDATES - count($plugins))), automatic: true);
    }

    /**
     * The names of the plugins or themes with an update.
     *
     * @param  list<array<string, mixed>>|null  $items
     * @return list<string>
     */
    public static function withUpdates(?array $items): array
    {
        return array_values(array_map(fn (array $i) => (string) $i['name'], array_filter($items ?? [], fn ($i) => is_array($i) && ($i['update'] ?? '') !== '' && Input::valid('wordpressSlug', $i['name'] ?? null))));
    }

    /** @param array<string, mixed> $result */
    private static function updated(AgentJob $job, WordPressSite $site, bool $succeeded, array $result): bool
    {
        $p = $job->payload ?? [];
        $update = [
            'jobId' => $job->id,
            'at' => now()->toIso8601String(),
            'automatic' => (bool) ($p['automatic'] ?? false),
            'core' => (string) ($p['core'] ?? ''),
            'plugins' => array_values((array) ($p['plugins'] ?? [])),
            'themes' => array_values((array) ($p['themes'] ?? [])),
        ];
        // The agent saves the restore point in step 2. A later step that fails puts it back.
        $saved = $succeeded || $job->step >= 2;
        $restorePoint = $saved ? [
            'at' => $job->started_at?->toIso8601String() ?? now()->toIso8601String(),
            'version' => $succeeded ? ($result['before']['version'] ?? $site->version) : $site->version,
            'database' => isset($p['database']),
        ] : $site->restore_point;
        if ($succeeded) {
            $before = is_array($result['before'] ?? null) ? $result['before'] : [];
            $update += ['state' => 'succeeded', 'from' => $before['version'] ?? $site->version, 'to' => $result['wordpress']['version'] ?? null];

            return self::apply($site, $result['wordpress'] ?? [], ['last_update' => $update, 'restore_point' => $restorePoint, 'error' => null]);
        }
        $error = (string) $job->error;
        $state = ! $saved ? 'failed' : (str_contains($error, 'could not put back the restore point') ? 'failed' : 'rolledBack');

        $public = JobResource::publicError($job);

        return $site->update(['last_update' => $update + ['state' => $state, 'error' => $public], 'restore_point' => $restorePoint, 'error' => $public]);
    }

    /**
     * Keeps the versions that the agent reports.
     *
     * @param  array<string, mixed>  $inventory
     * @param  array<string, mixed>  $changes
     */
    private static function apply(WordPressSite $site, mixed $inventory, array $changes = []): bool
    {
        if (is_array($inventory) && is_string($inventory['version'] ?? null)) {
            $changes += [
                'version' => Str::limit($inventory['version'], 40, ''),
                'core_minor' => is_string($inventory['coreMinor'] ?? null) ? Str::limit($inventory['coreMinor'], 40, '') : null,
                'core_latest' => is_string($inventory['coreLatest'] ?? null) ? Str::limit($inventory['coreLatest'], 40, '') : null,
                'plugins' => self::items($inventory['plugins'] ?? []),
                'themes' => self::items($inventory['themes'] ?? []),
                'site_url' => is_string($inventory['siteUrl'] ?? null) ? Str::limit($inventory['siteUrl'], 300, '') : null,
                'checked_at' => self::time($inventory['checkedAt'] ?? null) ?? now(),
            ];
        }

        return $site->update($changes);
    }

    /**
     * @return list<array{name: string, title: string, status: string, version: string, update: string|null}>
     */
    private static function items(mixed $items): array
    {
        $out = [];
        foreach (is_array($items) ? array_slice($items, 0, 500) : [] as $i) {
            if (! is_array($i) || ! is_string($i['name'] ?? null)) {
                continue;
            }
            $out[] = [
                'name' => Str::limit($i['name'], 100, ''),
                'title' => Str::limit((string) ($i['title'] ?? $i['name']), 200, ''),
                'status' => Str::limit((string) ($i['status'] ?? ''), 20, ''),
                'version' => Str::limit((string) ($i['version'] ?? ''), 40, ''),
                'update' => ($i['update'] ?? '') !== '' ? Str::limit((string) $i['update'], 40, '') : null,
            ];
        }

        return $out;
    }

    private static function time(mixed $value): ?Carbon
    {
        try {
            return is_string($value) && $value !== '' ? Carbon::parse($value) : null;
        } catch (\Throwable) {
            return null;
        }
    }

    /**
     * The automatic updates (FR-206): in the maintenance window of its server, each site gets one
     * check a day. When the check finds updates that the settings of the site allow, the panel
     * starts an update with a restore point. The scheduler runs this every 10 minutes.
     *
     * @return int The number of checks that started.
     */
    public static function startAutomaticChecks(Carbon $now): int
    {
        $started = 0;
        $sites = WordPressSite::with('website.server')->where('status', WordPressSite::READY)
            ->where(fn ($q) => $q->where('auto_update_core', '!=', 'off')->orWhere('auto_update_plugins', true)->orWhere('auto_update_themes', true))
            ->where(fn ($q) => $q->whereNull('auto_checked_at')->orWhere('auto_checked_at', '<', $now->copy()->subHours(20)))
            ->get();
        foreach ($sites as $site) {
            $website = $site->website;
            $server = $website?->server;
            if (! $website || $website->status !== WebsiteStatus::Running || ! $server || ! in_array($server->state, Server::WORKING, true)
                || ! InstallScheduledUpdates::inWindow($server->maintenance_window, $now)
                || $website->customer?->isSuspended()) {
                continue;
            }
            $busy = AgentJob::where('website_id', $website->id)
                ->whereIn('action', array_map(fn (AgentAction $a) => $a->value, self::ACTIONS))
                ->whereIn('state', [JobState::Queued->value, JobState::Running->value])->exists();
            if ($busy) {
                continue;
            }
            DB::transaction(function () use ($site, $website, $now) {
                $site->update(['auto_checked_at' => $now]);
                self::check($website, automatic: true);
            });
            $started++;
        }

        return $started;
    }

    /**
     * The site as the API gives it.
     *
     * @return array<string, mixed>
     */
    public static function resource(WordPressSite $site): array
    {
        return [
            'status' => $site->status,
            'version' => $site->version,
            'coreMinor' => $site->core_minor,
            'coreLatest' => $site->core_latest,
            'siteUrl' => $site->site_url,
            'adminUrl' => $site->site_url ? rtrim($site->site_url, '/').'/wp-admin/' : null,
            'adminUser' => $site->admin_user,
            'databaseId' => $site->database_id,
            'plugins' => $site->plugins ?? [],
            'themes' => $site->themes ?? [],
            'checkedAt' => $site->checked_at?->toIso8601String(),
            'autoUpdates' => ['core' => $site->auto_update_core, 'plugins' => $site->auto_update_plugins, 'themes' => $site->auto_update_themes],
            'lastUpdate' => $site->last_update,
            'restorePoint' => $site->restore_point,
            'error' => $site->error,
        ];
    }

    /** @return array<string, mixed> */
    public static function commandResource(WordPressCommand $c): array
    {
        return [
            'id' => $c->id,
            'command' => $c->command,
            'status' => $c->status,
            'exitCode' => $c->exit_code,
            'output' => $c->output,
            'ranAt' => $c->ran_at?->toIso8601String(),
            'createdAt' => $c->created_at?->toIso8601String(),
            'user' => $c->user?->name,
            'jobId' => $c->agent_job_id,
        ];
    }
}
