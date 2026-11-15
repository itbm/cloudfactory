<?php

namespace App\Services;

use App\Enums\AgentAction;
use App\Enums\JobState;
use App\Models\AgentJob;
use App\Models\Database;
use App\Models\DatabaseServer;
use App\Models\DatabaseTool;
use App\Models\DatabaseToolLogin;
use App\Models\Server;
use App\Models\User;
use App\Models\Website;
use App\Validation\Input;
use Illuminate\Support\Collection;
use Illuminate\Support\Facades\DB;
use Illuminate\Support\Str;
use Illuminate\Validation\ValidationException;

/**
 * The database tool (Adminer) of the customers.
 *
 * - Each customer has one Adminer container on the panel server. The agent operates it as a
 *   website: its own Unix user, its own gVisor sandbox, its own limits and its own port. The
 *   container has the image "adminer" of the release, and no files of the customer.
 * - The browser opens Adminer on the Adminer domain (by default "adminer." and the panel
 *   domain). Each container has its own path on that domain: a route of the edge proxy with a
 *   path prefix.
 * - The container connects only to the database servers of its customer. The panel puts it in
 *   the networks as a website that connects to each MariaDB and PostgreSQL database server of
 *   the customer (links). The firewall of the agent refuses all other connections, as for a
 *   website.
 * - Single sign-on: to open a database, the panel makes a temporary database user with access
 *   to that one database. The web panel sends the user and its password to Adminer in a form,
 *   so that the user types no password. The panel does not keep the password, and removes the
 *   database user when it expires (expire).
 */
class DatabaseTools
{
    /** The engines that Adminer can open. Adminer has no driver for Valkey. */
    public const ENGINES = ['mariadb', 'postgresql'];

    /** The Adminer driver of each engine. */
    public const DRIVERS = ['mariadb' => 'server', 'postgresql' => 'pgsql'];

    /** The port in the container of the image "adminer". */
    public const CONTAINER_PORT = 8080;

    /**
     * The limits of an Adminer container. Adminer is one PHP file, and it keeps its sessions
     * and the files of an import in /tmp, in memory.
     */
    public const LIMITS = [
        'vcpus' => 1.0,
        'memoryBytes' => 256 * 1024 ** 2,
        'diskBytes' => 256 * 1024 ** 2,
        'diskIoBytesPerSecond' => 20 * 1024 ** 2,
        'processes' => 64,
    ];

    /** The part of the name of a temporary database user after the prefix of the website. */
    public const USER_PART = 'adminer_';

    public function __construct(private JobDispatcher $jobs) {}

    /** The Adminer domain of the panel. */
    public static function domain(): string
    {
        $domain = config('cloudfactory.adminer.domain');

        return strtolower(is_string($domain) && $domain !== '' ? $domain : 'adminer.'.config('cloudfactory.panel_domain'));
    }

    /** The address of the Adminer of a customer. */
    public static function url(DatabaseTool $tool): string
    {
        $url = config('cloudfactory.adminer.url');

        return is_string($url) && $url !== '' ? rtrim($url, '/').$tool->path() : 'https://'.self::domain().$tool->path();
    }

    /**
     * The Adminer address of one sign-in: the address of the tool and a random part. Adminer
     * keeps its session cookie on this path only.
     */
    public static function loginUrl(DatabaseToolLogin $login): string
    {
        return self::url($login->tool).$login->address_key.'/';
    }

    /** The server of the Adminer containers: the panel server. */
    public static function panelServer(): ?Server
    {
        return Server::where('local', true)->first();
    }

    /**
     * The website fields of the container for the agent, as for CreateWebsite and SetRuntime.
     *
     * @return array<string, mixed>
     */
    public static function payload(DatabaseTool $tool): array
    {
        return [
            'website' => [
                'id' => $tool->id,
                'unixUser' => $tool->unix_user,
                'image' => config('cloudfactory.registry').'/adminer:'.$tool->image_tag,
                'imageDigest' => '',
                // The agent starts the image as it is, with its own web server and PHP.
                'runtime' => 'custom',
                'phpVersion' => '',
                'workerMode' => false,
                'extensions' => [],
                'phpIni' => (object) [],
                'limits' => self::LIMITS,
                'documentRoot' => 'public',
                'containerPort' => self::CONTAINER_PORT,
                'command' => [],
                'worker' => false,
                'domains' => [['name' => self::domain(), 'kind' => 'primary']],
                // The container connects to the database servers of the customer on the panel
                // server with the names of the engines, as a website does.
                'databaseInstance' => $tool->customer_id,
                'routePath' => $tool->path(),
            ],
        ];
    }

