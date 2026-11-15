<?php

namespace App\Http\Controllers\Api;

use App\Http\Controllers\Controller;
use App\Http\Resources\JobResource;
use App\Models\Customer;
use App\Models\DnsRecord;
use App\Models\DnsZone;
use App\Models\Server;
use App\Models\Website;
use App\Services\Audit;
use App\Services\Branding;
use App\Services\Dns\Dnssec;
use App\Services\Dns\Records;
use App\Services\Dns\Zones;
use App\Validation\Input;
use Illuminate\Database\Eloquent\Builder;
use Illuminate\Http\JsonResponse;
use Illuminate\Http\Request;
use Illuminate\Support\Collection;
use Illuminate\Validation\ValidationException;

/**
 * DNS hosting: the zones of a customer and their records.
 *
 * A customer makes a zone for a domain, and then gives the name servers of the panel to the
 * registrar of the domain. Each change of a zone goes to each DNS server as a job. The panel
 * makes the NS records of the zone, and the A and AAAA records of the websites of the customer.
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
        $zones = $this->visible($request)->withCount('records')
            ->when($request->query('customerId'), fn ($q, $id) => $q->where('customer_id', $id))
            ->orderBy('name')->get();

        return response()->json([
            'data' => $zones->map(fn (DnsZone $z) => self::present($z, $servers)),
            // The customer gives these host names to the registrar of each domain. A customer of
            // a reseller sees the host names of the reseller (white label).
            'nameServers' => $request->user()->isAdministrator() ? $servers->pluck('dns_name')->unique()->values()->all() : Branding::nameServersFor($request->user()->customer),
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
        ]);
        $user = $request->user();
        $customerId = $data['customerId'] ?? $user->customer_id;
        if (! $customerId || ! $user->canSeeCustomer($customerId)) {
            throw ValidationException::withMessages(['customerId' => 'Select a customer that you control.']);
        }
        $customer = Customer::with('plan')->findOrFail($customerId);
        abort_if($customer->isSuspended(), 409, 'This customer is suspended. Lift the suspension before you make a DNS zone.');

        $zone = $this->zones->create($customer, $data['name'], $user);
        Audit::record($request, 'Created DNS zone', $zone->name, $customer->id);

        return response()->json(['zone' => self::present($zone->fresh(), Zones::servers(), records: true), 'jobs' => self::jobs($request, $zone)], 202);
    }

    /** Removes a zone from the DNS servers, with all its records. */
    public function destroy(Request $request, string $id): JsonResponse
    {
        $zone = $this->find($request, $id);
        $jobs = $this->zones->remove($zone);
        Audit::record($request, 'Removed DNS zone', $zone->name, $zone->customer_id);

        return response()->json(['jobs' => JobResource::collection(collect($jobs))->resolve($request)], 202);
    }

    public function storeRecord(Request $request, string $id): JsonResponse
    {
        $zone = $this->findActive($request, $id);
        $record = $zone->records()->create(Records::check($zone, self::recordInput($request)));
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

    /** Asks the public DNS again if the name servers of the zone are the name servers of the panel. */
    public function check(Request $request, string $id): JsonResponse
    {
        $zone = $this->findActive($request, $id);
        $this->zones->checkDelegation($zone);

        return response()->json(self::present($zone->fresh(), Zones::servers(), records: true));
    }

    /** Sends the zone to each DNS server again, for example after a job failed. */
    public function sync(Request $request, string $id): JsonResponse
    {
        $zone = $this->findActive($request, $id);
        $jobs = $this->zones->changed($zone, "Send DNS zone {$zone->name} again");

        return response()->json(['zone' => self::present($zone->fresh(), Zones::servers(), records: true), 'jobs' => JobResource::collection(collect($jobs))->resolve($request)], 202);
    }

    /** A record that a user can change. The panel keeps the NS records of the zone itself. */
    private static function editable(DnsZone $zone, string $recordId): DnsRecord
    {
        $record = $zone->records()->findOrFail($recordId);
        if ($record->managed === DnsRecord::NS) {
            throw ValidationException::withMessages(['record' => 'The panel keeps the name servers of the zone. You cannot change this record.']);
        }

        return $record;
    }

    /** @return array<string, mixed> */
    private static function recordInput(Request $request): array
    {
        return $request->validate([
            'type' => ['sometimes', 'string', 'max:10'],
            'name' => ['sometimes', 'nullable', 'string', 'max:255'],
            'value' => ['sometimes', 'string', 'max:4096'],
            'ttl' => ['sometimes', 'integer'],
            'priority' => ['sometimes', 'nullable', 'integer'],
        ]);
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
            // "ns" or "website" for a record that the panel made, or null.
            'managed' => $r->managed,
        ];
    }

    /** @param  Collection<int, Server>  $servers  The servers that answer DNS queries. */
    public static function present(DnsZone $z, Collection $servers, bool $records = false): array
    {
        $ds = $z->ds();

        return [
            'id' => $z->id,
            'name' => $z->name,
            'customerId' => $z->customer_id,
            // "active", "updating", "failed" or "removing".
            'status' => Zones::state($z, $servers),
            'serial' => $z->serial,
            'recordCount' => $z->records_count ?? $z->records()->count(),
            'nameServers' => Branding::nameServersFor($z->customer),
            // What the public DNS says: "here" when the registrar sends the queries to the panel.
            'delegation' => [
                'state' => $z->delegation,
                'nameServers' => $z->delegation_name_servers ?? [],
                'checkedAt' => $z->delegation_checked_at?->toIso8601String(),
            ],
            'dnssec' => [
                'enabled' => $z->dnssec,
                'ds' => $ds,
                'dnskey' => $z->dnssec && $z->dnssec_public_key ? Dnssec::dnskey($z->name, $z->dnssec_public_key) : null,
            ],
            // The state of the zone on each name server: "ok", "pending" or "failed".
            'servers' => $servers->map(fn (Server $s) => [
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
