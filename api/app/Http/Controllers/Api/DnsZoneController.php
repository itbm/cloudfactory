<?php

namespace App\Http\Controllers\Api;

use App\Enums\JobState;
use App\Http\Controllers\Controller;
use App\Http\Resources\JobResource;
use App\Models\AgentJob;
use App\Models\Customer;
use App\Models\DnsAccount;
use App\Models\DnsRecord;
use App\Models\DnsZone;
use App\Models\Server;
use App\Models\Website;
use App\Services\Audit;
use App\Services\Branding;
use App\Services\Dns\Dnssec;
use App\Services\Dns\Import;
use App\Services\Dns\Records;
use App\Services\Dns\Rollover;
use App\Services\Dns\Secondaries;
use App\Services\Dns\ZoneFile;
use App\Services\Dns\Zones;
use App\Validation\Input;
use App\Validation\InputRules;
use Illuminate\Database\Eloquent\Builder;
use Illuminate\Http\JsonResponse;
use Illuminate\Http\Request;
use Illuminate\Support\Collection;
use Illuminate\Support\Facades\DB;
use Illuminate\Validation\Rule;
use Illuminate\Validation\ValidationException;

/**
 * DNS hosting: the zones of a customer and their records.
 *
 * A customer makes a zone for a domain, and then gives the name servers of the panel to the
 * registrar of the domain. Each change of a zone goes to each DNS server as a job. The panel
 * makes the NS records of the zone, and the A and AAAA records of the websites of the customer.
 * A zone with a DNS account goes to an external DNS provider instead (DnsAccountController).
 */
class DnsZoneController extends Controller
{
    public function __construct(private Zones $zones) {}

    private function visible(Request $request): Builder
    {
        $ids = $request->user()->visibleCustomerIds();

        return DnsZone::query()->when($ids !== null, fn (Builder $q) => $q->whereIn('customer_id', $ids));
    }

    private function find(Request $request, string $id): DnsZone
    {
        return $this->visible($request)->findOrFail($id);
    }

    /** A zone that the panel does not remove. A zone with the state "removing" takes no change. */
    private function findActive(Request $request, string $id): DnsZone
    {
        $zone = $this->find($request, $id);
        if ($zone->status === DnsZone::REMOVING) {
            throw ValidationException::withMessages(['zone' => 'The panel removes this zone.']);
        }

        return $zone;
    }

    public function index(Request $request): JsonResponse
    {
        $request->validate(['customerId' => ['nullable', 'string']]);
        $servers = Zones::servers();
        $zones = $this->visible($request)->with(['customer', 'account.plugin'])->withCount('records')
            ->when($request->query('customerId'), fn ($q, $id) => $q->where('customer_id', $id))
            ->orderBy('name')->get();

        // The customer gives these host names to the registrar of each domain. A customer of a
        // reseller sees the host names of the reseller (white label). The name servers of the
        // secondary DNS servers of the panel come after them.
        $own = $request->user()->isAdministrator() ? $servers->pluck('dns_name')->unique()->values()->all() : Branding::nameServersFor($request->user()->customer);

        return response()->json([
            'data' => $zones->map(fn (DnsZone $z) => self::present($z, $servers)),
            'nameServers' => $own === [] ? [] : array_values(array_unique([...$own, ...Secondaries::nameServers()])),
        ]);
    }

    public function show(Request $request, string $id): JsonResponse
    {
        return response()->json(self::present($this->find($request, $id), Zones::servers(), records: true));
    }

