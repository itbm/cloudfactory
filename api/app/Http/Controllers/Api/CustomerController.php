<?php

namespace App\Http\Controllers\Api;

use App\Enums\AgentAction;
use App\Enums\Role;
use App\Enums\WebsiteStatus;
use App\Http\Controllers\Controller;
use App\Models\Customer;
use App\Models\Domain;
use App\Models\Plan;
use App\Models\User;
use App\Models\Website;
use App\Services\Audit;
use App\Services\Branding;
use App\Services\DatabaseServers;
use App\Services\Dns\Zones;
use App\Services\DomainPolicy;
use App\Services\JobDispatcher;
use App\Services\Notifications\Notifier;
use App\Services\PanelCertificate;
use Illuminate\Http\JsonResponse;
use Illuminate\Http\Request;
use Illuminate\Support\Facades\DB;
use Illuminate\Validation\Rule;
use Illuminate\Validation\ValidationException;

/** FR-102 and FR-106: resellers make website owners, set their limits and set their brand. */
class CustomerController extends Controller
{
    public function __construct(private JobDispatcher $jobs) {}

    public function index(Request $request): JsonResponse
    {
        $ids = $request->user()->visibleCustomerIds();

        return response()->json(['data' => Customer::withCount(['websites', 'databaseServers'])
            ->when($ids !== null, fn ($q) => $q->whereIn('id', $ids))
            ->orderBy('name')->get()->map(fn (Customer $c) => self::present($c))]);
    }

    public function store(Request $request): JsonResponse
    {
        $user = $request->user();
        if ($user->role === Role::Owner) {
            return response()->json(['message' => 'Website owners cannot add customers.'], 403);
        }
        $data = $request->validate([
            'name' => ['required', 'string', 'max:200'],
            'email' => ['required', 'email'],
            // A reseller can add website owners only.
            'role' => ['required', Rule::in($user->isAdministrator() ? ['reseller', 'owner'] : ['owner'])],
            'planId' => ['required', 'exists:plans,id'],
            // Audit finding B5: also make the first user of the customer, with the email of
            // the customer. The answer then has the temporary password of that user.
            'createUser' => ['sometimes', 'boolean'],
        ]);
        $createUser = (bool) ($data['createUser'] ?? false);
        if ($createUser) {
            $token = $user->currentAccessToken();
            if (! $token || ! $token->can('users:write')) {
                return response()->json(['message' => 'The token does not have the users:write scope.'], 403);
            }
            $request->validate(['email' => ['max:255', Rule::unique('users', 'email')]], ['email.unique' => 'A user with this email exists already.']);
        }
        self::checkPlan($user, $data['planId']);
        [$customer, $account, $password] = DB::transaction(function () use ($data, $user, $createUser) {
            $customer = Customer::create([
                'name' => $data['name'],
                'email' => $data['email'],
                'role' => $data['role'],
                'plan_id' => $data['planId'],
                'reseller_id' => $user->role === Role::Reseller ? $user->customer_id : null,
            ]);

            return [$customer, ...($createUser ? UserController::make($data['name'], $data['email'], $customer) : [null, null])];
        });
        Audit::record($request, 'Created customer', $customer->name, $customer->id);
        if ($account) {
            Audit::record($request, 'Created user', $account->email, $customer->id);
        }

        return response()->json(self::present($customer) + ($account ? ['user' => UserController::present($account) + ['temporaryPassword' => $password]] : []), 201);
    }

