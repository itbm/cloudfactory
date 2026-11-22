<?php

namespace App\Services\Dns;

use App\Models\DnsAccount;
use App\Models\DnsRecord;
use App\Models\DnsZone;
use App\Models\Plugin;
use App\Services\DomainPolicy;
use App\Services\Plugins\Caller;
use App\Validation\Input;
use App\Validation\InputRules;
use Illuminate\Support\Facades\Log;
use Illuminate\Support\Str;
use Illuminate\Validation\ValidationException;

/**
 * External DNS providers, for example Cloudflare. A plugin offers a provider in its manifest
 * ("dnsProviders"). The panel keeps the records of the zone. After each change, it sends the
 * full zone to the plugin, and the plugin makes the zone at the provider the same.
 *
 * The contract is cf-dns-provider/1 (docs/plugins.md). Each call is a signed POST to the path
 * of the provider, with the header "Cloudfactory-Event: dns.provider". The operations are:
 *
 * - "verify": checks the credentials. The answer can give the name of the account.
 * - "records": gives the records that the provider has for a zone now. The panel copies them
 *   to a zone that moves to the provider, so that no record goes.
 * - "sync": makes the zone at the provider the same as the zone of the panel. The answer gives
 *   the name servers of the zone at the provider, and the state of DNSSEC.
 * - "remove": removes the zone at the provider.
 *
 * The panel reads each answer only as data, and checks each value before it keeps it.
 */
class Providers
{
    public const CONTRACT = 'cf-dns-provider/1';

    public const EVENT = 'dns.provider';

    /** The time for each operation, in seconds. */
    public const TIMEOUT = ['verify' => 15, 'records' => 30, 'sync' => 60, 'remove' => 30];

    /** The longest error text of a plugin that the panel shows. */
    public const MAX_ERROR = 300;

    /** The most name servers and DS records that the panel keeps from an answer. */
    private const MAX_NAME_SERVERS = 13;

    private const MAX_DS = 4;

    /** The TTL of the TXT record with the claim token of a zone. */
    public const CLAIM_TTL = 300;

    public function __construct(private Caller $caller, private DomainPolicy $policy) {}

    /**
     * The providers of the plugins that are on, with their plugin.
     *
     * @return list<array<string, mixed>>
     */
    public static function available(): array
    {
        return Plugin::where('enabled', true)->orderBy('name')->get()
            ->flatMap(fn (Plugin $p) => array_map(fn (array $d) => ['plugin' => $p] + $d, $p->dnsProviders()))
            ->values()->all();
    }

    /**
     * Checks the credentials with the plugin. It gives the name of the account at the provider,
     * or null when the plugin gives none.
     *
     * @param  array<string, mixed>  $definition  The provider in the manifest.
     * @param  array<string, string>  $credentials
     *
     * @throws ValidationException
     */
    public function verify(Plugin $plugin, array $definition, array $credentials): ?string
    {
        $result = $this->send($plugin, $definition, 'verify', $credentials, null);
        if ($result['error'] !== null) {
            throw ValidationException::withMessages(['credentials' => $result['error']]);
        }
        $account = $result['answer']['account'] ?? null;

        return is_string($account) && trim($account) !== '' ? mb_substr(trim($account), 0, 100) : null;
    }

