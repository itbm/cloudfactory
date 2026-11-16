<?php

namespace App\Http\Controllers\Api;

use App\Enums\AgentAction;
use App\Enums\DomainKind;
use App\Enums\Role;
use App\Enums\WebsiteStatus;
use App\Http\Controllers\Controller;
use App\Http\Resources\JobResource;
use App\Http\Resources\WebsiteResource;
use App\Models\AgentJob;
use App\Models\Customer;
use App\Models\Database;
use App\Models\DatabaseServer;
use App\Models\DatabaseTool;
use App\Models\Image;
use App\Models\Plan;
use App\Models\Plugin;
use App\Models\Server;
use App\Models\User;
use App\Models\Website;
use App\Services\Audit;
use App\Services\DatabaseNames;
use App\Services\DatabaseServers;
use App\Services\Dns\Zones;
use App\Services\DomainPolicy;
use App\Services\JobDispatcher;
use App\Services\Networks;
use App\Services\Plugins\Hooks;
use App\Services\WebsiteRemoval;
use App\Validation\Input;
use App\Validation\InputRules;
use Illuminate\Http\JsonResponse;
use Illuminate\Http\Request;
use Illuminate\Http\Resources\Json\AnonymousResourceCollection;
use Illuminate\Support\Facades\DB;
use Illuminate\Support\Str;
use Illuminate\Validation\Rule;
use Illuminate\Validation\ValidationException;

/** FR-201 to FR-205: websites. */
class WebsiteController extends Controller
{
    use Concerns;

    /**
     * A lower-case domain name. Audit finding R5: the agent has the same rule
     * (agent/internal/validate): the two have it from schema/input-rules.json. Each label
     * starts and ends with a letter or a digit, and the last label is not a number, so that an
     * IP address is not a domain name.
     */
    public const DOMAIN = InputRules::DOMAIN;

    /**
     * The short name that a user types for a private website or a worker: lower-case letters,
     * digits and "-". The panel makes the Unix user and the network name from it.
     */
    public const SHORT_NAME = '/^[a-z]([a-z0-9-]{0,38}[a-z0-9])?$/';

    /** The PHP extensions of a new website, when the plan has them. The owner can change them. */
    public const DEFAULT_EXTENSIONS = ['opcache', 'intl', 'gd', 'zip', 'mysqli', 'pdo_mysql', 'pdo_pgsql'];

    public function __construct(private JobDispatcher $jobs, private DomainPolicy $domains) {}

    public function index(Request $request): AnonymousResourceCollection
    {
        $request->validate(['status' => ['nullable', Rule::enum(WebsiteStatus::class)], 'q' => ['nullable', 'string', 'max:200']]);
        $websites = $this->visibleWebsites($request)
            ->with(['image', 'domains', 'server'])
            ->when($request->query('status'), fn ($q, $s) => $q->where('status', $s))
            // whereLike ignores the case of the letters in each database engine.
            ->when($request->query('q'), fn ($q, $s) => $q->whereLike('name', '%'.$s.'%'))
            ->orderBy('name')
            ->paginate(self::perPage($request));

        return WebsiteResource::collection($websites);
    }

    public function show(Request $request, string $id): WebsiteResource
    {
        return new WebsiteResource($this->findWebsite($request, $id));
    }