    public function update(Request $request, string $id): JsonResponse
    {
        $customer = $this->find($request, $id);
        $data = $request->validate([
            'name' => ['sometimes', 'string', 'max:200'],
            'planId' => ['sometimes', 'exists:plans,id'],
            'suspended' => ['sometimes', 'boolean'],
            'branding' => ['sometimes', 'array'],
            ...Branding::rules('branding.'),
            'branding.panelDomain' => ['sometimes', 'nullable', 'regex:'.WebsiteController::DOMAIN],
            'branding.nameServers' => ['sometimes', 'nullable', 'array', 'max:10'],
            'branding.nameServers.*' => ['distinct', 'regex:'.WebsiteController::DOMAIN],
            'branding.mailHost' => ['sometimes', 'nullable', 'regex:'.WebsiteController::DOMAIN],
            'allowedPlanIds' => ['sometimes', 'array'],
            'allowedPlanIds.*' => ['distinct', 'exists:plans,id'],
        ], [
            'branding.panelDomain.regex' => 'Enter a domain in lower-case letters, for example panel.agency.example.',
            'branding.nameServers.*.regex' => 'Enter a host name in lower-case letters, for example ns1.agency.example.',
            'branding.nameServers.*.distinct' => 'Give a different host name to each name server.',
            'branding.mailHost.regex' => 'Enter a host name in lower-case letters, for example mail.agency.example.',
        ]);
        $user = $request->user();
        if ($customer->id === $user->customer_id && (isset($data['planId']) || isset($data['suspended']))) {
            return response()->json(['message' => 'You cannot change your own plan or status.'], 403);
        }
        // R-07: only an administrator sets the plans of a reseller.
        if (isset($data['allowedPlanIds']) && (! $user->isAdministrator() || $customer->role !== 'reseller')) {
            return response()->json(['message' => 'Only an administrator can set the plans of a reseller.'], 403);
        }
        if (isset($data['planId']) && $data['planId'] !== $customer->plan_id) {
            self::checkPlan($user, $data['planId']);
            self::checkPlanHasSpace($customer, Plan::findOrFail($data['planId']));
        }
        // R-06: a reseller cannot lift a suspension that an administrator set.
        if (($data['suspended'] ?? null) === false && $customer->suspended && $customer->suspended_by === 'administrator' && ! $user->isAdministrator()) {
            return response()->json(['message' => 'An administrator suspended this customer. Only an administrator can lift the suspension.'], 403);
        }
        // CF-20: the brand is for resellers. An administrator or the reseller itself sets it.
        if (isset($data['branding'])) {
            if ($customer->role !== 'reseller' || ! ($user->isAdministrator() || $customer->id === $user->customer_id)) {
                return response()->json(['message' => 'Only a reseller has a brand.'], 403);
            }
            $data['branding'] = $this->checkBrand($data['branding'], $customer, $user);
        }
        $suspension = [];
        if (isset($data['suspended']) && $data['suspended'] !== $customer->suspended) {
            $suspension = ['suspended' => $data['suspended'], 'suspended_by' => $data['suspended'] ? ($user->isAdministrator() ? 'administrator' : 'reseller') : null];
        }
        $brandBefore = $customer->branding ?? [];
        $customer->update(array_filter([
            'name' => $data['name'] ?? null,
            'plan_id' => $data['planId'] ?? null,
            'branding' => isset($data['branding']) ? self::mergeBrand($customer->branding ?? [], $data['branding']) : null,
            'plan_ids' => isset($data['allowedPlanIds']) ? array_values($data['allowedPlanIds']) : null,
        ], fn ($v) => $v !== null) + $suspension);
        // CF-08: a suspension takes effect at once.
        if ($data['suspended'] ?? false) {
            $customer->revokeTokens();
        }
        if ($suspension) {
            $this->applySuspension($customer->fresh());
            app(Notifier::class)->suspension($customer->fresh(), $suspension['suspended']);
        }
        if (isset($data['branding'])) {
            $this->brandChanged($brandBefore, $customer->fresh());
        }
        Audit::record($request, 'Changed customer', $customer->name, $customer->id);

        return response()->json(self::present($customer->fresh()));
    }

    /**
     * A suspension stops the websites of the customer, and of the customers of a reseller. When it
     * ends, only the websites that the suspension stopped start again.
     *
     * Audit finding F05: a suspension also turns off the SFTP access of each website, and the
     * agent stops the open SFTP sessions. Before, a suspended customer could still change its
     * files with SFTP. When the suspension ends, the websites that had SFTP get it again, with
     * the same keys.
     */
    private function applySuspension(Customer $customer): void
    {
        $ids = Customer::where('id', $customer->id)->orWhere('reseller_id', $customer->id)->get()
            // A customer of a reseller stays stopped while its own suspension continues.
            ->filter(fn (Customer $c) => $customer->suspended || ! $c->isSuspended())
            ->pluck('id');
        $websites = Website::with('server')->whereIn('customer_id', $ids)->get();
        foreach ($websites as $website) {
            if ($customer->suspended && in_array($website->status, [WebsiteStatus::Running, WebsiteStatus::Starting], true)) {
                $website->update(['status' => WebsiteStatus::Stopping, 'stopped_by_suspension' => true]);
                $this->jobs->dispatch($website->server, AgentAction::SetWebsitePower, ['websiteId' => $website->id, 'unixUser' => $website->unix_user, 'power' => 'stop'], "Stop {$website->name} (suspended)", $website);
            } elseif (! $customer->suspended && $website->stopped_by_suspension && $website->status === WebsiteStatus::Failed) {
                // The panel could not make this website, so it has no container to start.
                $website->update(['stopped_by_suspension' => false]);
            } elseif (! $customer->suspended && $website->stopped_by_suspension) {
                $website->update(['status' => WebsiteStatus::Starting, 'stopped_by_suspension' => false]);
                $this->jobs->dispatch($website->server, AgentAction::SetWebsitePower, ['websiteId' => $website->id, 'unixUser' => $website->unix_user, 'power' => 'start'], "Start {$website->name} (suspension ended)", $website);
            }
            // The result of the job records the new SFTP state of the website (JobOutcome). The
            // keys stay in the panel, so that the owner does not add them again.
            $sftp = ['websiteId' => $website->id, 'unixUser' => $website->unix_user, 'authorizedKeys' => $website->sftp_keys ?? []];
            if ($customer->suspended && $website->sftp_enabled) {
                $website->update(['sftp_closed_by_suspension' => true]);
                $this->jobs->dispatch($website->server, AgentAction::SetSftp, ['enabled' => false] + $sftp, "Turn off SFTP access of {$website->name} (suspended)", $website);
            } elseif (! $customer->suspended && $website->sftp_closed_by_suspension) {
                $website->update(['sftp_closed_by_suspension' => false]);
                if (($website->sftp_keys ?? []) !== [] && $website->status !== WebsiteStatus::Failed) {
                    $this->jobs->dispatch($website->server, AgentAction::SetSftp, ['enabled' => true] + $sftp, "Turn on SFTP access of {$website->name} (suspension ended)", $website);
                }
            }
        }
    }

