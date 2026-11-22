<?php

namespace App\Services\Dns;

use App\Enums\AgentAction;
use App\Enums\JobState;
use App\Http\Controllers\Api\DomainController;
use App\Jobs\SyncDnsZone;
use App\Models\AgentJob;
use App\Models\Customer;
use App\Models\DnsAccount;
use App\Models\DnsRecord;
use App\Models\DnsZone;
use App\Models\Domain;
use App\Models\Server;
use App\Models\User;
use App\Models\Website;
use App\Services\Audit;
use App\Services\Branding;
use App\Services\DomainPolicy;
use App\Services\JobDispatcher;
use App\Validation\InputRules;
use Illuminate\Database\UniqueConstraintViolationException;
use Illuminate\Support\Carbon;
use Illuminate\Support\Collection;
use Illuminate\Support\Facades\DB;
use Illuminate\Validation\ValidationException;

/**
 * DNS hosting: the zones of the customers and the servers that answer for them.
 *
 * - Each server with the role "dns" and a name server host name answers for all zones. The
 *   panel sends the full zone to each of these servers after each change (SetDnsZones). The
 *   servers do not transfer zones between them.
 * - A zone belongs to one customer. A customer makes a zone only after it proves control of
 *   the name in the public DNS (checkName). The name servers are the same for all customers,
 *   so a domain that points to them does not show which customer controls it.
 * - The panel makes the NS records of each zone, and the A and AAAA records of the domains
 *   of the websites of the customer.
 * - A zone with a DNS account goes to an external DNS provider, for example Cloudflare, and
 *   not to the DNS servers of the panel (Providers). The panel still keeps the records. The
 *   NS records of the zone are the name servers that the provider gives.
 */
class Zones
{
    /** The TTL of the records that the panel makes for a website: 5 minutes. */
    public const WEBSITE_TTL = 300;

    /** The TTL of the NS records of a zone: one hour. */
    public const NS_TTL = 3600;

    /**
     * The time that the DNS servers of the panel keep a zone that moved to a provider, after
     * the public DNS stops giving the name servers of the panel: two days. Resolvers can keep
     * the old NS records for that time.
     */
    public const HANDOVER_HOURS = 48;

    public function __construct(private JobDispatcher $jobs, private Lookup $lookup, private DomainPolicy $policy, private Providers $providers) {}

    /**
     * The servers that answer DNS queries: a server with the role "dns", an agent that can
     * operate DNS, and a host name as a name server.
     *
     * @return Collection<int, Server>
     */
    public static function servers(): Collection
    {
        return Server::whereJsonContains('roles', 'dns')->where('dns', true)->whereNotNull('dns_name')->orderBy('dns_name')->get();
    }

    /**
     * The host names of the name servers. The customer gives them to the registrar of a domain.
     *
     * @return list<string>
     */
    public static function nameServers(): array
    {
        return self::servers()->pluck('dns_name')->unique()->values()->all();
    }

    /** The zone of a customer that has the name, or null. The longest zone name wins. */
    public static function zoneOf(string $customerId, string $name): ?DnsZone
    {
        return DnsZone::where('customer_id', $customerId)->where('status', DnsZone::ACTIVE)->get()
            ->filter(fn (DnsZone $z) => $z->contains($name))
            ->sortByDesc(fn (DnsZone $z) => strlen($z->name))->first();
    }

    /**
     * True when the customer has a zone with the name in it, and the public DNS sends the
     * queries for that zone to the panel. The customer then controls the name.
     */
    public static function controls(string $customerId, string $name): bool
    {
        return self::zoneOf($customerId, $name)?->delegatedHere() ?? false;
    }

    /**
     * Makes a zone for a customer, with its NS records and the records of the websites of the
     * customer, and sends it to the DNS servers. With a DNS account, the zone goes to the
     * provider of the account. With "importRecords", the zone first gets the records that the
     * provider has for the name now. The result of that copy is in "provider_state.imported".
     *
     * @throws ValidationException
     */
    public function create(Customer $customer, string $name, User $user, ?DnsAccount $account = null, bool $importRecords = true): DnsZone
    {
        if ($account === null && self::nameServers() === []) {
            throw ValidationException::withMessages(['name' => $user->isAdministrator()
                ? 'The panel has no DNS server. Give a server the role "DNS" and a name server host name on the page of the server.'
                : 'DNS hosting is not available. Ask your provider.']);
        }
        $other = $this->checkName($name, $customer, $user);
        // The provider must answer before the panel makes the zone. A wrong token then stops
        // here, and the panel does not make a zone that cannot go to the provider.
        $remote = $account && $importRecords ? $this->providers->records($account, $name) : null;
        try {
            $zone = DB::transaction(function () use ($customer, $name, $other, $account) {
                // The count and the new zone are in one transaction, with a lock on the customer.
                $customer = Customer::with('plan')->lockForUpdate()->findOrFail($customer->id);
                self::checkLimit($customer);
                // Adversarial review, finding A6-08: the zone of the other customer goes only
                // when the new zone comes. The DNS servers get the new zone with the same
                // name, so the panel removes only its record.
                $other?->delete();

                return DnsZone::create(['customer_id' => $customer->id, 'name' => $name, 'serial' => now()->timestamp, 'status' => DnsZone::ACTIVE, 'dns_account_id' => $account?->id]);
            });
        } catch (UniqueConstraintViolationException) {
            throw ValidationException::withMessages(['name' => 'A zone with this name was made at the same time. Try again.']);
        }
        $taken = $other !== null;
        if ($taken) {
            Audit::system('Removed DNS zone, because a different customer proved control of the name', $name);
        }
        self::writeNameServers($zone);
        foreach (self::websiteDomains($zone) as [$website, $domain]) {
            self::writeWebsiteRecords($zone, $website, $domain);
        }
        if ($remote !== null) {
            $this->importRemote($zone, $remote);
        }
        $this->checkDelegation($zone);
        $this->push($zone, "Create DNS zone {$zone->name}");

        return $zone;
    }