    public function store(Request $request): JsonResponse
    {
        $kind = $request->input('kind', Website::PUBLIC);
        // A Node.js or Python website runs a start command, as a worker does.
        $app = is_string($request->input('imageId')) && Image::find($request->input('imageId'))?->isApp();
        $data = $request->validate([
            'kind' => ['sometimes', Rule::in(Website::KINDS)],
            // A website has the name of its first domain. A private website and a worker have
            // no domain: the user types a short name, and the panel makes the network name.
            'name' => ['required', 'string', 'lowercase', 'regex:'.($kind === Website::PUBLIC ? self::DOMAIN : self::SHORT_NAME)],
            'command' => [Rule::requiredIf($kind === Website::WORKER), Rule::prohibitedIf($kind !== Website::WORKER && ! $app), 'string', Input::rule('commandLine')],
            // The install command of a Node.js or Python website. Empty: no install step.
            'installCommand' => [Rule::prohibitedIf(! $app), 'nullable', 'string', Input::rule('commandLine')],
            'application' => ['nullable', 'string', 'max:100'],
            'imageId' => ['required', 'exists:images,id'],
            'planId' => ['nullable', 'exists:plans,id'],
            'serverId' => ['nullable', 'exists:servers,id'],
            'customerId' => ['nullable', 'exists:customers,id'],
        ]);
        $user = $request->user();
        $customerId = $data['customerId'] ?? $user->customer_id;
        if (! $customerId || ! $user->canSeeCustomer($customerId)) {
            throw ValidationException::withMessages(['customerId' => 'Select a customer that you control.']);
        }
        $customer = Customer::with('plan')->findOrFail($customerId);
        $this->refuseSuspended($customer);
        // CF-07: only an administrator, or the reseller of the customer, selects a different plan.
        $planId = $data['planId'] ?? $customer->plan_id;
        if ($planId !== $customer->plan_id && ! self::canChoosePlan($user, $customer, $planId)) {
            throw ValidationException::withMessages(['planId' => 'Only an administrator or your reseller can select the plan.']);
        }

        $image = Image::findOrFail($data['imageId']);
        // Adversarial review, finding X1-12: the images of the panel itself are not for websites.
        if ($image->family === 'edge') {
            throw ValidationException::withMessages(['imageId' => 'Select an image for websites.']);
        }
        if (! $user->isAdministrator() && ($image->custom || isset($data['serverId']))) {
            throw ValidationException::withMessages($image->custom
                ? ['imageId' => 'Select an official image.']
                : ['serverId' => 'Only an administrator can select the server.']);
        }
        $plan = Plan::findOrFail($planId);
        $server = isset($data['serverId'])
            ? Server::findOrFail($data['serverId'])
            // A healthy server first. A degraded server can also get a website: its agent answers.
            // A server that the panel cannot connect to comes last: its jobs fail.
            : Server::whereJsonContains('roles', 'web')->whereIn('state', Server::WORKING)->withCount('websites')
                ->orderByRaw('case when connection_error is null then 0 else 1 end')
                ->orderByRaw("case when state = 'healthy' then 0 else 1 end")->orderBy('websites_count')->first();
        if (! $server || ! in_array('web', $server->roles, true)) {
            throw ValidationException::withMessages(['serverId' => 'Select a server with the web role.']);
        }
        $unixUser = self::unixUser($data['name']);
        $domain = null;
        if ($kind === Website::PUBLIC) {
            $proved = DomainController::checkName($this->domains, $data['name'], $customer->id, $request);
            $domain = DomainController::newDomain($this->domains, $request, $customer->id, $data['name'], DomainKind::Primary, $proved);
        } else {
            // Only the websites of its networks connect to a private website, and a worker
            // needs the start command: the two need an agent with these functions.
            if (! Networks::supportsWebsites($server)) {
                throw ValidationException::withMessages(['kind' => "The agent on the server {$server->name} did not report that it can operate a ".Website::KIND_NAMES[$kind].'. Update the agent of the server, then try again.']);
            }
            // The name of the website is its network name, for example "queue-a1b2.internal".
            // It is not a domain name of the internet, and no two websites have the same one.
            $data['name'] = Networks::websiteName($unixUser).Networks::DOMAIN;
        }
        if (Website::where('name', $data['name'])->exists()) {
            throw ValidationException::withMessages(['name' => 'The name has already been taken.']);
        }
        // The plugins with the before hook can refuse the new website.
        app(Hooks::class)->check('website.creating', [
            'name' => $data['name'], 'kind' => $kind, 'customerId' => $customer->id, 'customerName' => $customer->name,
            'planId' => $plan->id, 'imageId' => $image->id, 'serverId' => $server->id,
        ], $user);

        $website = DB::transaction(function () use ($data, $kind, $unixUser, $customer, $image, $plan, $server, $domain) {
            self::checkWebsiteLimit($customer);
            // The usual commands of the family, when the user gave none.
            $defaults = Image::APP_DEFAULTS[$image->family] ?? null;
            $install = array_key_exists('installCommand', $data) ? trim((string) $data['installCommand']) : ($defaults['install'] ?? '');
            $website = Website::create([
                'name' => $data['name'],
                'kind' => $kind,
                'command' => isset($data['command']) ? trim($data['command']) : ($defaults['command'] ?? null),
                'install_command' => $defaults && $install !== '' ? $install : null,
                // A Node.js or Python website runs from its application directory: /app.
                ...($defaults ? ['document_root' => '.'] : []),
                'application' => $data['application'] ?? '',
                'customer_id' => $customer->id,
                'server_id' => $server->id,
                'plan_id' => $plan->id,
                'image_id' => $image->id,
                'unix_user' => $unixUser,
                'status' => WebsiteStatus::Starting,
                // The database drivers are on from the start: most PHP applications need one.
                'extensions' => $image->isPhp() ? array_values(array_intersect(self::DEFAULT_EXTENSIONS, $plan->php_extensions)) : [],
                'php_ini' => $image->isPhp() ? ['memory_limit' => '256M', 'upload_max_filesize' => '64M', 'max_execution_time' => '60'] : [],
                'limits' => $plan->limits,
            ]);
            if ($domain) {
                $website->domains()->create($domain);
            }

            return $website;
        });

        $job = $this->jobs->dispatch($server, AgentAction::CreateWebsite, JobDispatcher::websitePayload($website), 'Create '.Website::KIND_NAMES[$kind]." {$website->name}", $website);
        // A DNS zone of the customer with the name in it gets the address records of the website.
        if ($website->isPublic()) {
            app(Zones::class)->websiteDomainAdded($website, $website->name);
        }
        Audit::record($request, 'Created website', $website->name, $customer->id);

        return response()->json(['website' => new WebsiteResource($website->fresh(['image', 'domains'])), 'job' => new JobResource($job)], 202);
    }