    /**
     * Checks the new fields of the brand of a reseller, and gives them in the form that the
     * panel keeps. A null value removes the field.
     *
     * @param  array<string, mixed>  $brand
     * @return array<string, mixed>
     *
     * @throws ValidationException
     */
    private function checkBrand(array $brand, Customer $reseller, User $user): array
    {
        $before = $reseller->branding ?? [];
        $brand = array_intersect_key($brand, array_flip(['name', 'colour', 'logo', 'supportEmail', 'panelDomain', 'nameServers', 'mailHost']));
        foreach (['panelDomain', 'mailHost'] as $field) {
            if (isset($brand[$field])) {
                $brand[$field] = strtolower($brand[$field]);
            }
        }
        if (isset($brand['logo'])) {
            $brand['logo'] = Branding::logo($brand['logo'], 'branding.logo');
        }
        if (isset($brand['panelDomain']) && $brand['panelDomain'] !== ($before['panelDomain'] ?? null)) {
            $this->checkPanelDomain($brand['panelDomain'], $reseller, $user);
        }
        if (isset($brand['mailHost']) && $brand['mailHost'] !== ($before['mailHost'] ?? null)) {
            $this->checkBrandHost($brand['mailHost'], 'branding.mailHost', $reseller, $user);
        }
        if (array_key_exists('nameServers', $brand)) {
            $names = array_values(array_map('strtolower', $brand['nameServers'] ?? []));
            $panel = Zones::nameServers();
            if ($names !== [] && $panel === []) {
                throw ValidationException::withMessages(['branding.nameServers' => 'The panel has no DNS server, so it has no name servers to give your names to.']);
            }
            if ($names !== [] && count($names) !== count($panel)) {
                throw ValidationException::withMessages(['branding.nameServers' => 'Give '.count($panel).' host names: one for each name server of the panel.']);
            }
            foreach ($names as $i => $name) {
                if (! in_array($name, $before['nameServers'] ?? [], true)) {
                    $this->checkBrandHost($name, "branding.nameServers.{$i}", $reseller, $user);
                }
            }
            $brand['nameServers'] = $names === [] ? null : $names;
        }

        return $brand;
    }

    /**
     * Merges the new fields of a brand into the brand. A null value removes the field.
     *
     * @return array<string, mixed>
     */
    private static function mergeBrand(array $before, array $changes): array
    {
        return array_filter([...$before, ...$changes], fn ($v) => $v !== null && $v !== '');
    }

    /**
     * After a change of a brand: the agent on the panel server gets the new panel domains, and
     * the zones of the reseller and its customers get the new name servers.
     */
    private function brandChanged(array $before, Customer $reseller): void
    {
        $after = $reseller->branding ?? [];
        if (($before['panelDomain'] ?? null) !== ($after['panelDomain'] ?? null)) {
            Branding::syncPanelDomains($this->jobs);
        }
        if (($before['nameServers'] ?? null) !== ($after['nameServers'] ?? null)) {
            app(Zones::class)->resellerNameServersChanged($reseller);
        }
    }