    /**
     * Moves a zone to a different DNS provider, or back to the DNS servers of the panel. The
     * records stay. The customer then gives the new name servers to the registrar.
     *
     * - From the DNS servers of the panel to a provider: the DNS servers of the panel keep the
     *   zone in a handover time, until HANDOVER_HOURS after the public DNS stops giving their
     *   names. They answer with the NS records of the provider.
     * - From a provider: the zone stays at the old provider, with its records of the time of
     *   the move. The customer removes it there after the registrar has the new name servers.
     * - A zone with DNSSEC does not move: the new provider signs with a different key, and the
     *   DS record at the registrar must change first.
     *
     * @return list<AgentJob>
     *
     * @throws ValidationException
     */
    public function move(DnsZone $zone, ?DnsAccount $account, bool $importRecords = true): array
    {
        if ($zone->dns_account_id === $account?->id) {
            throw ValidationException::withMessages(['accountId' => $account ? 'The zone uses this DNS account already.' : 'The zone uses the name servers of the panel already.']);
        }
        if ($zone->dnssec) {
            throw ValidationException::withMessages(['accountId' => 'Turn off DNSSEC before you move the zone. First remove the DS record at the registrar, and wait one day.']);
        }
        if ($account === null && self::nameServers() === []) {
            throw ValidationException::withMessages(['accountId' => 'The panel has no DNS server.']);
        }
        $remote = $account && $importRecords ? $this->providers->records($account, $zone->name) : null;
        $from = $zone->account?->providerName() ?? 'the DNS servers of the panel';
        $handover = $account !== null && $zone->onPanelServers();
        $zone->update([
            'dns_account_id' => $account?->id,
            'provider_state' => null,
            'handover' => $handover,
            'handover_ends_at' => null,
        ]);
        $zone->unsetRelation('account');
        // Back to the panel: the NS records are the name servers of the panel again. To a
        // provider: the NS records stay until the provider gives its name servers.
        self::writeNameServers($zone);
        if ($remote !== null) {
            $this->importRemote($zone, $remote);
        }
        $this->checkDelegation($zone);
        $to = $account?->providerName() ?? 'the DNS servers of the panel';

        return $this->changed($zone, "Move DNS zone {$zone->name} from {$from} to {$to}");
    }

    /**
     * Adds the records that the provider had to the zone (Import::apply), and keeps the result
     * in "provider_state.imported" for the user.
     *
     * @param  list<array<string, mixed>>  $remote
     */
    private function importRemote(DnsZone $zone, array $remote): void
    {
        $result = DB::transaction(function () use ($zone, $remote) {
            DnsZone::whereKey($zone->id)->lockForUpdate()->first();

            return Import::apply($zone, $remote);
        });
        $zone->update(['provider_state' => [...($zone->provider_state ?? []), 'imported' => [
            'added' => count($result['added']),
            'existing' => $result['existing'],
            'skipped' => array_slice($result['skipped'], 0, 50),
        ]]]);
    }

