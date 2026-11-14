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
use App\Models\Image;
use App\Models\Plan;
use App\Models\Server;
use App\Models\User;
use App\Models\Website;
use App\Services\Audit;
use App\Services\DatabaseNames;
use App\Services\DomainPolicy;
use App\Services\JobDispatcher;
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
     * (agent/internal/validate), and the two test it with testdata/names.json. Each label
     * starts and ends with a letter or a digit, and the last label is not a number, so that an
     * IP address is not a domain name.
     */
    public const DOMAIN = '/^(?=.{1,253}$)([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+(?!\d+$)[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$/';

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
        $data = $request->validate([
            'name' => ['required', 'string', 'lowercase', 'regex:'.self::DOMAIN],
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
        $proved = DomainController::checkName($this->domains, $data['name'], $customer->id, $request);
        if (Website::where('name', $data['name'])->exists()) {
            throw ValidationException::withMessages(['name' => 'The name has already been taken.']);
        }
        $domain = DomainController::newDomain($this->domains, $request, $customer->id, $data['name'], DomainKind::Primary, $proved);

        $website = DB::transaction(function () use ($data, $customer, $image, $plan, $server, $domain) {
            self::checkWebsiteLimit($customer);
            $website = Website::create([
                'name' => $data['name'],
                'application' => $data['application'] ?? '',
                'customer_id' => $customer->id,
                'server_id' => $server->id,
                'plan_id' => $plan->id,
                'image_id' => $image->id,
                'unix_user' => self::unixUser($data['name']),
                'status' => WebsiteStatus::Starting,
                // The database drivers are on from the start: most PHP applications need one.
                'extensions' => $image->isPhp() ? array_values(array_intersect(self::DEFAULT_EXTENSIONS, $plan->php_extensions)) : [],
                'php_ini' => $image->isPhp() ? ['memory_limit' => '256M', 'upload_max_filesize' => '64M', 'max_execution_time' => '60'] : [],
                'limits' => $plan->limits,
            ]);
            $website->domains()->create($domain);

            return $website;
        });

        $job = $this->jobs->dispatch($server, AgentAction::CreateWebsite, JobDispatcher::websitePayload($website), "Create website {$website->name}", $website);
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
            $plan = Plan::findOrFail($data['planId']);
            // Audit finding R6: the website must be in the limits of the new plan.
            $databases = $website->databases()->count();
            if ($databases > $plan->databases) {
                throw ValidationException::withMessages(['planId' => "The {$plan->name} plan allows {$plan->databases} databases. This website has {$databases}."]);
            }
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
        // The agent also removes the databases and the database users of the website, so that a
        // later website with the same Unix user does not get them.
        $job = $this->jobs->dispatch($website->server, AgentAction::DeleteWebsite, [
            'websiteId' => $website->id,
            'unixUser' => $website->unix_user,
            'databases' => $website->databases()->orderBy('name')->get(['engine', 'name'])->map(fn ($d) => ['engine' => $d->engine, 'name' => $d->name])->all(),
            'databaseUsers' => $website->databaseUsers()->orderBy('name')->get(['engine', 'name'])->map(fn ($u) => ['engine' => $u->engine, 'name' => $u->name])->all(),
        ], "Remove website {$website->name}", $website);
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
        $name = 'staging.'.preg_replace('/^www\./', '', $source->name);
        if (Website::where('name', $name)->exists()) {
            throw ValidationException::withMessages(['name' => "{$name} exists."]);
        }
        // Audit finding R6: the copy gets a copy of each database, and has the same plan.
        $databases = $source->databases()->count();
        if ($databases > $source->plan->databases) {
            throw ValidationException::withMessages(['name' => "The {$source->plan->name} plan allows {$source->plan->databases} databases for each website. This website has {$databases}."]);
        }
        $proved = DomainController::checkName($this->domains, $name, $source->customer_id, $request);
        $domain = DomainController::newDomain($this->domains, $request, $source->customer_id, $name, DomainKind::Primary, $proved);
        $copy = DB::transaction(function () use ($source, $name, $domain) {
            // CF-09: a staging copy is a website, so the plan limit applies.
            self::checkWebsiteLimit($source->customer);
            // Audit finding R7: the copy has no SFTP account on the server, so it does not get
            // the SFTP settings of the source. The owner turns SFTP on for the copy.
            $copy = $source->replicate(['usage', 'managed_by_terraform', 'sftp_enabled', 'sftp_keys']);
            $copy->fill([
                'name' => $name,
                'application' => "Staging copy of {$source->name}",
                'unix_user' => self::unixUser($name),
                'status' => WebsiteStatus::Starting,
                'staging_of' => $source->id,
                'managed_by_terraform' => false,
            ])->save();
            $copy->domains()->create($domain);

            return $copy;
        });
        [$copies, $users, $credentials] = $this->copyDatabases($source, $copy);
        $payload = JobDispatcher::websitePayload($copy) + [
            'copyFrom' => $source->id,
            'copyFromUnixUser' => $source->unix_user,
            // FR-205: the copy has its own databases and its own user. It never uses the
            // databases of the live website.
            'copyDatabases' => $copies,
            'copyUsers' => $users,
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
     * FR-205: each database of the source gets a copy with the prefix of the copy, and each engine
     * gets one new user with read-write access to its copies.
     *
     * @return array{0: list<array>, 1: list<array>, 2: list<array>}
     */
    private function copyDatabases(Website $source, Website $copy): array
    {
        $copies = $users = $credentials = [];
        $from = DatabaseNames::prefix($source);
        $to = DatabaseNames::prefix($copy);
        DB::transaction(function () use ($source, $copy, $from, $to, &$copies, &$users, &$credentials) {
            foreach ($source->databases()->orderBy('name')->get()->groupBy('engine') as $engine => $databases) {
                $user = $copy->databaseUsers()->create(['engine' => $engine, 'name' => $to.'app', 'access' => 'website']);
                $password = Str::password(24, symbols: false);
                $names = [];
                foreach ($databases as $database) {
                    $name = $to.substr($database->name, strlen($from));
                    $new = $copy->databases()->create(['engine' => $engine, 'name' => $name]);
                    $user->databases()->attach($new->id, ['privileges' => 'readwrite']);
                    $copies[] = ['engine' => $engine, 'from' => $database->name, 'to' => $name];
                    $names[] = $name;
                }
                $users[] = ['engine' => $engine, 'name' => $user->name, 'password' => $password];
                $credentials[] = ['engine' => $engine, 'user' => $user->name, 'password' => $password, 'databases' => $names];
            }
        });

        return [$copies, $users, $credentials];
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
    private static function canChoosePlan(User $user, Customer $customer, string $planId): bool
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

        return $base.'_'.strtolower(Str::random(4));
    }
}