    /**
     * A host name of a brand, for example a name server or the mail host. It must not be
     * reserved, and the reseller must prove that it controls the name, as for a panel domain.
     */
    private function checkBrandHost(string $name, string $field, Customer $reseller, User $user): void
    {
        $policy = app(DomainPolicy::class);
        if ($policy->isReserved($name, $reseller->id)) {
            throw ValidationException::withMessages([$field => 'This name is reserved or in use.']);
        }
        if (! $policy->preVerified($user, $reseller->id, $name) && ! $policy->proves($reseller->id, $name)) {
            throw ValidationException::withMessages([$field => "Prove that you control {$name}. Add a TXT record _cloudfactory-challenge.{$name} with the value {$policy->claimToken($reseller->id, $name)}, and then save again. DNS changes can take some minutes."]);
        }
    }

    /**
     * A reseller panel domain must not be reserved, and must not be a domain of a website of a
     * different customer (CF-03, CF-20). R-03: a reseller must also prove that it controls the
     * domain, with a verified domain of its own or a DNS TXT record.
     */
    private function checkPanelDomain(string $domain, Customer $reseller, User $user): void
    {
        $policy = app(DomainPolicy::class);
        $inUse = Domain::where('name', $domain)->whereHas('website', fn ($q) => $q->where('customer_id', '!=', $reseller->id))->exists();
        if ($policy->isReserved($domain, $reseller->id) || $inUse) {
            throw ValidationException::withMessages(['branding.panelDomain' => 'This domain is reserved or in use.']);
        }
        if (! $policy->preVerified($user, $reseller->id, $domain) && ! $policy->proves($reseller->id, $domain)) {
            throw ValidationException::withMessages(['branding.panelDomain' => "Prove that you control this domain. Add a TXT record _cloudfactory-challenge.{$domain} with the value {$policy->claimToken($reseller->id, $domain)}, and then save again. DNS changes can take some minutes."]);
        }
    }

    /**
     * R-07: a reseller gives its customers only the plans that the administrator set for it.
     *
     * @throws ValidationException
     */
    private static function checkPlan(User $user, string $planId): void
    {
        if ($user->role === Role::Reseller && ! in_array($planId, $user->customer->allowedPlanIds(), true)) {
            throw ValidationException::withMessages(['planId' => 'Select a plan that your provider gives you.']);
        }
    }

    /**
     * Audit finding R6: a customer cannot move to a plan that allows fewer websites, or fewer
     * database servers, than the customer has. For a reseller, those of its customers count too.
     *
     * @throws ValidationException
     */
    private static function checkPlanHasSpace(Customer $customer, Plan $plan): void
    {
        $ids = $customer->role === 'reseller'
            ? Customer::where('id', $customer->id)->orWhere('reseller_id', $customer->id)->pluck('id')
            : [$customer->id];
        $websites = Website::whereIn('customer_id', $ids)->count();
        if ($websites > $plan->websites) {
            throw ValidationException::withMessages(['planId' => "The {$plan->name} plan allows {$plan->websites} websites. {$customer->name} has {$websites}."]);
        }
        $databaseServers = DatabaseServers::countFor($customer);
        if ($databaseServers > $plan->database_servers) {
            throw ValidationException::withMessages(['planId' => "The {$plan->name} plan allows {$plan->database_servers} database servers. {$customer->name} has {$databaseServers}."]);
        }
    }

    private function find(Request $request, string $id): Customer
    {
        $customer = Customer::findOrFail($id);
        abort_unless($request->user()->canSeeCustomer($customer->id), 404);

        return $customer;
    }

    public static function present(Customer $c): array
    {
        return [
            'id' => $c->id,
            'name' => $c->name,
            'email' => $c->email,
            'role' => $c->role,
            'planId' => $c->plan_id,
            'resellerId' => $c->reseller_id,
            'branding' => $c->branding,
            // White label: the state of the certificate of the panel domain of a reseller, and
            // the brand that the reseller and its customers see.
            'brandStatus' => $c->role === 'reseller' ? [
                'certificate' => Branding::certificateOf($c->branding['panelDomain'] ?? null),
                // The domain of the panel: a CNAME record of the reseller panel domain can point to it.
                'panelDomain' => PanelCertificate::domain(),
                // The name servers of the panel and their addresses. Each host name of the
                // reseller needs the same A and AAAA records.
                'panelNameServers' => Zones::nameServers(),
                'nameServerAddresses' => Zones::servers()->unique('dns_name')
                    ->mapWithKeys(fn ($s) => [$s->dns_name => array_values($s->addresses ?? [])])->all(),
            ] : null,
            'allowedPlanIds' => $c->role === 'reseller' ? $c->allowedPlanIds() : null,
            'suspended' => $c->suspended,
            'suspendedBy' => $c->suspended_by,
            'websites' => $c->websites_count ?? null,
            'databaseServers' => $c->database_servers_count ?? null,
            'createdAt' => $c->created_at?->toIso8601String(),
        ];
    }
}
