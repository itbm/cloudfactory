<?php

namespace App\Http\Controllers\Api;

use App\Enums\AgentAction;
use App\Enums\DomainKind;
use App\Http\Controllers\Controller;
use App\Http\Resources\JobResource;
use App\Services\Audit;
use App\Services\JobDispatcher;
use Illuminate\Http\JsonResponse;
use Illuminate\Http\Request;
use Illuminate\Validation\Rule;
use Illuminate\Validation\ValidationException;

/** FR-401 to FR-403: a primary domain, redirects and aliases. */
class DomainController extends Controller
{
    use Concerns;

    public function __construct(private JobDispatcher $jobs) {}

    public function store(Request $request, string $websiteId): JsonResponse
    {
        $website = $this->findWebsite($request, $websiteId);
        $data = $request->validate([
            'name' => ['required', 'string', 'lowercase', 'regex:'.WebsiteController::DOMAIN, 'unique:domains,name'],
            'kind' => ['required', Rule::in([DomainKind::Redirect->value, DomainKind::Alias->value])],
        ]);
        $website->domains()->create(['name' => $data['name'], 'kind' => $data['kind']]);
        $job = $this->jobs->dispatch($website->server, AgentAction::AddDomain, ['websiteId' => $website->id, 'unixUser' => $website->unix_user, 'domain' => $data['name'], 'kind' => $data['kind'], 'primary' => $website->name], "Add domain {$data['name']}", $website);
        Audit::record($request, 'Added domain', "{$website->name} {$data['name']}", $website->customer_id);

        return response()->json(['job' => new JobResource($job)], 202);
    }

    public function destroy(Request $request, string $websiteId, string $name): JsonResponse
    {
        $website = $this->findWebsite($request, $websiteId);
        $domain = $website->domains()->where('name', $name)->firstOrFail();
        if ($domain->kind === DomainKind::Primary) {
            throw ValidationException::withMessages(['name' => 'Make a different domain primary first.']);
        }
        $domain->delete();
        $job = $this->jobs->dispatch($website->server, AgentAction::RemoveDomain, ['websiteId' => $website->id, 'domain' => $name], "Remove domain {$name}", $website);
        Audit::record($request, 'Removed domain', "{$website->name} {$name}", $website->customer_id);

        return response()->json(['job' => new JobResource($job)], 202);
    }
}
