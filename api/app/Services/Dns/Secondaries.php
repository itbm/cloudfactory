<?php

namespace App\Services\Dns;

use App\Enums\AgentAction;
use App\Enums\JobState;
use App\Models\AgentJob;
use App\Models\DnsSecondary;
use App\Models\DnsZone;
use App\Models\Server;
use App\Models\Setting;
use App\Services\JobDispatcher;
use App\Validation\InputRules;
use Illuminate\Support\Collection;
use Illuminate\Validation\ValidationException;
use Symfony\Component\HttpFoundation\IpUtils;

/**
 * Secondary DNS: DNS servers outside the panel copy the zones from the DNS servers of the
 * panel with zone transfers. The panel never copies a zone from a different server.
 *
 * - A secondary of the panel copies all zones. An administrator adds it. Each DNS server of
 *   the panel gets the list of the secondaries (SetDnsSecondaries). The list has a version. The
 *   report of each agent has the version that the server has, and the panel sends the list
 *   again when the server has a different version.
 * - The owner of a zone can give its own secondaries for the zone. The zone takes them to
 *   each DNS server in the job of the zone (SetDnsZones). Their addresses must be public, so
 *   that the DNS servers do not send NOTIFY into a private network.
 * - A TSIG key signs the transfers. The secrets are not in the jobs: the panel adds them each
 *   time that it sends a job (forAgent and Zones::keysFor).
 */
class Secondaries
{
    /** The setting with the version of the list of the secondaries. */
    public const VERSION = 'dns.secondaries.version';

    /** The setting with the name of the catalog zone in the last version of the list. */
    public const CATALOG = 'dns.secondaries.catalog';

    /** The prefix of the name of the TSIG key of the transfers of one zone. */
    public const ZONE_KEY_PREFIX = 'transfer.';

    /** The most name servers that the owner of a zone adds for its secondaries. */
    public const MAX_ZONE_NAME_SERVERS = 4;

    /**
     * The addresses that are not public, in addition to the private and the reserved ranges
     * of PHP. The agent refuses the same addresses (publicAddress in dns_transfers.go).
     */
    private const NOT_PUBLIC = [
        '0.0.0.0/8', '10.0.0.0/8', '100.64.0.0/10', '127.0.0.0/8', '169.254.0.0/16', '172.16.0.0/12', '192.0.0.0/24',
        '192.168.0.0/16', '198.18.0.0/15', '224.0.0.0/4', '240.0.0.0/4',
        '::/128', '::1/128', '::ffff:0:0/96', '64:ff9b::/96', '64:ff9b:1::/48', '100::/64', '2002::/16', 'fc00::/7', 'fe80::/10', 'ff00::/8',
    ];

    public function __construct(private JobDispatcher $jobs, private Zones $zones) {}

    /** The version of the list of the secondaries. 0: the panel never had a secondary. */
    public static function version(): int
    {
        return (int) Setting::get(self::VERSION, 0);
    }

    /** The name of the catalog zone. */
    public static function catalogZone(): string
    {
        return strtolower((string) config('cloudfactory.dns_catalog_zone'));
    }

    /**
     * The host names of the secondaries of the panel that are name servers. Each zone on the DNS
     * servers of the panel gets an NS record for each one.
     *
     * @return list<string>
     */
    public static function nameServers(): array
    {
        return DnsSecondary::whereNotNull('name_server')->orderBy('name_server')->pluck('name_server')->unique()->values()->all();
    }

    /** A new TSIG secret: as many bytes as the hash of the algorithm. */
    public static function newSecret(string $algorithm): string
    {
        return base64_encode(random_bytes($algorithm === 'hmac-sha512' ? 64 : 32));
    }

    /**
     * Checks a TSIG secret that a user copied from a secondary DNS service: base64, 16 to 64
     * bytes.
     *
     * @throws ValidationException
     */
    public static function checkSecret(string $field, string $secret): void
    {
        $bytes = base64_decode($secret, true);
        if (preg_match(InputRules::TSIG_SECRET, $secret) !== 1 || $bytes === false || strlen($bytes) < 16 || strlen($bytes) > 64) {
            throw ValidationException::withMessages([$field => InputRules::MESSAGES['tsigSecret']]);
        }
    }

    /** True when an address is on the public internet. */
    public static function publicAddress(string $address): bool
    {
        return ! IpUtils::checkIp($address, self::NOT_PUBLIC);
    }