    /**
     * Refuses a zone name that the customer must not use. It returns the zone of a different
     * customer that has the same name, when this customer proved control of the name. The
     * caller removes that zone when it makes the new zone.
     *
     * - Only an administrator makes a zone for the domain of the panel, the host name of a
     *   server or of a name server, or for a name above one of them.
     * - A customer proves control of each new zone name. The proof is a TXT record in the
     *   public DNS, a zone of the same customer that has the name in it, or a verified domain
     *   of a website of the customer. All customers use the same name servers. Thus a domain
     *   that points to the panel does not prove which customer controls it, and a name below
     *   the domain of a different person is not free to take (adversarial review, findings
     *   A6-01 and A6-02).
     * - A name that a different customer has as a zone, or a name in such a zone or above it:
     *   the TXT record is the proof. When the queries for the other zone do not come to the
     *   panel, a verified website domain of the customer is also a proof, and so is a zone of
     *   the customer that the public DNS sends to the panel.
     *
     * @throws ValidationException
     */
    public function checkName(string $name, Customer $customer, User $user): ?DnsZone
    {
        if (! $user->isAdministrator()) {
            foreach ([...$this->policy->reserved(), ...self::nameServers()] as $reserved) {
                if (DomainPolicy::within($name, $reserved) || DomainPolicy::within($reserved, $name)) {
                    throw ValidationException::withMessages(['name' => 'This name is reserved for the panel or a server.']);
                }
            }
        }
        $taken = null;
        // The zones with this name, with a name above it, or with a name in it.
        $labels = explode('.', $name);
        $above = array_map(fn (int $i) => implode('.', array_slice($labels, $i)), array_keys($labels));
        $overlap = DnsZone::whereIn('name', $above)->orWhereLike('name', '%.'.$name)->get()
            ->filter(fn (DnsZone $z) => $z->name === $name || $z->customer_id !== $customer->id);
        foreach ($overlap as $other) {
            if ($other->customer_id === $customer->id) {
                throw ValidationException::withMessages(['name' => $other->status === DnsZone::REMOVING
                    ? 'The panel removes a zone with this name. Try again in one minute.'
                    : 'You have this zone already.']);
            }
        }
        $administrator = $user->isAdministrator();
        // The TXT record is the proof that no other customer can copy: its value is a keyed
        // hash of the customer and the name.
        $txt = ! $administrator && $this->policy->proves($customer->id, $name);
        // A proof that the customer gave before: a zone of the customer that has the name in
        // it, or a verified domain of a website of the customer.
        $before = ! $administrator && ! $txt && $this->provedBefore($customer->id, $name);
        $challenge = " add a TXT record _cloudfactory-challenge.{$name} with the value {$this->policy->claimToken($customer->id, $name)} at your current DNS provider, and then try again. DNS changes can take some minutes.";
        if (! $administrator && ! $txt && ! $before && $overlap->isEmpty()) {
            throw ValidationException::withMessages(['name' => 'Prove that you control this domain. To do that,'.$challenge
                .' If the domain uses the name servers of this panel already, speak to your provider.']);
        }
        foreach ($overlap as $other) {
            $proved = $administrator || $txt || (! $other->delegatedHere() && $this->policy->preVerified($user, $customer->id, $name));
            if (! $proved) {
                // Adversarial review, finding A6-12: the text does not tell which names
                // other customers have.
                throw ValidationException::withMessages(['name' => 'Prove that you control this domain. To do that,'.$challenge
                    .' If the domain uses the name servers of this panel already, speak to your provider.']);
            }
            if ($other->name === $name) {
                if ($other->status === DnsZone::REMOVING) {
                    throw ValidationException::withMessages(['name' => 'The panel removes a zone with this name. Try again in one minute.']);
                }
                $taken = $other;
            }
        }

        return $taken;
    }

    /**
     * True when the customer gave a proof of control for the name before: the customer has an
     * active zone with the name in it, or a verified domain of a website that is the name or
     * is above it.
     */
    private function provedBefore(string $customerId, string $name): bool
    {
        if (self::zoneOf($customerId, $name) !== null) {
            return true;
        }

        return Domain::whereNotNull('verified_at')
            ->whereHas('website', fn ($q) => $q->where('customer_id', $customerId))
            ->pluck('name')
            ->contains(fn (string $verified) => DomainPolicy::within($name, $verified));
    }

    /**
     * The plan of a customer gives the number of its zones. The zones of the customers of a
     * reseller count against the plan of the reseller too. The caller holds the lock on the
     * customer row.
     *
     * @throws ValidationException
     */
    public static function checkLimit(Customer $customer): void
    {
        $allowed = $customer->plan->dns_zones;
        if (DnsZone::where('customer_id', $customer->id)->count() >= $allowed) {
            throw ValidationException::withMessages(['name' => "The {$customer->plan->name} plan allows {$allowed} DNS ".($allowed === 1 ? 'zone' : 'zones').'.']);
        }
        if ($customer->reseller_id || $customer->role === 'reseller') {
            $reseller = $customer->reseller_id ? Customer::with('plan')->lockForUpdate()->findOrFail($customer->reseller_id) : $customer;
            $total = $reseller->plan->dns_zones;
            if (self::countFor($reseller) >= $total) {
                throw ValidationException::withMessages(['name' => $customer->reseller_id
                    ? "The {$reseller->plan->name} plan of your provider allows {$total} DNS ".($total === 1 ? 'zone' : 'zones').' in total.'
                    : "The {$reseller->plan->name} plan allows {$total} DNS ".($total === 1 ? 'zone' : 'zones').' in total, for you and your customers.']);
            }
        }
    }

    /** The number of zones that count against the plan of a customer. */
    public static function countFor(Customer $customer): int
    {
        $ids = $customer->role === 'reseller'
            ? Customer::where('id', $customer->id)->orWhere('reseller_id', $customer->id)->pluck('id')->all()
            : [$customer->id];

        return DnsZone::whereIn('customer_id', $ids)->count();
    }