    /** The image tag of a new container: the version of the agent of the panel server. */
    private static function imageTag(Server $server): string
    {
        $version = (string) ($server->agent_version ?: config('cloudfactory.agent.latest_version'));

        return Input::valid('imageTag', $version) ? $version : (string) config('cloudfactory.agent.latest_version');
    }

    /**
     * Makes the container of a customer, or makes it again after a failure. A container with an
     * older image gets the image of the agent of the panel server.
     *
     * @return array{0: DatabaseTool, 1: ?AgentJob}
     */
    public function ensure(string $customerId): array
    {
        $server = self::panelServer();
        if (! $server) {
            throw ValidationException::withMessages(['database' => 'The panel has no panel server yet, so it cannot start Adminer.']);
        }
        [$tool, $action] = DB::transaction(function () use ($customerId, $server) {
            $tool = DatabaseTool::where('customer_id', $customerId)->lockForUpdate()->first();
            $tag = self::imageTag($server);
            if (! $tool) {
                $tool = DatabaseTool::create(['customer_id' => $customerId, 'server_id' => $server->id, 'unix_user' => self::unixUser(), 'image_tag' => $tag, 'status' => DatabaseTool::STARTING]);

                return [$tool, AgentAction::CreateWebsite];
            }
            if ($tool->status === DatabaseTool::FAILED) {
                $tool->update(['status' => DatabaseTool::STARTING, 'image_tag' => $tag]);

                return [$tool, AgentAction::CreateWebsite];
            }
            if ($tool->status === DatabaseTool::RUNNING && $tool->image_tag !== $tag) {
                $tool->update(['status' => DatabaseTool::STARTING, 'image_tag' => $tag]);

                return [$tool, AgentAction::SetRuntime];
            }

            return [$tool, null];
        });
        if ($action === null) {
            return [$tool, null];
        }
        // The networks of the new container: the tunnels to the database servers of the
        // customer on other servers.
        Mesh::refreshLater();
        $title = $action === AgentAction::CreateWebsite ? 'Start Adminer' : 'Update Adminer';
        $job = $this->jobs->dispatch($tool->server, $action, self::payload($tool) + ['databaseTool' => $tool->id], $title, ref: 'database-tool:'.$tool->id);

        return [$tool, $job];
    }

    /** A Unix user that no website and no other container has. */
    private static function unixUser(): string
    {
        do {
            $name = 'adminer_'.strtolower(Str::random(4));
        } while (Website::where('unix_user', $name)->exists() || DatabaseTool::where('unix_user', $name)->exists());

        return $name;
    }

    /**
     * Checks that the Adminer of the customer can open the database, and gives the host and
     * the port that Adminer connects to.
     *
     * - A database server on the panel server: the name of its engine and its port, as for a
     *   website on that server.
     * - A database server on a different server: its network name. The agents of the two
     *   servers must operate the networks.
     *
     * @throws ValidationException
     */
    public static function connection(Server $panel, DatabaseServer $server): string
    {
        if ($server->server_id === $panel->id) {
            return DatabaseServer::HOSTS[$server->engine].':'.$server->port;
        }
        $host = Networks::hostOf($server);
        if ($host === null || ! Networks::supports($panel) || ! Networks::supports($server->server)) {
            throw ValidationException::withMessages(['database' => "Adminer cannot connect to the database server {$server->name} yet: its server and the panel server must both have a current agent."]);
        }

        return $host;
    }