    public function store(Request $request): JsonResponse
    {
        $data = $request->validate([
            'name' => ['required', 'string', Input::rule('domain')],
            'customerId' => ['nullable', 'exists:customers,id'],
            // A DNS account: the zone goes to its external provider. Null: the DNS servers of the panel.
            'accountId' => ['nullable', 'string', 'max:64'],
            // With a DNS account: first copy the records that the provider has for the name now.
            'importRecords' => ['sometimes', 'boolean'],
        ]);
        $user = $request->user();
        $customerId = $data['customerId'] ?? $user->customer_id;
        if (! $customerId || ! $user->canSeeCustomer($customerId)) {
            throw ValidationException::withMessages(['customerId' => 'Select a customer that you control.']);
        }
        $customer = Customer::with('plan')->findOrFail($customerId);
        abort_if($customer->isSuspended(), 409, 'This customer is suspended. Lift the suspension before you make a DNS zone.');

        $account = self::account($request, $data['accountId'] ?? null, $customer->id);
        $zone = $this->zones->create($customer, $data['name'], $user, $account, (bool) ($data['importRecords'] ?? true));
        Audit::record($request, 'Created DNS zone', $zone->name.($account ? " ({$account->providerName()})" : ''), $customer->id);

        return response()->json(['zone' => self::present($zone->fresh(), Zones::servers(), records: true), 'jobs' => self::jobs($request, $zone)], 202);
    }

    /** Removes a zone from the DNS servers, with all its records. */
    public function destroy(Request $request, string $id): JsonResponse
    {
        $zone = $this->find($request, $id);
        // Adversarial review, finding A6-06: each removal makes one job for each DNS server.
        if ($zone->status === DnsZone::REMOVING && AgentJob::where('dns_zone_id', $zone->id)
            ->whereIn('state', [JobState::Queued->value, JobState::Running->value])->exists()) {
            throw ValidationException::withMessages(['zone' => 'The panel removes this zone. Wait until the jobs are complete.']);
        }
        // A zone at an external provider goes from the provider too, unless "keepAtProvider".
        $keep = $request->boolean('keepAtProvider');
        $provider = $zone->account?->providerName();
        $jobs = $this->zones->remove($zone, $keep);
        Audit::record($request, 'Removed DNS zone', $zone->name.($provider ? ($keep ? " (kept at {$provider})" : " (also at {$provider})") : ''), $zone->customer_id);

        return response()->json(['jobs' => JobResource::collection(collect($jobs))->resolve($request)], 202);
    }

    public function storeRecord(Request $request, string $id): JsonResponse
    {
        $zone = $this->findActive($request, $id);
        // The checks and the new record are in one transaction, with a lock on the zone: two
        // requests at the same time cannot go above the limit, or make a CNAME record and a
        // different record for one name.
        $record = DB::transaction(function () use ($zone, $request) {
            DnsZone::whereKey($zone->id)->lockForUpdate()->first();

            return $zone->records()->create(Records::check($zone, self::recordInput($request)));
        });
        $jobs = $this->zones->changed($zone, "Add {$record->type} record to {$zone->name}");
        Audit::record($request, 'Added DNS record', "{$zone->name} {$record->type} {$record->name}", $zone->customer_id);

        return response()->json(['record' => self::presentRecord($record), 'jobs' => JobResource::collection(collect($jobs))->resolve($request)], 202);
    }

    /**
     * Changes the name, the value, the TTL or the priority of a record. The type stays. A
     * record that the panel made for a website becomes a record of the user.
     */
    public function updateRecord(Request $request, string $id, string $recordId): JsonResponse
    {
        $zone = $this->findActive($request, $id);
        $record = self::editable($zone, $recordId);
        $record->update(Records::check($zone, self::recordInput($request), $record) + ['managed' => null]);
        $jobs = $this->zones->changed($zone, "Change {$record->type} record of {$zone->name}");
        Audit::record($request, 'Changed DNS record', "{$zone->name} {$record->type} {$record->name}", $zone->customer_id);

        return response()->json(['record' => self::presentRecord($record), 'jobs' => JobResource::collection(collect($jobs))->resolve($request)], 202);
    }

    public function destroyRecord(Request $request, string $id, string $recordId): JsonResponse
    {
        $zone = $this->findActive($request, $id);
        $record = self::editable($zone, $recordId);
        $record->delete();
        $jobs = $this->zones->changed($zone, "Remove {$record->type} record from {$zone->name}");
        Audit::record($request, 'Removed DNS record', "{$zone->name} {$record->type} {$record->name}", $zone->customer_id);

        return response()->json(['jobs' => JobResource::collection(collect($jobs))->resolve($request)], 202);
    }

