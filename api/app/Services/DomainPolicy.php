<?php

namespace App\Services;

use App\Models\Customer;
use App\Models\Domain;
use App\Models\Server;
use App\Models\User;

/**
 * CF-03 and CF-20: which domains a customer can use.
 *
 * - Nobody can use the panel domain, the host name of a server, or a sub-domain of one of these.
 * - Nobody else can use the panel domain of a reseller (R-03: only that exact name, not its zone).
 * - The edge proxy serves a domain only after the customer proves that they control it, with a
 *   DNS TXT record. A domain is verified at once when the user is an administrator, or when it
 *   is a verified domain of the same customer or a sub-domain of one.
 */
class DomainPolicy
{
    /** The domains that nobody can attach to a website, with all of their sub-domains. */
    public function reserved(): array
    {
        $domains = [config('cloudfactory.panel_domain')];
        foreach (Server::pluck('agent_url') as $url) {
            $host = parse_url((string) $url, PHP_URL_HOST);
            if (is_string($host) && str_contains($host, '.') && ! filter_var($host, FILTER_VALIDATE_IP)) {
                $domains[] = $host;
            }
        }

        return self::normalise($domains);
    }

    /**
     * R-03: the panel domains of resellers. Only the exact name is reserved, so that a brand
     * cannot block a whole zone such as "co.uk" for all other customers.
     */
    public function brandDomains(?string $exceptCustomerId = null): array
    {
        $domains = [];
        foreach (Customer::where('role', 'reseller')->when($exceptCustomerId, fn ($q, $id) => $q->where('id', '!=', $id))->get(['branding']) as $reseller) {
            $domains[] = $reseller->branding['panelDomain'] ?? null;
        }

        return self::normalise($domains);
    }

    /** True when the name is reserved for the panel, a server or a different reseller. */
    public function isReserved(string $name, ?string $exceptCustomerId = null): bool
    {
        foreach ($this->reserved() as $domain) {
            if (self::within($name, $domain)) {
                return true;
            }
        }

        return in_array($name, $this->brandDomains($exceptCustomerId), true);
    }

    /**
     * R-03 and R-04: the TXT value that proves that a customer controls a name. The panel does
     * not store it. It is a keyed hash of the customer and the name, so that it is the same each
     * time and nobody else can make it.
     */
    public function claimToken(string $customerId, string $name): string
    {
        return 'cf-'.substr(hash_hmac('sha256', "claim|{$customerId}|{$name}", (string) config('app.key')), 0, 40);
    }

    /** True when the TXT record of the name holds the claim token of the customer. */
    public function proves(string $customerId, string $name): bool
    {
        return $this->hasTxt('_cloudfactory-challenge.'.$name, $this->claimToken($customerId, $name));
    }

    /** True when the user does not need to prove control of the domain. */
    public function preVerified(User $user, string $customerId, string $name): bool
    {
        if ($user->isAdministrator()) {
            return true;
        }

        return Domain::whereNotNull('verified_at')
            ->whereHas('website', fn ($q) => $q->where('customer_id', $customerId))
            ->pluck('name')
            ->contains(fn (string $verified) => self::within($name, $verified));
    }

    /** True when the TXT record of the domain holds its verification token. */
    public function check(Domain $domain): bool
    {
        return $this->hasTxt($domain->verificationRecord(), (string) $domain->verification_token);
    }

    private function hasTxt(string $record, string $token): bool
    {
        foreach ($this->txtRecords($record) as $value) {
            if ($token !== '' && hash_equals($token, trim($value))) {
                return true;
            }
        }

        return false;
    }

    /** @return list<string> */
    protected function txtRecords(string $name): array
    {
        $records = @dns_get_record($name, DNS_TXT) ?: [];

        return array_values(array_map(fn ($r) => (string) ($r['txt'] ?? ''), $records));
    }

    /** @param  list<mixed>  $domains */
    private static function normalise(array $domains): array
    {
        return array_values(array_unique(array_filter(array_map(fn ($d) => is_string($d) ? strtolower(trim($d, '.')) : null, $domains))));
    }

    /** True when $name is $domain or a sub-domain of $domain. */
    public static function within(string $name, string $domain): bool
    {
        return $name === $domain || str_ends_with($name, '.'.$domain);
    }
}