    /**
     * Opens a database in Adminer: makes a temporary database user with access to the
     * database. The answer has the fields of the Adminer sign-in form, with the password. The
     * panel does not keep the password.
     *
     * @return array{login: DatabaseToolLogin, form: array<string, string>, jobs: list<AgentJob>}
     *
     * @throws ValidationException
     */
    public function open(User $user, Database $database, string $privileges): array
    {
        $database->loadMissing(['website', 'databaseServer.server']);
        $website = $database->website;
        $server = $database->databaseServer;
        if (! in_array($database->engine, self::ENGINES, true)) {
            throw ValidationException::withMessages(['database' => 'Adminer can open MariaDB and PostgreSQL databases. It cannot open a Valkey database.']);
        }
        if (! $website || ! $server) {
            throw ValidationException::withMessages(['database' => 'This database has no database server.']);
        }
        if ($server->status !== DatabaseServer::RUNNING) {
            throw ValidationException::withMessages(['database' => "The database server {$server->name} does not operate now. Try again when its status is Running."]);
        }
        $panel = self::panelServer();
        if (! $panel) {
            throw ValidationException::withMessages(['database' => 'The panel has no panel server yet, so it cannot start Adminer.']);
        }
        $host = self::connection($panel, $server);

        [$tool, $toolJob] = $this->ensure($website->customer_id);
        $password = Input::databasePassword();
        $login = DatabaseToolLogin::create([
            'database_tool_id' => $tool->id,
            'database_id' => $database->id,
            'website_id' => $website->id,
            'unix_user' => $website->unix_user,
            'database_server_id' => $server->id,
            'user_id' => $user->id,
            'engine' => $database->engine,
            'database_name' => $database->name,
            'privileges' => $privileges,
            'name' => self::loginName($website),
            'address_key' => bin2hex(random_bytes(16)),
            'status' => DatabaseToolLogin::STARTING,
            'expires_at' => now()->addMinutes(max(1, (int) config('cloudfactory.adminer.minutes'))),
        ]);
        // The temporary user is a database user of the website, with access to one database.
        // The job goes to the agent of the server of the database server.
        $job = $this->jobs->dispatch($server->server, AgentAction::SetDatabaseUser, [
            'websiteId' => $website->id,
            'unixUser' => $website->unix_user,
            'engine' => $database->engine,
            'name' => $login->name,
            'password' => $password,
            'access' => 'website',
            'create' => true,
            'grants' => [['database' => $database->name, 'privileges' => $privileges]],
            'websiteDatabases' => $website->databases()->where(['engine' => $database->engine, 'database_server_id' => $server->id])->orderBy('name')->pluck('name')->all(),
            'databaseServer' => $server->forAgent(),
            'databaseToolLogin' => $login->id,
        ], "Make Adminer user {$login->name}", $website, ref: 'database-tool-login:'.$login->id, databaseServer: $server);

        return [
            'login' => $login,
            'form' => [
                'driver' => self::DRIVERS[$database->engine],
                'server' => $host,
                'username' => $login->name,
                'password' => $password,
                'db' => $database->name,
            ],
            'jobs' => array_values(array_filter([$toolJob, $job])),
        ];
    }

    /** A name for a temporary database user of a website that no database user has. */
    private static function loginName(Website $website): string
    {
        do {
            $name = DatabaseNames::prefix($website).self::USER_PART.strtolower(Str::random(8));
        } while (DatabaseToolLogin::where('name', $name)->exists() || $website->databaseUsers()->where('name', $name)->exists());

        return $name;
    }

    /**
     * The state of a login for the web panel: "starting" until the container and the database
     * user are ready, then "ready", or "failed".
     */
    public static function state(DatabaseToolLogin $login): string
    {
        $login->loadMissing('tool');
        if ($login->status === DatabaseToolLogin::FAILED || $login->tool->status === DatabaseTool::FAILED) {
            return 'failed';
        }
        if ($login->status === DatabaseToolLogin::REMOVING || $login->expires_at->isPast()) {
            return 'expired';
        }

        return $login->status === DatabaseToolLogin::READY && $login->tool->status === DatabaseTool::RUNNING ? 'ready' : 'starting';
    }