    /**
     * The records that the provider has for a zone now, in the form of ZoneFile::parse, so that
     * Import::apply can add them. The list has no NS records of the zone itself: the provider
     * keeps them.
     *
     * @return list<array{line: int, text: string, name: string, type: string, ttl: int, value: string, priority: ?int, note: ?string}>
     *
     * @throws ValidationException
     */
    public function records(DnsAccount $account, string $zoneName, string $customerId): array
    {
        $claim = $this->policy->claimToken($customerId, $zoneName);
        $result = $this->forAccount($account, 'records', ['name' => $zoneName]);
        if ($result['error'] !== null) {
            throw ValidationException::withMessages(['accountId' => $result['error']]);
        }
        $list = $result['answer']['records'] ?? [];
        if (! is_array($list) || ! array_is_list($list)) {
            throw ValidationException::withMessages(['accountId' => "The plugin of {$account->providerName()} gave no list of records."]);
        }
        $records = [];
        foreach (array_slice($list, 0, InputRules::MAX_DNS_RECORDS_PER_ZONE) as $i => $r) {
            if (! is_array($r)) {
                continue;
            }
            $type = strtoupper(is_string($r['type'] ?? null) ? $r['type'] : '');
            $name = is_string($r['name'] ?? null) ? $r['name'] : '@';
            $value = is_string($r['value'] ?? null) ? $r['value'] : '';
            $ttl = is_int($r['ttl'] ?? null) ? max(InputRules::MIN_DNS_SHORT_TTL, min(InputRules::MAX_DNS_TTL, $r['ttl'])) : Records::DEFAULT_TTL;
            $priority = is_int($r['priority'] ?? null) ? $r['priority'] : null;
            if ($type === 'NS' && in_array($name, ['@', '', $zoneName], true)) {
                continue;
            }
            // The panel sends the claim token with the zone. It is not a record of the customer.
            if ($type === 'TXT' && $name === DomainPolicy::CHALLENGE && $value === $claim) {
                continue;
            }
            $records[] = [
                'line' => $i + 1,
                'text' => mb_substr(trim("{$name} {$ttl} {$type} ".($priority !== null ? "{$priority} " : '').$value), 0, 300),
                'name' => $name, 'type' => $type, 'ttl' => $ttl, 'value' => $value, 'priority' => $priority, 'note' => null,
            ];
        }

        return $records;
    }

    /**
     * Sends the full zone to the provider. The state has the name servers of the zone at the
     * provider, "active" (the provider answers for the zone) and the state of DNSSEC.
     *
     * The zone also has a TXT record with the claim token of the customer for the zone. The
     * name servers of a provider serve many accounts, and the name servers that the plugin
     * gives come from the account of the customer. Thus only the token in the public DNS proves
     * that the public DNS uses this zone (Zones::checkDelegation).
     *
     * @return array{error: ?string, state: array<string, mixed>}
     */
    public function sync(DnsZone $zone): array
    {
        // The provider keeps the NS records of the zone itself.
        $records = $zone->records()->where(fn ($q) => $q->whereNull('managed')->orWhere('managed', '!=', DnsRecord::NS))
            ->orderBy('name')->orderBy('type')->orderBy('id')->get()
            ->map(fn (DnsRecord $r) => ['name' => $r->name, 'type' => $r->type, 'ttl' => $r->ttl, 'value' => $r->value, 'priority' => $r->priority])->all();
        $claim = ['name' => DomainPolicy::CHALLENGE, 'type' => 'TXT', 'ttl' => self::CLAIM_TTL, 'value' => $this->policy->claimToken($zone->customer_id, $zone->name), 'priority' => null];
        if (! collect($records)->contains(fn (array $r) => $r['name'] === $claim['name'] && $r['type'] === 'TXT' && $r['value'] === $claim['value'])) {
            $records[] = $claim;
        }
        $result = $this->forAccount($zone->account, 'sync', [
            'name' => $zone->name,
            'serial' => $zone->serial,
            'dnssec' => $zone->dnssec,
            'records' => $records,
        ]);
        if ($result['error'] !== null) {
            return ['error' => $result['error'], 'state' => []];
        }
        $a = $result['answer'];
        $changes = is_array($a['changes'] ?? null) ? $a['changes'] : [];

        return ['error' => null, 'state' => [
            'nameServers' => self::hosts($a['nameServers'] ?? []),
            'active' => ($a['active'] ?? false) === true,
            'dnssec' => self::dnssec($a['dnssec'] ?? null),
            'changes' => array_map(fn ($k) => is_int($changes[$k] ?? null) ? max(0, $changes[$k]) : 0, ['added' => 'added', 'changed' => 'changed', 'removed' => 'removed']),
        ]];
    }

    /** Removes the zone at the provider. It gives the error, or null when the zone is gone. */
    public function remove(DnsAccount $account, string $zoneName): ?string
    {
        return $this->forAccount($account, 'remove', ['name' => $zoneName])['error'];
    }