    public function update(Request $request, string $id): JsonResponse
    {
        $website = $this->findWebsite($request, $id);
        $data = $request->validate([
            'application' => ['sometimes', 'string', 'max:100'],
            'planId' => ['sometimes', 'exists:plans,id'],
            // FR-202: only administrators set limits directly.
            'limits' => ['sometimes', 'array'],
            'limits.vcpus' => ['required_with:limits', 'numeric', 'gt:0'],
            'limits.memoryBytes' => ['required_with:limits', 'integer', 'gt:0'],
            'limits.diskBytes' => ['required_with:limits', 'integer', 'gt:0'],
            'limits.diskIoBytesPerSecond' => ['required_with:limits', 'integer', 'gt:0'],
            'limits.processes' => ['required_with:limits', 'integer', 'gt:0'],
            // The traffic of each website in a calendar month. 0 or none is no limit. The panel
            // shows the traffic and alerts near the limit. It does not stop the website.
            'limits.bandwidthBytes' => ['sometimes', 'integer', 'min:0'],
            'managedByTerraform' => ['sometimes', 'boolean'],
        ]);
        if (isset($data['limits']) && ! $request->user()->isAdministrator()) {
            return response()->json(['message' => 'Only administrators can set resource limits.'], 403);
        }
        // CF-07: a website owner cannot move their website to a different plan.
        if (isset($data['planId']) && $data['planId'] !== $website->plan_id && ! self::canChoosePlan($request->user(), $website->customer, $data['planId'])) {
            return response()->json(['message' => 'Only an administrator or your reseller can change the plan.'], 403);
        }

        $changes = array_filter([
            'application' => $data['application'] ?? null,
            'managed_by_terraform' => $data['managedByTerraform'] ?? null,
        ], fn ($v) => $v !== null);
        $plan = null;
        if (isset($data['planId']) && $data['planId'] !== $website->plan_id) {
            // The plan of a website does not limit its databases: the plan of each database
            // server gives the number of the databases in it.
            $plan = Plan::findOrFail($data['planId']);
            $changes['plan_id'] = $plan->id;
        }
        if (isset($data['limits'])) {
            $changes['limits'] = $data['limits'];
        }
        $website->update($changes);

        $job = null;
        if ($plan) {
            // Batch 1, finding 8: the limits and the extensions of the new plan reach the server.
            PlanController::applyPlan($this->jobs, $website, $plan, ! isset($data['limits']));
            $job = $website->jobs()->first();
        }
        if (isset($data['limits'])) {
            $job = $this->jobs->dispatch($website->server, AgentAction::SetLimits, ['websiteId' => $website->id, 'unixUser' => $website->unix_user, 'limits' => $website->limits], 'Apply resource limits', $website);
        }
        Audit::record($request, 'Changed website', $website->name, $website->customer_id);

        return response()->json(['website' => new WebsiteResource($website->fresh(['image', 'domains'])), 'job' => $job ? new JobResource($job) : null]);
    }

