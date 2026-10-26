<?php

namespace App\Http\Controllers\Api;

use App\Enums\AgentAction;
use App\Enums\DomainKind;
use App\Enums\WebsiteStatus;
use App\Http\Controllers\Controller;
use App\Http\Resources\JobResource;
use App\Http\Resources\WebsiteResource;
use App\Models\Customer;
use App\Models\Image;
use App\Models\Plan;
use App\Models\Server;
use App\Models\Website;
use App\Services\Audit;
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

    public const DOMAIN = '/^(?!-)[a-z0-9-]{1,63}(\.[a-z0-9-]{1,63})+$/';

    public function __construct(private JobDispatcher $jobs) {}

    public function index(Request $request): AnonymousResourceCollection
    {
        $request->validate(['status' => ['nullable', Rule::enum(WebsiteStatus::class)], 'q' => ['nullable', 'string']]);
        $websites = $this->visibleWebsites($request)
            ->with(['image', 'domains'])
            ->when($request->query('status'), fn ($q, $s) => $q->where('status', $s))
            ->when($request->query('q'), fn ($q, $s) => $q->where('name', 'like', '%'.$s.'%'))
            ->orderBy('name')
            ->paginate(min(100, (int) $request->query('perPage', 25)));

        return WebsiteResource::collection($websites);
    }

    public function show(Request $request, string $id): WebsiteResource
    {
        return new WebsiteResource($this->findWebsite($request, $id));
    }

    public function store(Request $request): JsonResponse
    {
        $data = $request->validate([
            'name' => ['required', 'string', 'lowercase', 'regex:'.self::DOMAIN, 'unique:websites,name', 'unique:domains,name'],
            'application' => ['nullable', 'string', 'max:100'],
            'imageId' => ['required', 'exists:images,id'],
            'planId' => ['required', 'exists:plans,id'],
            'serverId' => ['nullable', 'exists:servers,id'],
            'customerId' => ['nullable', 'exists:customers,id'],
        ]);
        $user = $request->user();
        $customerId = $data['customerId'] ?? $user->customer_id;
        if (! $customerId || ! $user->canSeeCustomer($customerId)) {
            throw ValidationException::withMessages(['customerId' => 'Select a customer that you control.']);
        }
        $customer = Customer::with('plan')->findOrFail($customerId);
        if ($customer->websites()->count() >= $customer->plan->websites) {
            throw ValidationException::withMessages(['name' => "The {$customer->plan->name} plan allows {$customer->plan->websites} websites."]);
        }

        $image = Image::findOrFail($data['imageId']);
        $plan = Plan::findOrFail($data['planId']);
        $server = isset($data['serverId'])
            ? Server::findOrFail($data['serverId'])
            : Server::whereJsonContains('roles', 'web')->where('state', 'healthy')->withCount('websites')->orderBy('websites_count')->first();
        if (! $server || ! in_array('web', $server->roles, true)) {
            throw ValidationException::withMessages(['serverId' => 'Select a server with the web role.']);
        }

        $website = DB::transaction(function () use ($data, $customer, $image, $plan, $server) {
            $website = Website::create([
                'name' => $data['name'],
                'application' => $data['application'] ?? '',
                'customer_id' => $customer->id,
                'server_id' => $server->id,
                'plan_id' => $plan->id,
                'image_id' => $image->id,
                'unix_user' => self::unixUser($data['name']),
                'status' => WebsiteStatus::Starting,
                'extensions' => $image->isPhp() ? array_values(array_intersect(['opcache', 'intl', 'gd', 'zip'], $plan->php_extensions)) : [],
                'php_ini' => $image->isPhp() ? ['memory_limit' => '256M', 'upload_max_filesize' => '64M', 'max_execution_time' => '60'] : [],
                'limits' => $plan->limits,
            ]);
            $website->domains()->create(['name' => $data['name'], 'kind' => DomainKind::Primary]);

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

        $changes = array_filter([
            'application' => $data['application'] ?? null,
            'managed_by_terraform' => $data['managedByTerraform'] ?? null,
        ], fn ($v) => $v !== null);
        if (isset($data['planId'])) {
            $plan = Plan::findOrFail($data['planId']);
            $changes += ['plan_id' => $plan->id, 'limits' => $plan->limits, 'extensions' => array_values(array_intersect($website->extensions, $plan->php_extensions))];
        }
        if (isset($data['limits'])) {
            $changes['limits'] = $data['limits'];
        }
        $website->update($changes);

        $job = null;
        if (isset($changes['limits'])) {
            $job = $this->jobs->dispatch($website->server, AgentAction::SetLimits, ['websiteId' => $website->id, 'unixUser' => $website->unix_user, 'limits' => $website->limits], 'Apply resource limits', $website);
        }
        Audit::record($request, 'Changed website', $website->name, $website->customer_id);

        return response()->json(['website' => new WebsiteResource($website->fresh(['image', 'domains'])), 'job' => $job ? new JobResource($job) : null]);
    }

    public function destroy(Request $request, string $id): JsonResponse
    {
        $website = $this->findWebsite($request, $id);
        $job = $this->jobs->dispatch($website->server, AgentAction::DeleteWebsite, ['websiteId' => $website->id, 'unixUser' => $website->unix_user], "Remove website {$website->name}", $website);
        Audit::record($request, 'Removed website', $website->name, $website->customer_id);

        return response()->json(['job' => new JobResource($job)], 202);
    }

    /** FR-204: start, stop and restart. */
    public function power(Request $request, string $id): JsonResponse
    {
        $website = $this->findWebsite($request, $id);
        $data = $request->validate(['action' => ['required', Rule::in(['start', 'stop', 'restart'])]]);
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
        $name = 'staging.'.preg_replace('/^www\./', '', $source->name);
        if (Website::where('name', $name)->exists()) {
            throw ValidationException::withMessages(['name' => "{$name} exists."]);
        }
        $copy = DB::transaction(function () use ($source, $name) {
            $copy = $source->replicate(['usage', 'managed_by_terraform']);
            $copy->fill([
                'name' => $name,
                'application' => "Staging copy of {$source->name}",
                'unix_user' => self::unixUser($name),
                'status' => WebsiteStatus::Starting,
                'staging_of' => $source->id,
                'managed_by_terraform' => false,
            ])->save();
            $copy->domains()->create(['name' => $name, 'kind' => DomainKind::Primary]);

            return $copy;
        });
        $payload = JobDispatcher::websitePayload($copy) + ['copyFrom' => $source->id, 'copyFromUnixUser' => $source->unix_user];
        $job = $this->jobs->dispatch($copy->server, AgentAction::CreateWebsite, $payload, "Make staging copy of {$source->name}", $copy);
        Audit::record($request, 'Made staging copy', "{$source->name} → {$name}", $source->customer_id);

        return response()->json(['website' => new WebsiteResource($copy->fresh(['image', 'domains'])), 'job' => new JobResource($job)], 202);
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