    /**
     * Records a change of the records of a zone: the serial becomes larger, and each DNS
     * server gets the zone again.
     *
     * @return list<AgentJob>
     */
    public function changed(DnsZone $zone, string $title): array
    {
        self::bump($zone);

        return $this->push($zone, $title);
    }

    /** The largest serial of a DNS zone: 32 bits. */
    public const MAX_SERIAL = 4294967295;

    /** The panel follows a serial from a server only when it is not more than this in the future, in seconds. */
    public const SERIAL_MARGIN = 86400;

    /** Gives a zone a larger serial. The serial is the time of the change, in seconds. */
    private static function bump(DnsZone $zone): void
    {
        $zone->update(['serial' => min(self::MAX_SERIAL, max($zone->serial + 1, now()->timestamp))]);
    }

    /**
     * Sends one zone to each DNS server, with one job for each server. A zone with a DNS
     * account goes to its provider (SyncDnsZone), and to the DNS servers of the panel only in
     * its handover time.
     *
     * @return list<AgentJob>
     */
    public function push(DnsZone $zone, string $title): array
    {
        $jobs = [];
        if ($zone->onPanelServers()) {
            $servers = self::servers();
            self::mark($zone, $servers->pluck('id')->all(), $zone->serial, 'pending');
            foreach ($servers as $server) {
                $jobs[] = $this->jobs->dispatch($server, AgentAction::SetDnsZones, ['zones' => [self::forAgent($zone)]], $title, dnsZone: $zone);
            }
        }
        if ($zone->dns_account_id !== null) {
            self::markProvider($zone, $zone->serial, ['state' => 'pending']);
            SyncDnsZone::dispatch($zone->id)->afterCommit();
        }

        return $jobs;
    }

    /**
     * Records the state of a zone at its provider. A result for an old serial does not change
     * the record of a newer serial.
     *
     * @param  array<string, mixed>  $fields
     */
    public static function markProvider(DnsZone $zone, int $serial, array $fields): void
    {
        DB::transaction(function () use ($zone, $serial, $fields) {
            $current = DnsZone::lockForUpdate()->find($zone->id);
            if (! $current) {
                return;
            }
            $state = $current->provider_state ?? [];
            if (($state['serial'] ?? 0) > $serial) {
                return;
            }
            $state = array_filter(['serial' => $serial, ...$fields] + $state, fn ($v) => $v !== null);
            $current->update(['provider_state' => $state]);
            $zone->setAttribute('provider_state', $state)->syncOriginalAttribute('provider_state');
        });
    }

    /**
     * A reseller changed the host names of its name servers. The zones of the reseller and of
     * its customers get new NS records, and the DNS servers get the zones again.
     */
    public function resellerNameServersChanged(Customer $reseller): void
    {
        $ids = Customer::where('id', $reseller->id)->orWhere('reseller_id', $reseller->id)->pluck('id');
        foreach (DnsZone::with('customer')->whereIn('customer_id', $ids)->where('status', DnsZone::ACTIVE)->whereNull('dns_account_id')->orderBy('name')->get() as $zone) {
            self::writeNameServers($zone);
            $this->changed($zone, "Update the name servers of DNS zone {$zone->name}");
        }
    }

    /**
     * Sends all zones to all DNS servers, after a change of the name servers. Each zone gets
     * the new NS records. One job has at most MAX_DNS_ZONES_PER_JOB zones.
     */
    public function pushAll(): void
    {
        $servers = self::servers();
        DnsZone::where('status', DnsZone::ACTIVE)->onPanelServers()->orderBy('name')->chunkById(InputRules::MAX_DNS_ZONES_PER_JOB, function (Collection $zones) use ($servers) {
            foreach ($zones as $zone) {
                self::writeNameServers($zone);
                self::bump($zone);
                self::mark($zone, $servers->pluck('id')->all(), $zone->serial, 'pending');
            }
            foreach ($servers as $server) {
                $count = $zones->count();
                $this->jobs->dispatch($server, AgentAction::SetDnsZones, ['zones' => $zones->map(fn (DnsZone $z) => self::forAgent($z))->values()->all()],
                    $count === 1 ? "Update DNS zone {$zones->first()->name}" : "Update {$count} DNS zones on {$server->name}");
            }
        });
    }

    /**
     * Removes all zones from a server that does not answer DNS queries now, for example
     * because an administrator removed its role.
     */
    public function retire(Server $server): void
    {
        DnsZone::orderBy('name')->pluck('name')->chunk(InputRules::MAX_DNS_ZONES_PER_JOB)->each(function (Collection $names) use ($server) {
            $this->jobs->dispatch($server, AgentAction::DeleteDnsZones, ['names' => $names->values()->all()], "Remove the DNS zones from {$server->name}");
        });
        foreach (DnsZone::whereNotNull('sync')->get() as $zone) {
            $zone->update(['sync' => array_diff_key($zone->sync ?? [], [$server->id => true]) ?: null]);
        }
    }

    /** The longest time between two tries to send zones again to one server, in minutes. */
    public const RESYNC_MAX_MINUTES = 60;