    /**
     * Checks the addresses of a secondary, and gives them in their usual form, each one time.
     * With "public", each address must be public.
     *
     * @return list<string>
     *
     * @throws ValidationException
     */
    public static function addresses(string $field, array $addresses, bool $public): array
    {
        $max = InputRules::MAX_DNS_TRANSFER_ADDRESSES;
        if ($addresses === [] || count($addresses) > $max) {
            throw ValidationException::withMessages([$field => "Enter 1 to {$max} IP addresses."]);
        }
        $out = [];
        foreach ($addresses as $address) {
            $packed = is_string($address) ? @inet_pton(trim($address)) : false;
            if ($packed === false || str_contains((string) $address, '%')) {
                throw ValidationException::withMessages([$field => 'Enter IP addresses, for example 192.0.2.53 or 2001:db8::53.']);
            }
            $ip = (string) inet_ntop($packed);
            // An IPv4 address in an IPv6 address: the agent gives it a different form.
            if (str_contains($ip, ':') && str_contains($ip, '.')) {
                throw ValidationException::withMessages([$field => "Enter {$ip} as an IPv4 address."]);
            }
            if (IpUtils::checkIp($ip, ['0.0.0.0/32', '::/128', '224.0.0.0/4', 'ff00::/8'])) {
                throw ValidationException::withMessages([$field => "{$ip} is not the address of a server."]);
            }
            if ($public && ! self::publicAddress($ip)) {
                throw ValidationException::withMessages([$field => "{$ip} is not a public address. Enter the public addresses of your secondary DNS servers."]);
            }
            $out[$ip] = true;
        }

        return array_keys($out);
    }

    /**
     * The secondaries of the panel as the agent gets them, with their secrets, and the
     * version. The panel adds this to each SetDnsSecondaries job when it sends the job, so the
     * agent gets the current list also after a job waited in the queue.
     *
     * @return array<string, mixed>
     */
    public static function forAgent(AgentJob $job): array
    {
        if ($job->action !== AgentAction::SetDnsSecondaries) {
            return [];
        }
        $secondaries = DnsSecondary::orderBy('id')->get();

        return [
            'secondaries' => $secondaries->map(fn (DnsSecondary $s) => [
                'id' => $s->id,
                'addresses' => $s->addresses,
                'tsig' => $s->tsig_algorithm ? ['name' => $s->tsig_name, 'algorithm' => $s->tsig_algorithm, 'secret' => (string) $s->tsig_secret] : null,
                'notify' => $s->notify,
                'catalog' => $s->catalog,
            ])->all(),
            'catalogZone' => $secondaries->contains('catalog', true) ? self::catalogZone() : '',
            'version' => self::version(),
        ];
    }

    /**
     * The transfers of a zone to the secondaries of its owner, as the agent gets them, or null.
     * With "secret", the TSIG key has its secret. A job keeps the transfers with no secret.
     *
     * @return array{addresses: list<string>, tsig: array{name: string, algorithm: string, secret: string}|null, notify: bool}|null
     */
    public static function transfersFor(DnsZone $zone, bool $secret): ?array
    {
        if (! $zone->transfer_addresses || $zone->dns_account_id !== null) {
            return null;
        }

        return [
            'addresses' => array_values($zone->transfer_addresses),
            'tsig' => $zone->transfer_tsig_algorithm ? ['name' => $zone->transferKeyName(), 'algorithm' => $zone->transfer_tsig_algorithm, 'secret' => $secret ? (string) $zone->transfer_tsig_secret : ''] : null,
            'notify' => $zone->transfer_notify,
        ];
    }