    /**
     * Turns DNSSEC on or off. When DNSSEC is on, the answer has the DS record for the
     * registrar. Before DNSSEC is turned off, the customer removes the DS record at the
     * registrar: resolvers that still have the DS record refuse a zone with no signatures.
     */
    public function dnssec(Request $request, string $id): JsonResponse
    {
        $zone = $this->findActive($request, $id);
        $enabled = (bool) $request->validate(['enabled' => ['required', 'boolean']])['enabled'];
        $jobs = $this->zones->setDnssec($zone, $enabled);
        if ($jobs) {
            Audit::record($request, $enabled ? 'Turned on DNSSEC' : 'Turned off DNSSEC', $zone->name, $zone->customer_id);
        }

        return response()->json(['zone' => self::present($zone->fresh(), Zones::servers(), records: true), 'jobs' => JobResource::collection(collect($jobs))->resolve($request)], 202);
    }

    /**
     * Starts a change of the DNSSEC key of the zone. Refer to Rollover: the new key goes to the
     * DNS servers, and some hours later the panel shows its DS record for the registrar.
     */
    public function startRollover(Request $request, string $id, Rollover $rollover): JsonResponse
    {
        $zone = $this->findActive($request, $id);
        $jobs = $rollover->start($zone);
        Audit::record($request, 'Started a change of the DNSSEC key', $zone->name, $zone->customer_id);

        return response()->json(['zone' => self::present($zone->fresh(), Zones::servers(), records: true), 'jobs' => JobResource::collection(collect($jobs))->resolve($request)], 202);
    }

    /** Stops a change of the DNSSEC key before the registry has the new DS record. */
    public function cancelRollover(Request $request, string $id, Rollover $rollover): JsonResponse
    {
        $zone = $this->findActive($request, $id);
        $jobs = $rollover->cancel($zone);
        Audit::record($request, 'Stopped a change of the DNSSEC key', $zone->name, $zone->customer_id);

        return response()->json(['zone' => self::present($zone->fresh(), Zones::servers(), records: true), 'jobs' => JobResource::collection(collect($jobs))->resolve($request)], 202);
    }

    /**
     * Sends the zone to secondary DNS servers of the owner of the zone with zone transfers, or
     * changes them. Refer to Secondaries::setTransfers. The answer does not give the TSIG
     * secret: the route "transfers/secret" gives it.
     */
    public function transfers(Request $request, string $id, Secondaries $secondaries): JsonResponse
    {
        $zone = $this->findActive($request, $id);
        $data = $request->validate([
            'addresses' => ['required', 'array', 'min:1', 'max:'.InputRules::MAX_DNS_TRANSFER_ADDRESSES],
            'notify' => ['sometimes', 'boolean'],
            'nameServers' => ['sometimes', 'array', 'max:'.Secondaries::MAX_ZONE_NAME_SERVERS],
            'nameServers.*' => ['string', Input::rule('domain')],
            'tsig' => ['sometimes', 'boolean'],
            'algorithm' => ['sometimes', Rule::in(InputRules::TSIG_ALGORITHMS)],
            'secret' => ['sometimes', 'nullable', 'string', 'max:88'],
            'newSecret' => ['sometimes', 'boolean'],
        ]);
        $jobs = $secondaries->setTransfers($zone, [
            'addresses' => $data['addresses'],
            'notify' => (bool) ($data['notify'] ?? true),
            'nameServers' => $data['nameServers'] ?? [],
            'tsig' => (bool) ($data['tsig'] ?? true),
            'algorithm' => $data['algorithm'] ?? $zone->transfer_tsig_algorithm ?? 'hmac-sha256',
            'secret' => $data['secret'] ?? null,
            'newSecret' => (bool) ($data['newSecret'] ?? false),
        ]);
        Audit::record($request, 'Set the zone transfers of a DNS zone', $zone->name.' to '.implode(', ', $zone->transfer_addresses ?? []), $zone->customer_id);

        return response()->json(['zone' => self::present($zone->fresh(), Zones::servers(), records: true), 'jobs' => JobResource::collection(collect($jobs))->resolve($request)], 202);
    }