    /**
     * Compares the zones that a server reported with the zones of the panel, and corrects the
     * server. The agent sends the report each minute and when it starts. Thus a DNS server
     * that was offline, that lost its data or that could not load a zone gets the last zones
     * with no work of an administrator.
     *
     * - A zone that the server does not have, or has with a different serial, goes to the
     *   server again. So does a zone whose last job on the server failed.
     * - A zone that the panel does not have, or that the panel removes, goes from the server.
     * - A server that does not answer DNS queries now loses all its zones.
     *
     * The panel does nothing while a DNS job of the server is in the queue or runs. After a
     * try with no result, it waits 1 minute, then 2, then 4, up to RESYNC_MAX_MINUTES.
     *
     * @param  list<array{name: string, serial: int}>  $reported
     */
    public function reconcile(Server $server, array $reported): void
    {
        $has = collect($reported)->mapWithKeys(fn (array $z) => [(string) $z['name'] => (int) $z['serial']]);
        $answers = self::servers()->contains('id', $server->id);
        $send = collect();
        $healed = [];
        if ($answers) {
            foreach (DnsZone::where('status', DnsZone::ACTIVE)->onPanelServers()->orderBy('name')->get() as $zone) {
                $sync = $zone->sync[$server->id] ?? null;
                // The server has a newer zone than the panel, for example after a restore of
                // the panel database. The server refuses an older zone, so the zone gets a
                // larger serial. Each other DNS server gets it after its next report.
                // Adversarial review, finding A3-01: the panel makes serials from the clock. It
                // does not follow a serial that is far in the future: a server could send
                // such a serial for each zone and stop the DNS changes of all customers.
                $reportedSerial = (int) $has->get($zone->name, 0);
                if ($reportedSerial > $zone->serial && $reportedSerial <= now()->timestamp + self::SERIAL_MARGIN) {
                    $zone->update(['serial' => min(self::MAX_SERIAL, max($reportedSerial + 1, now()->timestamp))]);
                }
                $current = $has->get($zone->name) === $zone->serial;
                if (! $current || ($sync['state'] ?? '') === 'failed') {
                    $send->push($zone);
                } elseif ($sync === null || ($sync['serial'] ?? 0) < $zone->serial || ($sync['state'] ?? '') !== 'ok') {
                    // The server has the zone, but the panel did not get the result of its job.
                    $healed[] = $zone;
                }
            }
        }
        // A zone at an external provider goes from the server after its handover time.
        $keep = $answers ? DnsZone::where('status', DnsZone::ACTIVE)->onPanelServers()->pluck('name')->flip() : collect();
        $remove = $has->keys()->reject(fn (string $name) => $keep->has($name))->values();

        if ($send->isEmpty() && $remove->isEmpty()) {
            foreach ($healed as $zone) {
                if (! self::busy($server)) {
                    self::mark($zone, [$server->id], $zone->serial, 'ok');
                }
            }
            if ($server->dns_resync !== null) {
                $server->update(['dns_resync' => null]);
            }

            return;
        }
        if (self::busy($server)) {
            return;
        }
        $tries = (int) ($server->dns_resync['tries'] ?? 0);
        $last = $server->dns_resync['at'] ?? null;
        $wait = $tries === 0 ? 0 : min(self::RESYNC_MAX_MINUTES, 2 ** ($tries - 1));
        if ($last !== null && now()->lt(Carbon::parse($last)->addMinutes($wait))) {
            return;
        }
        $server->update(['dns_resync' => ['at' => now()->toIso8601String(), 'tries' => $tries + 1]]);

        foreach ($send->chunk(InputRules::MAX_DNS_ZONES_PER_JOB) as $zones) {
            foreach ($zones as $zone) {
                self::mark($zone, [$server->id], $zone->serial, 'pending');
            }
            $count = $zones->count();
            $this->jobs->dispatch($server, AgentAction::SetDnsZones, ['zones' => $zones->map(fn (DnsZone $z) => self::forAgent($z))->values()->all()],
                $count === 1 ? "Send DNS zone {$zones->first()->name} to {$server->name} again" : "Send {$count} DNS zones to {$server->name} again",
                dnsZone: $count === 1 ? $zones->first() : null);
        }
        foreach ($remove->chunk(InputRules::MAX_DNS_ZONES_PER_JOB) as $names) {
            $count = $names->count();
            $this->jobs->dispatch($server, AgentAction::DeleteDnsZones, ['names' => $names->values()->all()],
                $count === 1 ? "Remove DNS zone {$names->first()} from {$server->name}" : "Remove {$count} DNS zones from {$server->name}");
        }
    }

    /** True while a DNS job of the server is in the queue or runs. */
    private static function busy(Server $server): bool
    {
        return AgentJob::where('server_id', $server->id)
            ->whereIn('action', [AgentAction::SetDnsZones->value, AgentAction::DeleteDnsZones->value])
            ->whereIn('state', [JobState::Queued->value, JobState::Running->value])->exists();
    }

