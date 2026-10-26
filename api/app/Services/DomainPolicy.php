<?php

namespace App\Services;

use App\Models\Customer;
use App\Models\Domain;
use App\Models\Server;
use App\Models\User;

/**
 * CF-03 and CF-20: which domains a customer can use.
 *
 * - Nobody can use the panel domain, a reseller panel domain, the host name of a server, or a
 *   sub-domain of one of these.
 * - The edge proxy serves a domain only after the customer proves that they control it, with a
 *   DNS TXT record. A domain is verified at once when the user is an administrator, or when it
 *   is a verified domain of the same customer or a sub-domain of one.
 */
class DomainPolicy
{
    /** The domains that nobody can attach to a website. */
    public function reserved(?string $exceptCustomerId = null): array
    {
        $domains = [config('cloudfactory.panel_domain')];
        foreach (Customer::where('role', 'reseller')->when($exceptCustomerId, fn ($q, $id) => $q->where('id', '!=', $id))->get(['branding']) as $reseller) {
            $domains[] = $reseller->branding['panelDomain'] ?? null;
        }
        foreach (Server::pluck('agent_url') as $url) {
            $host = parse_url((string) $url, PHP_URL_HOST);
            if (is_string($host) && str_contains($host, '.') && ! filter_var($host, FILTER_VALIDATE_IP)) {
                $domains[] = $host;
            }
        }

        return array_values(array_unique(array_filter(array_map(fn ($d) => is_string($d) ? strtolower(trim($d, '.')) : null, $domains))));
    }

    /** True when the name is a reserved domain or a sub-domain of one. */
    public function isReserved(string $name, ?string $exceptCustomerId = null): bool
    {
        foreach ($this->reserved($exceptCustomerId) as $domain) {
            if (self::within($name, $domain)) {
                return true;
            }
        }

        return false;
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
        foreach ($this->txtRecords($domain->verificationRecord()) as $value) {
            if (hash_equals((string) $domain->verification_token, trim($value))) {
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

    /** True when $name is $domain or a sub-domain of $domain. */
    public static function within(string $name, string $domain): bool
    {
        return $name === $domain || str_ends_with($name, '.'.$domain);
    }
}