    /** Stops the zone transfers to the secondary DNS servers of the owner of the zone. */
    public function destroyTransfers(Request $request, string $id, Secondaries $secondaries): JsonResponse
    {
        $zone = $this->findActive($request, $id);
        $jobs = $secondaries->setTransfers($zone, null);
        if ($jobs) {
            Audit::record($request, 'Stopped the zone transfers of a DNS zone', $zone->name, $zone->customer_id);
        }

        return response()->json(['zone' => self::present($zone->fresh(), Zones::servers(), records: true), 'jobs' => JobResource::collection(collect($jobs))->resolve($request)], 202);
    }

    /** The TSIG key of the zone transfers with its secret, for the configuration of a secondary DNS server. */
    public function transfersSecret(Request $request, string $id): JsonResponse
    {
        $zone = $this->find($request, $id);
        abort_if($zone->transfer_tsig_algorithm === null || ! $zone->transfer_addresses, 404, 'The zone transfers of this zone have no TSIG key.');
        Audit::record($request, 'Viewed the TSIG secret of a DNS zone', $zone->name, $zone->customer_id);

        return response()->json(['name' => $zone->transferKeyName(), 'algorithm' => $zone->transfer_tsig_algorithm, 'secret' => (string) $zone->transfer_tsig_secret]);
    }

    /**
     * Moves the zone to a different DNS provider, or back to the DNS servers of the panel
     * ("accountId": null). The records stay. Refer to Zones::move.
     */
    public function move(Request $request, string $id): JsonResponse
    {
        $zone = $this->findActive($request, $id);
        $data = $request->validate([
            'accountId' => ['present', 'nullable', 'string', 'max:64'],
            'importRecords' => ['sometimes', 'boolean'],
        ]);
        $account = self::account($request, $data['accountId'], $zone->customer_id);
        $jobs = $this->zones->move($zone, $account, (bool) ($data['importRecords'] ?? true));
        Audit::record($request, 'Moved DNS zone', "{$zone->name} to ".($account ? "{$account->providerName()} ({$account->name})" : 'the DNS servers of the panel'), $zone->customer_id);

        return response()->json(['zone' => self::present($zone->fresh(), Zones::servers(), records: true), 'jobs' => JobResource::collection(collect($jobs))->resolve($request)], 202);
    }

    /**
     * The DNS account for a zone of the customer: an account of the customer, or of the panel.
     * Null when the request gives no account.
     *
     * @throws ValidationException
     */
    private static function account(Request $request, ?string $id, string $customerId): ?DnsAccount
    {
        if ($id === null || $id === '') {
            return null;
        }
        $account = DnsAccount::with('plugin')->find($id);
        if (! $account || ! $account->usableBy($customerId) || ($account->customer_id !== null && ! $request->user()->canSeeCustomer($account->customer_id))) {
            throw ValidationException::withMessages(['accountId' => 'Select a DNS account of this customer, or of the panel.']);
        }
        if ($account->definition() === null || ! $account->plugin->enabled) {
            throw ValidationException::withMessages(['accountId' => "The plugin of the DNS account {$account->name} is off. Speak to your provider."]);
        }

        return $account;
    }

    /** Asks the public DNS again if the name servers of the zone are the name servers of the panel. */
    public function check(Request $request, string $id, Rollover $rollover): JsonResponse
    {
        $zone = $this->findActive($request, $id);
        $this->zones->checkDelegation($zone);
        // The DS records at the registry. A change of the key can go to its next step.
        $rollover->check($zone);

        return response()->json(self::present($zone->fresh(), Zones::servers(), records: true));
    }

    /** Sends the zone to each DNS server again, for example after a job failed. */
    public function sync(Request $request, string $id): JsonResponse
    {
        $zone = $this->findActive($request, $id);
        $jobs = $this->zones->changed($zone, "Send DNS zone {$zone->name} again");

        return response()->json(['zone' => self::present($zone->fresh(), Zones::servers(), records: true), 'jobs' => JobResource::collection(collect($jobs))->resolve($request)], 202);
    }