    public function destroy(Request $request, string $id): JsonResponse
    {
        $website = $this->findWebsite($request, $id);
        app(Hooks::class)->check('website.removing', ['id' => $website->id, 'name' => $website->name, 'customerId' => $website->customer_id], $request->user());
        // A website with a database on a different server gets a job for that server first
        // (WebsiteRemoval).
        $job = app(WebsiteRemoval::class)->start($website);
        Audit::record($request, 'Removed website', $website->name, $website->customer_id);

        return response()->json(['job' => new JobResource($job)], 202);
    }

    /**
     * Makes a website again after its first job failed, for example because the server could
     * not pull the image. Before, the only way was to delete the website and to make it again.
     * The job of the agent can run again: each step keeps what a previous run made.
     */
    public function retry(Request $request, string $id): JsonResponse
    {
        $website = $this->findWebsite($request, $id);
        $this->refuseSuspended($website->customer);
        $job = self::createAgain($this->jobs, $website);
        Audit::record($request, 'Created website again', $website->name, $website->customer_id);

        return response()->json(['website' => new WebsiteResource($website->fresh(['image', 'domains'])), 'job' => new JobResource($job)], 202);
    }

    /**
     * Starts the job that makes a failed website again.
     *
     * @throws ValidationException
     */
    public static function createAgain(JobDispatcher $jobs, Website $website): AgentJob
    {
        if ($website->status !== WebsiteStatus::Failed) {
            throw ValidationException::withMessages(['website' => 'The panel made this website. Only a website that the panel could not make can be made again.']);
        }
        if ($website->staging_of) {
            // The job of a staging copy also copies the databases, with passwords that the panel
            // does not keep.
            throw ValidationException::withMessages(['website' => 'The panel could not make this staging copy. Delete it, and make the staging copy again.']);
        }
        $website->update(['status' => WebsiteStatus::Starting]);

        return $jobs->dispatch($website->server, AgentAction::CreateWebsite, JobDispatcher::websitePayload($website->fresh(['image', 'domains'])), "Create website {$website->name}", $website);
    }

    /** A website that the panel could not make has no container on the server. */
    public static function refuseFailed(Website $website): void
    {
        if ($website->status === WebsiteStatus::Failed) {
            throw ValidationException::withMessages(['website' => 'The panel could not make this website. Use "Try again" on the website first, or delete the website.']);
        }
    }

