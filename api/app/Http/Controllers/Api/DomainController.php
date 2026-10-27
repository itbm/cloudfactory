<?php

namespace App\Http\Controllers\Api;

use App\Enums\AgentAction;
use App\Enums\DomainKind;
use App\Http\Controllers\Controller;
use App\Http\Resources\JobResource;
use App\Http\Resources\WebsiteResource;
use App\Models\Domain;
use App\Models\Website;
use App\Services\Audit;
use App\Services\DomainPolicy;
use App\Services\JobDispatcher;
use Illuminate\Http\JsonResponse;
use Illuminate\Http\Request;
use Illuminate\Support\Facades\DB;
use Illuminate\Support\Str;
use Illuminate\Validation\Rule;
use Illuminate\Validation\ValidationException;

/**
 * FR-401 to FR-403: a primary domain, redirects and aliases.
 *
 * CF-03: the edge proxy serves a domain only after the customer proves that they control it.
 * The panel gives a token. The customer puts it in a TXT record, and then asks the panel to verify.
 */
class DomainController extends Controller
{
    use Concerns;

    public function __construct(private JobDispatcher $jobs, private DomainPolicy $policy) {}

    public function store(Request $request, string $websiteId): JsonResponse
    {
        $website = $this->findWebsite($request, $websiteId);
        $data = $request->validate([
            'name' => ['required', 'string', 'lowercase', 'regex:'.WebsiteController::DOMAIN],
            'kind' => ['required', Rule::in([DomainKind::Redirect->value, DomainKind::Alias->value])],
        ]);
        $proved = self::checkName($this->policy, $data['name'], $website->customer_id, $request);
        $domain = $website->domains()->create(self::newDomain($this->policy, $request, $website->customer_id, $data['name'], DomainKind::from($data['kind']), $proved));
        $job = $domain->verified() ? $this->addRoute($website, $domain) : null;
        Audit::record($request, 'Added domain', "{$website->name} {$data['name']}", $website->customer_id);

        return response()->json(['domain' => WebsiteResource::domain($domain), 'job' => $job ? new JobResource($job) : null], 202);
    }

    /** Checks the TXT record of the domain. When it agrees, the edge proxy starts to serve the domain. */
    public function verify(Request $request, string $websiteId, string $name): JsonResponse
    {
        $website = $this->findWebsite($request, $websiteId);
        $domain = $website->domains()->where('name', $name)->firstOrFail();
        if ($domain->verified()) {
            return response()->json(['domain' => WebsiteResource::domain($domain), 'job' => null]);
        }
        if (! $this->policy->check($domain)) {
            throw ValidationException::withMessages(['name' => "The TXT record {$domain->verificationRecord()} does not have the value {$domain->verification_token}. DNS changes can take some minutes."]);
        }
        $domain->update(['verified_at' => now()]);
        $job = $this->addRoute($website, $domain);
        Audit::record($request, 'Verified domain', "{$website->name} {$domain->name}", $website->customer_id);

        return response()->json(['domain' => WebsiteResource::domain($domain), 'job' => new JobResource($job)], 202);
    }

    /**
     * FR-401: makes a verified domain the primary domain. The old primary domain becomes an
     * alias. The website takes the name of its primary domain.
     */
    public function makePrimary(Request $request, string $websiteId, string $name): JsonResponse
    {
        $website = $this->findWebsite($request, $websiteId);
        $domain = $website->domains()->where('name', $name)->firstOrFail();
        if ($domain->kind === DomainKind::Primary) {
            return response()->json(['website' => new WebsiteResource($website), 'job' => null]);
        }
        if (! $domain->verified()) {
            throw ValidationException::withMessages(['name' => 'Verify the domain first.']);
        }
        DB::transaction(function () use ($website, $domain) {
            $website->domains()->where('kind', DomainKind::Primary->value)->update(['kind' => DomainKind::Alias->value]);
            $domain->update(['kind' => DomainKind::Primary]);
            $website->update(['name' => $domain->name]);
        });
        $job = $this->addRoute($website->fresh(), $domain->fresh());
        Audit::record($request, 'Made domain primary', "{$website->name} {$name}", $website->customer_id);

        return response()->json(['website' => new WebsiteResource($website->fresh(['image', 'domains', 'server'])), 'job' => new JobResource($job)], 202);
    }

