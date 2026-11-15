<?php

namespace App\Services\Dns;

use App\Enums\AgentAction;
use App\Enums\JobState;
use App\Http\Controllers\Api\DomainController;
use App\Models\AgentJob;
use App\Models\Customer;
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
 */
class Zones
{
    /** The TTL of the records that the panel makes for a website: 5 minutes. */
    public const WEBSITE_TTL = 300;

    /** The TTL of the NS records of a zone: one hour. */
    public const NS_TTL = 3600;

    public function __construct(private JobDispatcher $jobs, private Lookup $lookup, private DomainPolicy $policy) {}

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
     * customer, and sends it to the DNS servers.
     *
     * @throws ValidationException
     */
    public function create(Customer $customer, string $name, User $user): DnsZone
    {
        if (self::nameServers() === []) {
            throw ValidationException::withMessages(['name' => $user->isAdministrator()
                ? 'The panel has no DNS server. Give a server the role "DNS" and a name server host name on the page of the server.'
                : 'DNS hosting is not available. Ask your provider.']);
        }
        $taken = $this->checkName($name, $customer, $user);
        $zone = DB::transaction(function () use ($customer, $name) {
            // The count and the new zone are in one transaction, with a lock on the customer.
            $customer = Customer::with('plan')->lockForUpdate()->findOrFail($customer->id);
            self::checkLimit($customer);

            return DnsZone::create(['customer_id' => $customer->id, 'name' => $name, 'serial' => now()->timestamp, 'status' => DnsZone::ACTIVE]);
        });
        if ($taken) {
            Audit::system('Removed DNS zone, because a different customer proved control of the name', $name);
        }
        self::writeNameServers($zone);
        foreach (self::websiteDomains($zone) as [$website, $domain]) {
            self::writeWebsiteRecords($zone, $website, $domain);
        }
        $this->checkDelegation($zone);
        $this->push($zone, "Create DNS zone {$zone->name}");

        return $zone;
    }

    /**
     * Refuses a zone name that the customer must not use. It returns true when the zone of a
     * different customer was removed, because this customer proved control of the name.
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
    public function checkName(string $name, Customer $customer, User $user): bool
    {
        if (! $user->isAdministrator()) {
            foreach ([...$this->policy->reserved(), ...self::nameServers()] as $reserved) {
                if (DomainPolicy::within($name, $reserved) || DomainPolicy::within($reserved, $name)) {
                    throw ValidationException::withMessages(['name' => 'This name is reserved for the panel or a server.']);
                }
            }
        }
        $taken = false;
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
                $same = $other->name === $name;
                throw ValidationException::withMessages(['name' => ($same ? 'A different customer has this zone.' : 'This name overlaps with a zone of a different customer.')
                    .' If you control the domain,'.$challenge]);
            }
            if ($other->name === $name) {
                if ($other->status === DnsZone::REMOVING) {
                    throw ValidationException::withMessages(['name' => 'The panel removes a zone with this name. Try again in one minute.']);
                }
                // The zone of the other customer goes. The DNS servers get the new zone with
                // the same name, so the panel removes only its record.
                $other->delete();
                $taken = true;
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

    /** Gives a zone a larger serial. The serial is the time of the change, in seconds. */
    private static function bump(DnsZone $zone): void
    {
        $zone->update(['serial' => max($zone->serial + 1, now()->timestamp)]);
    }

    /**
     * Sends one zone to each DNS server, with one job for each server.
     *
     * @return list<AgentJob>
     */
    public function push(DnsZone $zone, string $title): array
    {
        $jobs = [];
        $servers = self::servers();
        self::mark($zone, $servers->pluck('id')->all(), $zone->serial, 'pending');
        foreach ($servers as $server) {
            $jobs[] = $this->jobs->dispatch($server, AgentAction::SetDnsZones, ['zones' => [self::forAgent($zone)]], $title, dnsZone: $zone);
        }

        return $jobs;
    }