    /**
     * Removes a zone. The record stays, with the state "removing", until each DNS server
     * removed the zone. A zone at an external provider goes from the provider first, unless
     * "keepAtProvider": then only the panel stops to change it.
     *
     * @return list<AgentJob>
     *
     * @throws ValidationException
     */
    public function remove(DnsZone $zone, bool $keepAtProvider = false): array
    {
        if ($zone->account && ! $keepAtProvider) {
            $error = $this->providers->remove($zone->account, $zone->name);
            if ($error !== null) {
                $name = $zone->account->providerName();
                throw ValidationException::withMessages(['zone' => "The panel could not remove the zone at {$name}. {$error} Try again, or keep the zone at {$name}."]);
            }
        }
        $servers = $zone->onPanelServers() ? self::servers() : collect();
        if ($servers->isEmpty()) {
            $zone->delete();

            return [];
        }
        $zone->update(['status' => DnsZone::REMOVING]);
        self::mark($zone, $servers->pluck('id')->all(), $zone->serial, 'pending');
        $jobs = [];
        foreach ($servers as $server) {
            $jobs[] = $this->jobs->dispatch($server, AgentAction::DeleteDnsZones, ['names' => [$zone->name]], "Remove DNS zone {$zone->name}", dnsZone: $zone);
        }

        return $jobs;
    }

    /**
     * Turns DNSSEC on or off for a zone. The panel makes a new key each time DNSSEC is turned
     * on, so the customer gives the new DS record to the registrar.
     *
     * @return list<AgentJob>
     */
    public function setDnssec(DnsZone $zone, bool $enabled): array
    {
        if ($enabled === $zone->dnssec) {
            return [];
        }
        if ($zone->account) {
            // The provider signs the zone with its own key, and gives the DS record.
            $name = $zone->account->providerName();
            if ($enabled && ! ($zone->account->definition()['dnssec'] ?? false)) {
                throw ValidationException::withMessages(['enabled' => "The plugin of {$name} cannot turn on DNSSEC."]);
            }
            if ($enabled && $zone->onPanelServers()) {
                throw ValidationException::withMessages(['enabled' => "Turn on DNSSEC when the handover to {$name} is complete. The DNS servers of the panel still answer for the zone, and they do not have the key of {$name}."]);
            }
            $zone->update(['dnssec' => $enabled]);

            return $this->changed($zone, ($enabled ? 'Turn on' : 'Turn off')." DNSSEC for {$zone->name} at {$name}");
        }
        if ($enabled) {
            $key = Dnssec::generate();
            $zone->update(['dnssec' => true, 'dnssec_key' => $key['key'], 'dnssec_public_key' => $key['publicKey'], 'dnssec_key_tag' => $key['keyTag']]);
        } else {
            $zone->update(['dnssec' => false, 'dnssec_key' => null, 'dnssec_public_key' => null, 'dnssec_key_tag' => null]);
        }

        return $this->changed($zone, ($enabled ? 'Turn on' : 'Turn off')." DNSSEC for {$zone->name}");
    }

    /**
     * Asks the public DNS for the name servers of the zone, and records the answer. When the
     * queries come to the panel now, the customer controls the zone: the panel then verifies
     * the domains of the websites of the customer in the zone.
     */
    public function checkDelegation(DnsZone $zone): void
    {
        $seen = $this->lookup->nameServers($zone->name);
        // White label: the host names of the panel, or the host names that the reseller of
        // the customer gave to the same name servers.
        $panel = array_values(array_unique([...self::nameServers(), ...Branding::nameServersFor($zone->customer)]));
        // A zone at an external provider: the name servers that the provider gives the zone.
        $ours = $zone->dns_account_id !== null ? ($zone->provider_state['nameServers'] ?? []) : $panel;
        $state = match (true) {
            $seen === [] => DnsZone::NONE,
            $ours !== [] && array_diff($seen, $ours) === [] => DnsZone::HERE,
            default => DnsZone::ELSEWHERE,
        };
        $was = $zone->delegation;
        $changes = ['delegation' => $state, 'delegation_name_servers' => $seen, 'delegation_checked_at' => now()];
        // The handover time starts when the public DNS stops giving the name servers of the panel.
        if ($zone->handover && $zone->handover_ends_at === null && $seen !== [] && array_intersect($seen, $panel) === []) {
            $changes['handover_ends_at'] = now()->addHours(self::HANDOVER_HOURS);
        }
        $zone->update($changes);
        if ($state === DnsZone::HERE && $was !== DnsZone::HERE) {
            $this->verifyDomains($zone);
        }
    }

    /** Verifies the domains of the websites of the customer in a zone that is delegated to the panel. */
    private function verifyDomains(DnsZone $zone): void
    {
        $domains = Domain::with('website.server')->whereNull('verified_at')
            ->whereHas('website', fn ($q) => $q->where('customer_id', $zone->customer_id))->get()
            ->filter(fn (Domain $d) => self::zoneOf($zone->customer_id, $d->name)?->is($zone));
        foreach ($domains as $domain) {
            $domain->update(['verified_at' => now()]);
            DomainController::serve($this->jobs, $domain->website, $domain);
        }
    }