    /** FR-402 and FR-403: checks the DNS of a domain again, and asks for a certificate if it is ready. */
    public function check(Request $request, string $websiteId, string $name): JsonResponse
    {
        $website = $this->findWebsite($request, $websiteId);
        $domain = $website->domains()->where('name', $name)->firstOrFail();
        if (! $domain->verified()) {
            throw ValidationException::withMessages(['name' => 'Verify the domain first.']);
        }
        $domain->update(['certificate_state' => 'pending']);
        $job = $this->addRoute($website, $domain);

        return response()->json(['domain' => WebsiteResource::domain($domain->fresh()), 'job' => new JobResource($job)], 202);
    }

    public function destroy(Request $request, string $websiteId, string $name): JsonResponse
    {
        $website = $this->findWebsite($request, $websiteId);
        $domain = $website->domains()->where('name', $name)->firstOrFail();
        if ($domain->kind === DomainKind::Primary) {
            throw ValidationException::withMessages(['name' => 'Make a different domain primary first.']);
        }
        $domain->delete();
        $job = $domain->verified()
            ? $this->jobs->dispatch($website->server, AgentAction::RemoveDomain, ['websiteId' => $website->id, 'domain' => $name], "Remove domain {$name}", $website)
            : null;
        Audit::record($request, 'Removed domain', "{$website->name} {$name}", $website->customer_id);

        return response()->json(['job' => $job ? new JobResource($job) : null], 202);
    }

    /**
     * Refuses a reserved domain and a domain of a different website. A domain that a different
     * customer added but did not verify does not block the customer that can verify it.
     *
     * R-04: when that domain is the primary domain of a website of a different customer, the
     * customer must first prove control of it, with a verified domain of its own or a DNS TXT
     * record. Then the panel parks the other website under an internal name. The method returns
     * true when the customer proved control here, so that the new domain is verified at once.
     *
     * @throws ValidationException
     */
    public static function checkName(DomainPolicy $policy, string $name, string $customerId, Request $request, string $field = 'name'): bool
    {
        if ($policy->isReserved($name)) {
            throw ValidationException::withMessages([$field => 'This domain is reserved for the panel or a server.']);
        }
        $existing = Domain::with('website')->where('name', $name)->first();
        if (! $existing) {
            return false;
        }
        if ($existing->verified() || $existing->website->customer_id === $customerId) {
            throw ValidationException::withMessages([$field => 'This domain is in use.']);
        }
        if ($existing->kind !== DomainKind::Primary) {
            $existing->delete();

            return false;
        }
        $preVerified = $policy->preVerified($request->user(), $customerId, $name);
        if (! $preVerified && ! $policy->proves($customerId, $name)) {
            throw ValidationException::withMessages([$field => "A different website uses this domain but has not proved control of it. If you control the domain, add a TXT record _cloudfactory-challenge.{$name} with the value {$policy->claimToken($customerId, $name)}, and then try again. DNS changes can take some minutes."]);
        }
        self::park($existing->website, $request);

        return true;
    }

    /** R-04: gives a website an internal name, because a different customer proved control of its domain. */
    private static function park(Website $website, Request $request): void
    {
        $old = $website->name;
        $parked = 'parked-'.strtolower($website->id).'.invalid';
        $website->domains()->where('name', $old)->update(['name' => $parked]);
        $website->update(['name' => $parked]);
        Audit::record($request, 'Parked website with a domain that it did not verify', "{$old} → {$parked}", $website->customer_id);
    }

    /** The attributes of a new domain, verified at once when the policy permits or when the customer proved control. */
    public static function newDomain(DomainPolicy $policy, Request $request, string $customerId, string $name, DomainKind $kind, bool $proved = false): array
    {
        return [
            'name' => $name,
            'kind' => $kind,
            'verification_token' => 'cf-'.Str::lower(Str::random(40)),
            'verified_at' => $proved || $policy->preVerified($request->user(), $customerId, $name) ? now() : null,
        ];
    }

    private function addRoute(Website $website, Domain $domain)
    {
        if ($domain->certificate_state !== 'pending') {
            $domain->update(['certificate_state' => 'pending']);
        }

        return $this->jobs->dispatch($website->server, AgentAction::AddDomain, [
            'websiteId' => $website->id,
            'unixUser' => $website->unix_user,
            'domain' => $domain->name,
            'kind' => $domain->kind->value,
            'primary' => $website->name,
        ], "Add domain {$domain->name}", $website);
    }
}