    /** FR-204: start, stop and restart. */
    public function power(Request $request, string $id): JsonResponse
    {
        $website = $this->findWebsite($request, $id);
        $data = $request->validate(['action' => ['required', Rule::in(['start', 'stop', 'restart'])]]);
        self::refuseFailed($website);
        if ($data['action'] !== 'stop') {
            $this->refuseSuspended($website->customer);
        }
        $website->update(['status' => match ($data['action']) {
            'start' => WebsiteStatus::Starting,
            'stop' => WebsiteStatus::Stopping,
            'restart' => $website->status,
        }]);
        $job = $this->jobs->dispatch($website->server, AgentAction::SetWebsitePower, ['websiteId' => $website->id, 'unixUser' => $website->unix_user, 'power' => $data['action']], ucfirst($data['action'])." {$website->name}", $website);
        Audit::record($request, ucfirst($data['action']).' website', $website->name, $website->customer_id);

        return response()->json(['job' => new JobResource($job)], 202);
    }

    /** FR-205: make a staging copy in one action. */
    public function staging(Request $request, string $id): JsonResponse
    {
        $source = $this->findWebsite($request, $id);
        $this->refuseSuspended($source->customer);
        if (! $source->isPublic()) {
            throw ValidationException::withMessages(['website' => 'The panel makes a staging copy only of a website with a domain.']);
        }
        // Adversarial review, finding X1-03: the copy has new databases, and the answer has
        // their passwords.
        abort_if($source->databases()->exists() && ! $request->user()->tokenCan('databases:write'), 403,
            'This website has databases. A staging copy needs a token with the scope databases:write.');
        if ($source->status === WebsiteStatus::Failed) {
            throw ValidationException::withMessages(['website' => 'This website failed. Repair the website before you make a staging copy.']);
        }
        $name = 'staging.'.preg_replace('/^www\./', '', $source->name);
        if (! preg_match(self::DOMAIN, $name)) {
            throw ValidationException::withMessages(['name' => "The name {$name} of the staging copy is not a valid domain."]);
        }
        if (Website::where('name', $name)->exists()) {
            throw ValidationException::withMessages(['name' => "{$name} exists."]);
        }
        // The agent of the server of the website makes the copy. It cannot copy a database
        // that is in a database server on a different server.
        if ($source->databases()->whereHas('databaseServer', fn ($q) => $q->where('server_id', '!=', $source->server_id))->exists()) {
            throw ValidationException::withMessages(['website' => 'This website has a database in a database server on a different server. The panel cannot make a staging copy of such a website yet.']);
        }
        $proved = DomainController::checkName($this->domains, $name, $source->customer_id, $request);
        $domain = DomainController::newDomain($this->domains, $request, $source->customer_id, $name, DomainKind::Primary, $proved);
        // The copy and its databases are one transaction: when a database server has no space
        // for the copies, the panel makes nothing.
        [$copy, $copies, $users, $credentials, $servers] = DB::transaction(function () use ($source, $name, $domain) {
            // CF-09: a staging copy is a website, so the plan limit applies.
            self::checkWebsiteLimit($source->customer);
            // Audit finding R7: the copy has no SFTP account on the server, so it does not get
            // the SFTP settings of the source. The owner turns SFTP on for the copy.
            // replicate() also gives the copy the loaded relations of the source. With the domains
            // of the source, the agent got the copy with the names of the live website: the edge
            // proxy made a route for the live names to the copy, and it refused the certificate
            // rule of a copy with a local name. Thus the copy starts with no relations.
            // Adversarial review, finding A2-11: the copy does not get the counters and the
            // flags of the source.
            $copy = $source->replicate(['usage', 'managed_by_terraform', 'sftp_enabled', 'sftp_keys', 'bandwidth_month', 'bandwidth_bytes',
                'stopped_by_suspension', 'sftp_closed_by_suspension', 'previous_image_id', 'previous_image_tag'])->withoutRelations();
            $copy->fill([
                'name' => $name,
                'application' => "Staging copy of {$source->name}",
                'unix_user' => self::unixUser($name),
                'status' => WebsiteStatus::Starting,
                'staging_of' => $source->id,
                'managed_by_terraform' => false,
            ])->save();
            $copy->domains()->create($domain);

            return [$copy, ...$this->copyDatabases($source, $copy)];
        });
        $payload = JobDispatcher::websitePayload($copy) + [
            'copyFrom' => $source->id,
            'copyFromUnixUser' => $source->unix_user,
            // FR-205: the copy has its own databases and its own user. It never uses the
            // databases of the live website.
            'copyDatabases' => $copies,
            'copyUsers' => $users,
            // The database servers of the copies.
            'databaseServers' => array_map(fn (DatabaseServer $s) => $s->forAgent(), $servers),
        ];
        $job = $this->jobs->dispatch($copy->server, AgentAction::CreateWebsite, $payload, "Make staging copy of {$source->name}", $copy);
        // The passwords are in the payload for the agent only. The panel removes them when the job ends.
        Audit::record($request, 'Made staging copy', "{$source->name} → {$name}", $source->customer_id);

        return response()->json([
            'website' => new WebsiteResource($copy->fresh(['image', 'domains', 'server'])),
            // Shown once. The application of the copy needs these values in place of the live ones.
            'databaseCredentials' => $credentials,
            'job' => new JobResource($job),
        ], 202);
    }