    /**
     * A website got a domain. When the customer has a zone with the name in it, the zone gets
     * the A and AAAA records of the server of the website.
     */
    public function websiteDomainAdded(Website $website, string $name): void
    {
        $zone = self::zoneOf($website->customer_id, $name);
        if ($zone && self::writeWebsiteRecords($zone, $website, $name)) {
            $this->changed($zone, "Add the DNS records of {$name}");
        }
    }

    /** A website lost a domain. The panel removes the records that it made for the name. */
    public function websiteDomainRemoved(string $customerId, string $name): void
    {
        $zone = self::zoneOf($customerId, $name);
        if ($zone && $zone->records()->where(['name' => $zone->relative($name), 'managed' => DnsRecord::WEBSITE])->delete() > 0) {
            $this->changed($zone, "Remove the DNS records of {$name}");
        }
    }

    /** A server reported new public addresses. The records of its websites get them. */
    public function serverAddressesChanged(Server $server): void
    {
        $changed = [];
        foreach (Website::with('domains')->where('server_id', $server->id)->get() as $website) {
            foreach ($website->domains as $domain) {
                $zone = self::zoneOf($website->customer_id, $domain->name);
                $name = $zone?->relative($domain->name);
                if ($zone && $zone->records()->where(['name' => $name, 'managed' => DnsRecord::WEBSITE])->exists()) {
                    $zone->records()->where(['name' => $name, 'managed' => DnsRecord::WEBSITE])->delete();
                    self::writeWebsiteRecords($zone, $website->setRelation('server', $server), $domain->name);
                    $changed[$zone->id] = $zone;
                }
            }
        }
        foreach ($changed as $zone) {
            $this->changed($zone, "Update the DNS records of the websites on {$server->name}");
        }
    }

    /**
     * The domains of the websites of the customer of a zone that are in the zone.
     *
     * @return list<array{0: Website, 1: string}>
     */
    private static function websiteDomains(DnsZone $zone): array
    {
        $found = [];
        foreach (Website::with(['domains', 'server'])->where('customer_id', $zone->customer_id)->get() as $website) {
            foreach ($website->domains as $domain) {
                if (self::zoneOf($zone->customer_id, $domain->name)?->is($zone)) {
                    $found[] = [$website, $domain->name];
                }
            }
        }

        return $found;
    }

    /**
     * Makes the A and AAAA records of a domain of a website, with the public addresses of the
     * server of the website. A name that has an address record or a CNAME record keeps its
     * records: the customer made them. It reports true when the zone changed.
     */
    private static function writeWebsiteRecords(DnsZone $zone, Website $website, string $domain): bool
    {
        $name = $zone->relative($domain);
        if ($zone->records()->where('name', $name)->whereIn('type', ['A', 'AAAA', 'CNAME'])->exists()) {
            return false;
        }
        $made = false;
        // Adversarial review, finding A6-05: the agent refuses a zone with too many records, or
        // with an IPv6 address that has an IPv4 part. Such a zone stops the jobs of other zones.
        $free = InputRules::MAX_DNS_RECORDS_PER_ZONE - $zone->records()->count();
        foreach ($website->server->addresses ?? [] as $address) {
            if ($free < 1 || (str_contains($address, ':') && str_contains($address, '.'))) {
                continue;
            }
            $free--;
            $type = filter_var($address, FILTER_VALIDATE_IP, FILTER_FLAG_IPV4) ? 'A' : (filter_var($address, FILTER_VALIDATE_IP, FILTER_FLAG_IPV6) ? 'AAAA' : null);
            if ($type !== null) {
                $zone->records()->create(['name' => $name, 'type' => $type, 'ttl' => self::WEBSITE_TTL, 'value' => strtolower($address), 'managed' => DnsRecord::WEBSITE]);
                $made = true;
            }
        }

        return $made;
    }

    /**
     * Writes the NS records of the zone itself: one for each name server of the panel. A zone
     * of a reseller, or of a customer of a reseller, gets the host names of the reseller when
     * it has its own names for the name servers (white label). A zone at an external provider
     * gets the name servers that the provider gives. Until the provider gives them, the records
     * stay. It reports true when the records changed.
     */
    public static function writeNameServers(DnsZone $zone): bool
    {
        $hosts = $zone->dns_account_id !== null ? ($zone->provider_state['nameServers'] ?? []) : Branding::nameServersFor($zone->customer);
        if ($hosts === []) {
            return false;
        }
        sort($hosts);
        $current = $zone->records()->where('managed', DnsRecord::NS)->pluck('value')->sort()->values()->all();
        if ($current === $hosts) {
            return false;
        }
        $zone->records()->where('managed', DnsRecord::NS)->delete();
        foreach ($hosts as $host) {
            $zone->records()->create(['name' => '@', 'type' => 'NS', 'ttl' => self::NS_TTL, 'value' => $host, 'managed' => DnsRecord::NS]);
        }

        return true;
    }