    /**
     * Removes the temporary database users that expired, and those of a database that is gone.
     * The scheduler runs this each minute.
     */
    public function expire(): int
    {
        $count = 0;
        $logins = DatabaseToolLogin::with(['databaseServer.server', 'tool'])
            ->whereIn('status', [DatabaseToolLogin::READY, DatabaseToolLogin::STARTING])
            ->where(fn ($q) => $q->where('expires_at', '<=', now())->orWhereNull('database_id'))
            ->get();
        foreach ($logins as $login) {
            // A job that makes the user is still open: remove the user after it.
            if ($login->status === DatabaseToolLogin::STARTING && AgentJob::where('ref', 'database-tool-login:'.$login->id)->whereIn('state', [JobState::Queued, JobState::Running])->exists()) {
                continue;
            }
            $server = $login->databaseServer;
            if ($server->status === DatabaseServer::REMOVING || $server->status === DatabaseServer::MOVING) {
                continue;
            }
            $login->update(['status' => DatabaseToolLogin::REMOVING]);
            $website = $login->website_id ? Website::find($login->website_id) : null;
            $this->jobs->dispatch($server->server, AgentAction::DeleteDatabaseUser, [
                'websiteId' => $login->website_id ?? $login->tool->id,
                'unixUser' => $login->unix_user,
                'engine' => $login->engine,
                'name' => $login->name,
                'websiteDatabases' => [],
                'databaseServer' => $server->id,
                // The agent of a different server has no record of the website.
                'customer' => $login->tool->customer_id,
                'databaseToolLogin' => $login->id,
            ], "Remove Adminer user {$login->name}", $website, ref: 'database-tool-login:'.$login->id, databaseServer: $server);
            $count++;
        }

        return $count;
    }

    /**
     * The result of a job of a container or of a temporary database user.
     */
    public static function jobFinished(AgentJob $job, bool $succeeded): void
    {
        $p = $job->payload ?? [];
        if (isset($p['databaseTool'])) {
            DatabaseTool::whereKey($p['databaseTool'])->update(['status' => $succeeded ? DatabaseTool::RUNNING : DatabaseTool::FAILED]);
            if (! $succeeded) {
                // A container that the agent did not make has no networks.
                Mesh::refreshLater();
            }
        }
        if (! isset($p['databaseToolLogin'])) {
            return;
        }
        $login = DatabaseToolLogin::find($p['databaseToolLogin']);
        if (! $login) {
            return;
        }
        if ($job->action === AgentAction::DeleteDatabaseUser) {
            // The user is gone. A failed removal tries again at the next run of expire.
            $succeeded ? $login->delete() : $login->update(['status' => DatabaseToolLogin::READY]);

            return;
        }
        if ($job->action === AgentAction::SetDatabaseUser) {
            $login->update(['status' => $succeeded ? DatabaseToolLogin::READY : DatabaseToolLogin::FAILED]);
            if (! $succeeded) {
                // The agent made no user, so there is nothing to remove.
                $login->delete();
            }
        }
    }

    /**
     * The links of the containers for the networks (Networks::links): each container connects
     * to each MariaDB and PostgreSQL database server of its customer, on each server. A
     * database server with the name of an engine has no network name, so it is not in the
     * list.
     *
     * @return Collection<int, object>
     */
    public static function links(): Collection
    {
        return DB::table('database_tools')
            ->join('database_servers', 'database_servers.customer_id', '=', 'database_tools.customer_id')
            ->where('database_tools.status', '!=', DatabaseTool::FAILED)
            ->whereIn('database_servers.engine', self::ENGINES)
            ->where('database_servers.status', '!=', DatabaseServer::REMOVING)
            ->whereNotIn('database_servers.name', Networks::RESERVED)
            ->select([
                'database_tools.id as website_id', 'database_tools.server_id as website_server_id',
                'database_servers.id as member_id', 'database_servers.server_id as server_id',
                'database_servers.name', 'database_servers.engine', 'database_servers.mesh_address',
            ])
            ->orderBy('database_tools.id')->orderBy('database_servers.id')
            ->get()
            ->map(fn (object $row) => (object) [
                'website_id' => $row->website_id, 'website_server_id' => $row->website_server_id, 'kind' => 'database',
                'member_id' => $row->member_id, 'server_id' => $row->server_id, 'name' => $row->name,
                'port' => Networks::portOf($row->engine), 'mesh_address' => $row->mesh_address,
            ]);
    }
}