    /**
     * FR-205: each database of the source gets a copy with the prefix of the copy. Each copy is
     * in the database server of its source database, and each database server gets one new
     * user with read-write access to the copies in it.
     *
     * The caller runs this in a transaction.
     *
     * @return array{0: list<array>, 1: list<array>, 2: list<array>, 3: list<DatabaseServer>}
     */
    private function copyDatabases(Website $source, Website $copy): array
    {
        $copies = $users = $credentials = $servers = [];
        $from = DatabaseNames::prefix($source);
        $to = DatabaseNames::prefix($copy);
        $connection = fn (string $engine, DatabaseServer $server) => [
            'host' => DatabaseServer::HOSTS[$engine],
            'port' => $server->port,
            'databaseServerId' => $server->id,
        ];
        $all = $source->databases()->with('databaseServer')->orderBy('name')->get();
        // The plan of each database server gives the number of its databases.
        foreach ($all->groupBy('database_server_id') as $databases) {
            $servers[] = $databases[0]->databaseServer;
            DatabaseServers::checkSpace($databases[0]->databaseServer, $databases->count());
        }
        foreach ($all->whereIn('engine', Database::SQL_ENGINES)->groupBy('engine') as $engine => $ofEngine) {
            $number = 0;
            // The database servers in the sequence of their ports: the first database server of
            // an engine has the user "app". The next ones have "app2", "app3", and so on.
            foreach ($ofEngine->groupBy('database_server_id')->sortBy(fn ($databases) => $databases[0]->databaseServer->port) as $databases) {
                $server = $databases[0]->databaseServer;
                $number++;
                $user = $copy->databaseUsers()->create(['engine' => $engine, 'name' => $to.'app'.($number > 1 ? $number : ''), 'access' => 'website', 'database_server_id' => $server->id]);
                $password = Input::databasePassword();
                $names = [];
                foreach ($databases as $database) {
                    $name = $to.substr($database->name, strlen($from));
                    $new = $copy->databases()->create(['engine' => $engine, 'name' => $name, 'database_server_id' => $server->id]);
                    $user->databases()->attach($new->id, ['privileges' => 'readwrite']);
                    $copies[] = ['engine' => $engine, 'from' => $database->name, 'to' => $name, 'fromServer' => $server->id, 'toServer' => $server->id];
                    $names[] = $name;
                }
                $users[] = ['engine' => $engine, 'name' => $user->name, 'password' => $password, 'databaseServer' => $server->id];
                $credentials[] = ['engine' => $engine, 'user' => $user->name, 'password' => $password, 'databases' => $names] + $connection($engine, $server);
            }
        }
        // Valkey: the data of a cache is for one website. The copy gets a new, empty Valkey
        // database for each Valkey database of the source, in the same database server, with
        // its own user and password. For the agent, a copy user of the engine Valkey is that
        // database.
        foreach ($all->where('engine', Database::VALKEY) as $database) {
            $server = $database->databaseServer;
            $name = $to.substr($database->name, strlen($from));
            $new = $copy->databases()->create(['engine' => Database::VALKEY, 'name' => $name, 'database_server_id' => $server->id]);
            $user = $copy->databaseUsers()->create(['engine' => Database::VALKEY, 'name' => $name, 'access' => 'website', 'database_server_id' => $server->id]);
            $user->databases()->attach($new->id, ['privileges' => 'readwrite']);
            $password = Input::databasePassword();
            $users[] = ['engine' => Database::VALKEY, 'name' => $name, 'password' => $password, 'databaseServer' => $server->id];
            $credentials[] = ['engine' => Database::VALKEY, 'user' => $name, 'password' => $password, 'databases' => [$name]] + $connection(Database::VALKEY, $server);
        }

        return [$copies, $users, $credentials, collect($servers)->unique('id')->sortBy([['engine', 'asc'], ['port', 'asc']])->values()->all()];
    }