    /**
     * Adds the records of a zone file to the zone, for example the export of the current DNS
     * provider of the domain. With "dryRun", the panel only shows what it would add. The import
     * does not change or remove the records that the zone has.
     */
    public function import(Request $request, string $id, Import $import): JsonResponse
    {
        $zone = $this->findActive($request, $id);
        $data = $request->validate([
            'zoneFile' => ['required', 'string', 'max:'.ZoneFile::MAX_BYTES],
            'dryRun' => ['sometimes', 'boolean'],
        ]);
        $dryRun = (bool) ($data['dryRun'] ?? false);
        $result = $import->run($zone, $data['zoneFile'], $dryRun);
        $count = count($result['added']);
        if (! $dryRun && $count > 0) {
            Audit::record($request, 'Imported DNS records', "{$zone->name} ({$count} ".($count === 1 ? 'record' : 'records').')', $zone->customer_id);
        }

        return response()->json([
            'dryRun' => $dryRun,
            'added' => $result['added'],
            'existing' => $result['existing'],
            'skipped' => $result['skipped'],
            'zone' => self::present($zone->fresh(), Zones::servers(), records: true),
            'jobs' => JobResource::collection(collect($result['jobs']))->resolve($request),
        ], $dryRun || $count === 0 ? 200 : 202);
    }

    /** The records of the zone as a zone file, for a different DNS provider or for a backup. */
    public function export(Request $request, string $id): JsonResponse
    {
        $zone = $this->find($request, $id);

        return response()->json(['name' => $zone->name, 'zoneFile' => ZoneFile::render($zone)]);
    }

    /** A record that a user can change. The panel keeps the NS records of the zone itself. */
    private static function editable(DnsZone $zone, string $recordId): DnsRecord
    {
        $record = $zone->records()->findOrFail($recordId);
        if ($record->managed === DnsRecord::NS) {
            throw ValidationException::withMessages(['record' => 'The panel keeps the name servers of the zone. You cannot change this record.']);
        }
        if ($record->managed === DnsRecord::ACME) {
            throw ValidationException::withMessages(['record' => 'The panel added this record for a certificate order, and removes it when the order is complete. You cannot change it.']);
        }

        return $record;
    }

    /** @return array<string, mixed> */
    private static function recordInput(Request $request): array
    {
        $data = $request->validate([
            'type' => ['sometimes', 'string', 'max:10'],
            'name' => ['sometimes', 'nullable', 'string', 'max:255'],
            'value' => ['sometimes', 'string', 'max:4096'],
            'ttl' => ['sometimes', 'integer'],
            'priority' => ['sometimes', 'nullable', 'integer'],
        ]);
        // A form sends a number as text. The rule "integer" accepts it, thus the value becomes
        // a number here.
        foreach (['ttl', 'priority'] as $field) {
            if (is_string($data[$field] ?? null) && preg_match('/^-?\d{1,10}$/D', $data[$field])) {
                $data[$field] = (int) $data[$field];
            }
        }

        return $data;
    }

    /** The jobs of the last change of a zone, one for each DNS server. */
    private static function jobs(Request $request, DnsZone $zone): array
    {
        $count = max(1, Zones::servers()->count());

        return JobResource::collection($zone->jobs()->latest()->orderByDesc('id')->limit($count)->get())->resolve($request);
    }

    public static function presentRecord(DnsRecord $r): array
    {
        return [
            'id' => $r->id,
            'name' => $r->name,
            'type' => $r->type,
            'value' => $r->value,
            'ttl' => $r->ttl,
            'priority' => $r->priority,
            // "ns", "website" or "acme" for a record that the panel made, or null.
            'managed' => $r->managed,
        ];
    }