    /**
     * A reseller changed the host names of its name servers. The zones of the reseller and of
     * its customers get new NS records, and the DNS servers get the zones again.
     */
    public function resellerNameServersChanged(Customer $reseller): void
    {
        $ids = Customer::where('id', $reseller->id)->orWhere('reseller_id', $reseller->id)->pluck('id');
        foreach (DnsZone::with('customer')->whereIn('customer_id', $ids)->where('status', DnsZone::ACTIVE)->orderBy('name')->get() as $zone) {
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
        DnsZone::where('status', DnsZone::ACTIVE)->orderBy('name')->chunkById(InputRules::MAX_DNS_ZONES_PER_JOB, function (Collection $zones) use ($servers) {
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
            foreach (DnsZone::where('status', DnsZone::ACTIVE)->orderBy('name')->get() as $zone) {
                $sync = $zone->sync[$server->id] ?? null;
                // The server has a newer zone than the panel, for example after a restore of
                // the panel database. The server refuses an older zone, so the zone gets a
                // larger serial. Each other DNS server gets it after its next report.
                if ($has->get($zone->name, 0) > $zone->serial) {
                    $zone->update(['serial' => max($has->get($zone->name) + 1, now()->timestamp)]);
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
        $keep = $answers ? DnsZone::where('status', DnsZone::ACTIVE)->pluck('name')->flip() : collect();
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
     * removed the zone.
     *
     * @return list<AgentJob>
     */
    public function remove(DnsZone $zone): array
    {
        $servers = self::servers();
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
        $ours = array_values(array_unique([...self::nameServers(), ...Branding::nameServersFor($zone->customer)]));
        $state = match (true) {
            $seen === [] => DnsZone::NONE,
            array_diff($seen, $ours) === [] => DnsZone::HERE,
            default => DnsZone::ELSEWHERE,
        };
        $was = $zone->delegation;
        $zone->update(['delegation' => $state, 'delegation_name_servers' => $seen, 'delegation_checked_at' => now()]);
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
        foreach ($website->server->addresses ?? [] as $address) {
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
     * it has its own names for the name servers (white label).
     */
    private static function writeNameServers(DnsZone $zone): void
    {
        $hosts = Branding::nameServersFor($zone->customer);
        sort($hosts);
        $current = $zone->records()->where('managed', DnsRecord::NS)->pluck('value')->sort()->values()->all();
        if ($current === $hosts) {
            return;
        }
        $zone->records()->where('managed', DnsRecord::NS)->delete();
        foreach ($hosts as $host) {
            $zone->records()->create(['name' => '@', 'type' => 'NS', 'ttl' => self::NS_TTL, 'value' => $host, 'managed' => DnsRecord::NS]);
        }
    }

    /** The zone as the agent gets it: the full list of the records, and the DNSSEC key. */
    public static function forAgent(DnsZone $zone): array
    {
        return [
            'name' => $zone->name,
            'serial' => $zone->serial,
            'records' => $zone->records()->orderBy('name')->orderBy('type')->orderBy('id')->get()->map(fn (DnsRecord $r) => $r->forAgent())->all(),
            'dnssecKey' => $zone->dnssec ? (string) $zone->dnssec_key : '',
            'dnssecKeyTag' => $zone->dnssec ? (int) $zone->dnssec_key_tag : 0,
        ];
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
        $states = ($servers ?? self::servers())->map(function (Server $s) use ($zone) {
            $sync = $zone->sync[$s->id] ?? null;

            return match (true) {
                $sync === null, ($sync['serial'] ?? 0) < $zone->serial, ($sync['state'] ?? '') === 'pending' => 'updating',
                ($sync['state'] ?? '') === 'failed' => 'failed',
                default => 'active',
            };
        });

        return $states->contains('failed') ? 'failed' : ($states->contains('updating') ? 'updating' : 'active');
    }
}
