<?php

namespace App\Services\Plugins;

use App\Enums\AgentAction;
use App\Models\AgentJob;
use App\Models\DatabaseTool;
use App\Models\Domain;
use App\Models\Plugin;
use App\Models\Server;
use App\Models\Website;
use App\Services\DomainPolicy;
use App\Services\JobDispatcher;
use Illuminate\Support\Str;
use Illuminate\Validation\ValidationException;

/**
 * Plugins that the panel runs. The manifest gives a container image with a digest, so the
 * signature of the manifest also covers the image. The agent of the selected server operates the
 * container as a website:
 *
 * - its own Unix user, its own rootless Podman and its own gVisor sandbox, with fixed limits;
 * - no files of a customer, and no database servers: its database instance is the plugin
 *   itself, so the firewall lets it connect to no database server;
 * - a route of the edge proxy for its host name, with a certificate;
 * - the environment variables CF_PANEL, CF_TOKEN and CF_WEBHOOK_SECRET. The token has only the
 *   scopes of the manifest, as for a plugin outside the panel.
 *
 * The container never gets root, the socket of the agent or the root helper.
 */
class Hosting
{
    /** The limits of a plugin container. */
    public const LIMITS = [
        'vcpus' => 1.0,
        'memoryBytes' => 512 * 1024 ** 2,
        'diskBytes' => 1024 ** 3,
        'diskIoBytesPerSecond' => 20 * 1024 ** 2,
        'processes' => 128,
    ];

    public const STARTING = 'starting';

    public const RUNNING = 'running';

    public const STOPPED = 'stopped';

    public const FAILED = 'failed';

    public const REMOVING = 'removing';

    public function __construct(private JobDispatcher $jobs) {}

    /** The domain of the host names of the plugins. */
    public static function domain(): string
    {
        $domain = config('cloudfactory.plugins.domain');

        return strtolower(is_string($domain) && $domain !== '' ? $domain : 'plugins.'.config('cloudfactory.panel_domain'));
    }

    /**
     * Checks the server and the host name of a new plugin container.
     *
     * @return array{0: Server, 1: string}
     *
     * @throws ValidationException
     */
    public static function place(array $manifest, ?string $serverId, ?string $host): array
    {
        if (empty($manifest['image'])) {
            throw ValidationException::withMessages(['hosting' => 'This manifest has no image, so the panel cannot run the plugin. Run it on your own host, and give its address.']);
        }
        $server = $serverId
            ? Server::find($serverId)
            : (Server::where('local', true)->first() ?? Server::whereJsonContains('roles', 'web')->whereIn('state', Server::WORKING)->first());
        if (! $server || ! in_array('web', $server->roles ?? [], true) && ! $server->local) {
            throw ValidationException::withMessages(['serverId' => 'Select a server with the web role, or the panel server.']);
        }
        $host = strtolower(trim($host ?? '') ?: $manifest['slug'].'.'.self::domain());
        if (! preg_match('/^(?=.{4,253}$)([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]{2,63}$/', $host)) {
            throw ValidationException::withMessages(['host' => 'Give a host name such as uptime.plugins.example.com.']);
        }
        $policy = new DomainPolicy;
        if ($host === strtolower((string) config('cloudfactory.panel_domain')) || in_array($host, $policy->brandDomains(), true) || Server::where('mail_hostname', $host)->exists()) {
            throw ValidationException::withMessages(['host' => 'The panel or a server uses this host name.']);
        }
        if (Website::where('name', $host)->exists() || Domain::where('name', $host)->exists() || Plugin::where('host', $host)->exists()) {
            throw ValidationException::withMessages(['host' => 'A website or a plugin has this host name.']);
        }

        return [$server, $host];
    }

    /** A Unix user that no website and no other container has. */
    public static function unixUser(): string
    {
        do {
            $name = 'plugin_'.strtolower(Str::random(4));
        } while (Website::where('unix_user', $name)->exists() || DatabaseTool::where('unix_user', $name)->exists() || Plugin::where('unix_user', $name)->exists());

        return $name;
    }

    /**
     * The website fields of the container for the agent, as for CreateWebsite and SetRuntime.
     *
     * @return array<string, mixed>
     */
    public static function payload(Plugin $plugin): array
    {
        $image = $plugin->image();

        return [
            'website' => [
                'id' => $plugin->id,
                'unixUser' => $plugin->unix_user,
                'image' => $image['ref'],
                'imageDigest' => $image['digest'],
                'runtime' => 'custom',
                'phpVersion' => '',
                'workerMode' => false,
                'extensions' => [],
                'phpIni' => (object) [],
                'limits' => self::LIMITS,
                'documentRoot' => 'public',
                'containerPort' => $image['port'],
                'command' => [],
                'worker' => false,
                'domains' => [['name' => $plugin->host, 'kind' => 'primary']],
                // Its own database instance: the container connects to no database server.
                'databaseInstance' => $plugin->id,
                'routePath' => '',
                'environment' => [
                    'CF_PANEL' => rtrim((string) config('app.url'), '/'),
                    'CF_TOKEN' => (string) $plugin->hosted_token,
                    'CF_WEBHOOK_SECRET' => (string) $plugin->webhook_secret,
                ],
            ],
            'hostedPlugin' => $plugin->id,
        ];
    }

    /** Makes the container, or makes it again with a new image or new credentials. */
    public function start(Plugin $plugin, bool $again): AgentJob
    {
        $plugin->update(['container_status' => self::STARTING]);
        $action = $again ? AgentAction::SetRuntime : AgentAction::CreateWebsite;

        return $this->jobs->dispatch($plugin->server, $action, self::payload($plugin), ($again ? 'Update plugin ' : 'Start plugin ').$plugin->name, ref: 'plugin:'.$plugin->id);
    }

    public function power(Plugin $plugin, bool $on): AgentJob
    {
        $plugin->update(['container_status' => self::STARTING]);

        return $this->jobs->dispatch($plugin->server, AgentAction::SetWebsitePower, [
            'websiteId' => $plugin->id, 'unixUser' => $plugin->unix_user, 'power' => $on ? 'start' : 'stop', 'hostedPlugin' => $plugin->id,
        ], ($on ? 'Start plugin ' : 'Stop plugin ').$plugin->name, ref: 'plugin:'.$plugin->id);
    }

    public function remove(Plugin $plugin): AgentJob
    {
        return $this->jobs->dispatch($plugin->server, AgentAction::DeleteWebsite, [
            'websiteId' => $plugin->id, 'unixUser' => $plugin->unix_user, 'databases' => [], 'databaseUsers' => [],
        ], 'Remove plugin '.$plugin->name, ref: 'plugin:'.$plugin->id);
    }

    /** Records the state of the container when a job of a plugin is complete (JobOutcome). */
    public static function jobFinished(AgentJob $job, bool $succeeded): void
    {
        $id = $job->payload['hostedPlugin'] ?? null;
        if (! is_string($id)) {
            return;
        }
        $state = match (true) {
            ! $succeeded => self::FAILED,
            $job->action === AgentAction::SetWebsitePower && ($job->payload['power'] ?? '') === 'stop' => self::STOPPED,
            default => self::RUNNING,
        };
        Plugin::whereKey($id)->update(['container_status' => $state]);
    }
}