    /**
     * @param  array<string, mixed>  $zone
     * @return array{answer: array<string, mixed>, error: ?string}
     */
    private function forAccount(DnsAccount $account, string $operation, array $zone): array
    {
        $definition = $account->definition();
        if ($definition === null) {
            return ['answer' => [], 'error' => "The plugin of this DNS account does not offer the provider \"{$account->provider}\" now. Speak to your provider."];
        }

        return $this->send($account->plugin, $definition, $operation, $account->credentials ?? [], $zone);
    }

    /**
     * @param  array<string, mixed>  $definition
     * @param  array<string, mixed>  $credentials
     * @param  array<string, mixed>|null  $zone
     * @return array{answer: array<string, mixed>, error: ?string}
     */
    private function send(Plugin $plugin, array $definition, string $operation, array $credentials, ?array $zone): array
    {
        if (! $plugin->enabled) {
            return ['answer' => [], 'error' => "The plugin {$plugin->name} is off. Speak to your provider."];
        }
        $id = strtolower((string) Str::ulid());
        $result = $this->caller->post($plugin, $plugin->urlOf($definition['path']), self::EVENT, $id, [
            'id' => $id,
            'contract' => self::CONTRACT,
            'provider' => $definition['id'],
            'operation' => $operation,
            'credentials' => (object) $credentials,
            'zone' => $zone,
            'createdAt' => now()->toIso8601String(),
        ], self::TIMEOUT[$operation]);
        $answer = $result['response']?->json();
        $answer = is_array($answer) ? $answer : [];
        if ($result['error'] === null) {
            return ['answer' => $answer, 'error' => null];
        }
        // The plugin writes its error for the user, for example "Cloudflare refused the API
        // token". Adversarial review, finding A7-12: the error of the connection has the address
        // of the plugin. It goes only to the log.
        $message = is_string($answer['error'] ?? null) && trim($answer['error']) !== '' ? mb_substr(trim($answer['error']), 0, self::MAX_ERROR) : null;
        Log::warning("The DNS provider {$definition['id']} of the plugin {$plugin->slug} failed the operation {$operation}: {$result['error']}".($message !== null ? " {$message}" : ''));

        return ['answer' => [], 'error' => $message ?? "The plugin of {$definition['name']} did not answer. Try again later. Speak to your provider if this continues."];
    }

    /** @return list<string> The valid host names of a list, in lower case and with no dot at the end. */
    private static function hosts(mixed $list): array
    {
        $hosts = [];
        foreach (is_array($list) ? array_slice($list, 0, self::MAX_NAME_SERVERS) : [] as $host) {
            $host = is_string($host) ? strtolower(rtrim(trim($host), '.')) : '';
            if ($host !== '' && Input::valid('dnsHost', $host)) {
                $hosts[] = $host;
            }
        }
        $hosts = array_values(array_unique($hosts));
        sort($hosts);

        return $hosts;
    }

    /** @return array{state: string, ds: list<array{keyTag: int, algorithm: int, digestType: int, digest: string}>} */
    private static function dnssec(mixed $value): array
    {
        $value = is_array($value) ? $value : [];
        $state = in_array($value['state'] ?? null, ['off', 'pending', 'active'], true) ? $value['state'] : 'off';
        $ds = [];
        foreach (is_array($value['ds'] ?? null) ? array_slice($value['ds'], 0, self::MAX_DS) : [] as $d) {
            if (! is_array($d)) {
                continue;
            }
            $tag = $d['keyTag'] ?? null;
            $algorithm = $d['algorithm'] ?? null;
            $digestType = $d['digestType'] ?? null;
            $digest = is_string($d['digest'] ?? null) ? strtoupper($d['digest']) : '';
            if (is_int($tag) && $tag >= 0 && $tag <= 65535 && is_int($algorithm) && $algorithm >= 1 && $algorithm <= 255
                && is_int($digestType) && $digestType >= 1 && $digestType <= 255 && preg_match('/^[0-9A-F]{2,128}$/D', $digest)) {
                $ds[] = ['keyTag' => $tag, 'algorithm' => $algorithm, 'digestType' => $digestType, 'digest' => $digest];
            }
        }

        return ['state' => $state, 'ds' => $ds];
    }
}