    /** The zone as the agent gets it: the full list of the records, and the DNSSEC key. */
    public static function forAgent(DnsZone $zone): array
    {
        return [
            'name' => $zone->name,
            'serial' => $zone->serial,
            'records' => $zone->records()->orderBy('name')->orderBy('type')->orderBy('id')->get()->map(fn (DnsRecord $r) => $r->forAgent())->all(),
            // Adversarial review, finding A6-13: the DNSSEC key is not in the job record. The
            // panel adds it each time that it sends the job (keysFor).
            'dnssecKey' => '',
            'dnssecKeyTag' => $zone->dnssec ? (int) $zone->dnssec_key_tag : 0,
        ];
    }

    /**
     * The zones of a SetDnsZones job, with the DNSSEC key of each signed zone. A zone that has
     * a different key now, or no key, goes with no key: a later job has its new state.
     *
     * @return array<string, mixed>
     */
    public static function keysFor(AgentJob $job): array
    {
        if ($job->action !== AgentAction::SetDnsZones || ! is_array($job->payload['zones'] ?? null)) {
            return [];
        }
        $zones = $job->payload['zones'];
        $signed = DnsZone::whereIn('name', array_column($zones, 'name'))->where('dnssec', true)->get()->keyBy('name');
        foreach ($zones as &$zone) {
            $tag = (int) ($zone['dnssecKeyTag'] ?? 0);
            $now = $signed->get($zone['name'] ?? '');
            if ($tag > 0 && $now && (int) $now->dnssec_key_tag === $tag) {
                $zone['dnssecKey'] = (string) $now->dnssec_key;
            } elseif ($tag > 0) {
                $zone['dnssecKeyTag'] = 0;
            }
        }

        return ['zones' => $zones];
    }

    /**
     * Records the state of a zone on some DNS servers. A result for an old serial does not
     * change the record of a newer serial.
     *
     * @param  list<string>  $serverIds
     */
    private static function mark(DnsZone $zone, array $serverIds, int $serial, string $state, ?string $error = null): void
    {
        DB::transaction(function () use ($zone, $serverIds, $serial, $state, $error) {
            $current = DnsZone::lockForUpdate()->find($zone->id);
            if (! $current) {
                return;
            }
            $sync = $current->sync ?? [];
            foreach ($serverIds as $id) {
                if (($sync[$id]['serial'] ?? 0) <= $serial) {
                    $sync[$id] = array_filter(['serial' => $serial, 'state' => $state, 'error' => $error], fn ($v) => $v !== null);
                }
            }
            $current->update(['sync' => $sync]);
            $zone->setAttribute('sync', $sync)->syncOriginalAttribute('sync');
        });
    }

    /**
     * A job of a DNS server is complete. The panel records the result for each zone of the
     * job, and removes the record of a zone that all DNS servers removed.
     */
    public static function jobFinished(AgentJob $job, bool $succeeded): void
    {
        $p = $job->payload;
        if ($job->action === AgentAction::SetDnsZones) {
            foreach (is_array($p['zones'] ?? null) ? $p['zones'] : [] as $sent) {
                $zone = DnsZone::where('name', $sent['name'] ?? '')->first();
                if ($zone) {
                    self::mark($zone, [$job->server_id], (int) ($sent['serial'] ?? 0), $succeeded ? 'ok' : 'failed');
                }
            }

            return;
        }
        foreach (DnsZone::whereIn('name', array_filter((array) ($p['names'] ?? []), 'is_string'))->where('status', DnsZone::REMOVING)->get() as $zone) {
            self::mark($zone, [$job->server_id], $zone->serial, $succeeded ? 'removed' : 'failed');
            $left = array_filter($zone->sync ?? [], fn ($s, $id) => ($s['state'] ?? '') !== 'removed' && self::servers()->contains('id', $id), ARRAY_FILTER_USE_BOTH);
            if ($left === []) {
                $zone->delete();
            }
        }
    }

    /**
     * The state of a zone for a user: "removing", "failed" (a DNS server did not take the last
     * change), "updating" (a DNS server does not have the last change yet) or "active".
     */
    public static function state(DnsZone $zone, ?Collection $servers = null): string
    {
        if ($zone->status === DnsZone::REMOVING) {
            return DnsZone::REMOVING;
        }
        $states = ($zone->onPanelServers() ? ($servers ?? self::servers()) : collect())->map(function (Server $s) use ($zone) {
            $sync = $zone->sync[$s->id] ?? null;

            return match (true) {
                $sync === null, ($sync['serial'] ?? 0) < $zone->serial, ($sync['state'] ?? '') === 'pending' => 'updating',
                ($sync['state'] ?? '') === 'failed' => 'failed',
                default => 'active',
            };
        });
        if ($zone->dns_account_id !== null) {
            $provider = $zone->provider_state ?? [];
            $states->push(match (true) {
                ($provider['serial'] ?? 0) < $zone->serial, ($provider['state'] ?? 'pending') === 'pending' => 'updating',
                $provider['state'] === 'failed' => 'failed',
                default => 'active',
            });
        }

        return $states->contains('failed') ? 'failed' : ($states->contains('updating') ? 'updating' : 'active');
    }
}