    /**
     * Sets the transfers of a zone to the secondaries of its owner, or turns them off with
     * null. The zone gets the NS records of the secondaries, and each DNS server gets the zone
     * again.
     *
     * - With "tsig", a TSIG key signs the transfers. The panel makes the secret, or keeps the
     *   secret that the user gives. "newSecret" makes a new secret.
     *
     * @param  array{addresses: list<string>, notify: bool, nameServers: list<string>, tsig: bool, algorithm: string, secret: ?string, newSecret: bool}|null  $in
     * @return list<AgentJob>
     *
     * @throws ValidationException
     */
    public function setTransfers(DnsZone $zone, ?array $in): array
    {
        if ($zone->dns_account_id !== null) {
            throw ValidationException::withMessages(['zone' => 'This zone uses an external DNS provider. The provider answers for the zone, so the panel does not send it to secondary DNS servers.']);
        }
        if ($in === null) {
            if (! $zone->transfer_addresses && ! $zone->transfer_name_servers) {
                return [];
            }
            $zone->update(['transfer_addresses' => null, 'transfer_notify' => false, 'transfer_tsig_algorithm' => null, 'transfer_tsig_secret' => null, 'transfer_name_servers' => null]);
            Zones::writeNameServers($zone);

            return $this->zones->changed($zone, "Stop the zone transfers of {$zone->name}");
        }
        $addresses = self::addresses('addresses', $in['addresses'], true);
        $nameServers = array_values(array_unique(array_map('strtolower', $in['nameServers'])));
        $algorithm = $in['tsig'] ? $in['algorithm'] : null;
        $secret = null;
        if ($algorithm !== null) {
            if (($in['secret'] ?? '') !== '') {
                self::checkSecret('secret', (string) $in['secret']);
                $secret = (string) $in['secret'];
            } elseif ($in['newSecret'] || $zone->transfer_tsig_secret === null || $zone->transfer_tsig_algorithm !== $algorithm) {
                $secret = self::newSecret($algorithm);
            } else {
                $secret = $zone->transfer_tsig_secret;
            }
        }
        $zone->update([
            'transfer_addresses' => $addresses,
            'transfer_notify' => $in['notify'],
            'transfer_tsig_algorithm' => $algorithm,
            'transfer_tsig_secret' => $secret,
            'transfer_name_servers' => $nameServers ?: null,
        ]);
        Zones::writeNameServers($zone);

        return $this->zones->changed($zone, "Set the zone transfers of {$zone->name}");
    }

    /**
     * The list of the secondaries changed. Each DNS server of the panel gets the new list.
     * When the name servers changed, each zone gets its new NS records.
     *
     * @return list<AgentJob>
     */
    public function changed(string $title, bool $nameServers): array
    {
        $version = self::version() + 1;
        Setting::put(self::VERSION, $version);
        Setting::put(self::CATALOG, self::catalogZone());
        $jobs = [];
        foreach (Zones::servers() as $server) {
            $jobs[] = $this->dispatch($server, $version, $title);
        }
        if ($nameServers) {
            $this->zones->pushAll();
        }

        return $jobs;
    }

    private function dispatch(Server $server, int $version, string $title): AgentJob
    {
        return $this->jobs->dispatch($server, AgentAction::SetDnsSecondaries, ['version' => $version], $title);
    }

    /** The time between two tries to send the list again to a server that did not take it, in minutes. */
    public const RETRY_MINUTES = 10;

    /**
     * Compares the version that a DNS server reported with the version of the panel, and sends
     * the list again when they are different, for example after the server was offline. After
     * a job that failed, the panel waits RETRY_MINUTES. A new name of the catalog zone sends a
     * new version to each DNS server.
     */
    public function reconcile(Server $server, int $reported): void
    {
        // A new name of the catalog zone (CF_DNS_CATALOG_ZONE) gives the list a new version.
        if (self::version() > 0 && Setting::get(self::CATALOG) !== self::catalogZone()) {
            if (DnsSecondary::where('catalog', true)->exists()) {
                $this->changed('Change the name of the catalog zone to '.self::catalogZone(), false);

                return;
            }
            Setting::put(self::CATALOG, self::catalogZone());
        }
        $version = self::version();
        if ($reported === $version || ! Zones::servers()->contains('id', $server->id)) {
            return;
        }
        $last = AgentJob::where('server_id', $server->id)->where('action', AgentAction::SetDnsSecondaries->value)->latest()->orderByDesc('id')->first();
        if ($last && in_array($last->state, [JobState::Queued, JobState::Running], true)) {
            return;
        }
        if ($last && $last->state === JobState::Failed && ($last->finished_at ?? $last->updated_at)?->gt(now()->subMinutes(self::RETRY_MINUTES))) {
            return;
        }
        $this->dispatch($server, $version, "Send the secondary DNS servers to {$server->name} again");
    }

    /**
     * The state of the list on each DNS server of the panel: "ok" when the last job with the
     * current version succeeded, "failed" when it failed, and "pending" in other cases.
     *
     * @return list<array{server: string, state: string}>
     */
    public static function serverStates(?Collection $servers = null): array
    {
        $version = self::version();

        return ($servers ?? Zones::servers())->map(function (Server $server) use ($version) {
            $last = AgentJob::where('server_id', $server->id)->where('action', AgentAction::SetDnsSecondaries->value)->latest()->orderByDesc('id')->first();
            $current = $last && (int) ($last->payload['version'] ?? 0) === $version;

            return ['server' => (string) $server->dns_name, 'state' => match (true) {
                $version === 0 => 'ok',
                $current && $last->state === JobState::Succeeded => 'ok',
                $current && $last->state === JobState::Failed => 'failed',
                default => 'pending',
            }];
        })->values()->all();
    }
}