    /** @param  Collection<int, Server>  $servers  The servers that answer DNS queries. */
    public static function present(DnsZone $z, Collection $servers, bool $records = false): array
    {
        $ds = $z->ds();
        $account = $z->dns_account_id !== null ? $z->account : null;
        $state = $z->provider_state ?? [];
        $onPanel = $z->onPanelServers();

        return [
            'id' => $z->id,
            'name' => $z->name,
            'customerId' => $z->customer_id,
            // "active", "updating", "failed" or "removing".
            'status' => Zones::state($z, $servers),
            'serial' => $z->serial,
            'recordCount' => $z->records_count ?? $z->records()->count(),
            // The name servers for the registrar: of the panel, or of the external provider.
            'nameServers' => $account ? ($state['nameServers'] ?? []) : Zones::panelNameServersFor($z),
            // Null for a zone on the DNS servers of the panel.
            'provider' => $account ? [
                'accountId' => $account->id,
                'accountName' => $account->name,
                'name' => $account->providerName(),
                // The last change at the provider: "ok", "pending" or "failed".
                'state' => (($state['serial'] ?? 0) < $z->serial) ? 'pending' : ($state['state'] ?? 'pending'),
                'error' => $state['error'] ?? null,
                // True when the provider answers for the zone. Cloudflare, for example, waits
                // until the registrar has its name servers.
                'active' => (bool) ($state['active'] ?? false),
                'syncedAt' => $state['syncedAt'] ?? null,
                'changes' => $state['changes'] ?? null,
                // The records that the panel copied from the provider when the zone moved there.
                'imported' => $state['imported'] ?? null,
                // The DNS servers of the panel still answer for the zone that moved here.
                'handover' => $onPanel ? ['endsAt' => $z->handover_ends_at?->toIso8601String()] : null,
            ] : null,
            // What the public DNS says: "here" when the registrar sends the queries to the panel.
            'delegation' => [
                'state' => $z->delegation,
                'nameServers' => $z->delegation_name_servers ?? [],
                'checkedAt' => $z->delegation_checked_at?->toIso8601String(),
            ],
            'dnssec' => [
                'enabled' => $z->dnssec,
                // "off", "pending" (the provider makes the key) or "active".
                'state' => ! $z->dnssec ? 'off' : ($account ? ($state['dnssec']['state'] ?? 'pending') : 'active'),
                'ds' => $ds,
                'dnskey' => $z->dnssec && $z->dnssec_public_key ? Dnssec::dnskey($z->name, $z->dnssec_public_key) : null,
                // The change of the key, and the DS records at the registry (Rollover). Null
                // for a zone at an external DNS provider.
                ...Rollover::present($z),
            ],
            // The zone transfers to the secondary DNS servers of the owner. Null for a zone at an
            // external provider. "primaries" are the addresses that a secondary copies the zone
            // from: the addresses of the DNS servers of the panel.
            'transfers' => $account ? null : [
                'addresses' => array_values($z->transfer_addresses ?? []),
                'notify' => $z->transfer_notify,
                'nameServers' => array_values($z->transfer_name_servers ?? []),
                'tsig' => $z->transfer_tsig_algorithm ? ['name' => $z->transferKeyName(), 'algorithm' => $z->transfer_tsig_algorithm] : null,
                'primaries' => $servers->flatMap(fn (Server $s) => $s->addresses ?? [])->unique()->values()->all(),
            ],
            // The state of the zone on each name server of the panel: "ok", "pending" or "failed".
            'servers' => ($onPanel ? $servers : collect())->map(fn (Server $s) => [
                'nameServer' => Branding::nameServerName($z->customer, $s->dns_name),
                'state' => (($z->sync[$s->id]['serial'] ?? 0) < $z->serial) ? 'pending' : ($z->sync[$s->id]['state'] ?? 'pending'),
            ])->values()->all(),
            // The websites of the customer that have a domain in the zone.
            'websites' => Website::where('customer_id', $z->customer_id)
                ->whereHas('domains', fn ($q) => $q->where('name', $z->name)->orWhereLike('name', '%.'.$z->name))->count(),
            'createdAt' => $z->created_at?->toIso8601String(),
            ...($records ? ['records' => $z->records()->orderByRaw("case when name = '@' then 0 else 1 end")->orderBy('name')->orderBy('type')->orderBy('value')->get()->map(fn (DnsRecord $r) => self::presentRecord($r))->all()] : []),
        ];
    }
}