    /**
     * CF-09: the plan limits the number of websites of a customer. The caller runs this in a
     * transaction. The lock on the customer row stops two requests from passing at the same time.
     *
     * @throws ValidationException
     */
    private static function checkWebsiteLimit(Customer $customer): void
    {
        $customer = Customer::with('plan')->lockForUpdate()->findOrFail($customer->id);
        if ($customer->websites()->count() >= $customer->plan->websites) {
            throw ValidationException::withMessages(['name' => "The {$customer->plan->name} plan allows {$customer->plan->websites} websites."]);
        }
        // R-07: the websites of the customers of a reseller count against the plan of the reseller.
        // Audit finding R6: the same total applies when the reseller makes a website for itself.
        if ($customer->reseller_id || $customer->role === 'reseller') {
            $reseller = $customer->reseller_id ? Customer::with('plan')->lockForUpdate()->findOrFail($customer->reseller_id) : $customer;
            $ids = Customer::where('id', $reseller->id)->orWhere('reseller_id', $reseller->id)->pluck('id');
            if (Website::whereIn('customer_id', $ids)->count() >= $reseller->plan->websites) {
                throw ValidationException::withMessages(['name' => $customer->reseller_id
                    ? "The {$reseller->plan->name} plan of your provider allows {$reseller->plan->websites} websites in total."
                    : "The {$reseller->plan->name} plan allows {$reseller->plan->websites} websites in total, for you and your customers."]);
            }
        }
    }

    /**
     * CF-07: an administrator, or the reseller of the customer, can select the plan. R-07: a
     * reseller can select only the plans that the administrator set for it.
     */
    public static function canChoosePlan(User $user, Customer $customer, string $planId): bool
    {
        return $user->isAdministrator() || ($user->role === Role::Reseller && $customer->reseller_id !== null && $customer->reseller_id === $user->customer_id
            && in_array($planId, $user->customer->allowedPlanIds(), true));
    }

    /** A Unix user name: up to 8 letters from the domain, then 4 random characters. */
    public static function unixUser(string $domain): string
    {
        $base = substr(preg_replace('/[^a-z0-9]/', '', explode('.', $domain)[0]), 0, 8) ?: 'site';
        if (ctype_digit($base[0])) {
            $base = 's'.$base;
        }
        // Adversarial review, finding X2-03: the names that start with "adminer_" and
        // "plugin_" are for the containers of the panel. No two containers have one Unix user.
        if (in_array($base, ['adminer', 'plugin', 'cf', 'db', 'root'], true)) {
            $base = 's'.substr($base, 0, 7);
        }
        do {
            $name = $base.'_'.strtolower(Str::random(4));
        } while (Website::where('unix_user', $name)->exists() || DatabaseTool::where('unix_user', $name)->exists() || Plugin::where('unix_user', $name)->exists());

        return $name;
    }
}
