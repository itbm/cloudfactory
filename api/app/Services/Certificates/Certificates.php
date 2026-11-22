<?php

namespace App\Services\Certificates;

use App\Enums\AgentAction;
use App\Enums\JobState;
use App\Models\AgentJob;
use App\Models\Certificate;
use App\Models\Server;
use App\Models\Website;
use App\Services\DatabaseTools;
use App\Services\DomainPolicy;
use App\Services\JobDispatcher;
use Illuminate\Support\Carbon;

/**
 * The certificates from the panel on the servers (docs/certificates.md).
 *
 * Each verified domain of a website uses the best usable certificate of the customer of the
 * website that is valid for it: a certificate with the exact name first, then a wildcard
 * certificate, then the certificate that expires last. A domain with no such certificate gets
 * a certificate from the edge proxy (Let's Encrypt, or the panel CA for a local name).
 *
 * The panel does not send a certificate to a server when it is also valid for a name that it
 * must not serve there:
 *
 * - a host name of the panel, of a server, of a plugin, of Adminer or of a mail server, or a
 *   panel domain of a reseller;
 * - a domain of a different website on the same server that does not use a certificate from
 *   the panel. The edge proxy could otherwise send that certificate for such a domain, for
 *   example a certificate of one customer for the domain of a different customer.
 *
 * The edge proxy of each server gets the full list of its certificates (SetCertificates). The
 * panel sends the list again when a server reports a different list.
 */
class Certificates
{
    /** The longest time between two tries to send the certificates again to one server, in minutes. */
    public const RESYNC_MAX_MINUTES = 60;

    public function __construct(private JobDispatcher $jobs, private DomainPolicy $policy) {}

    /**
     * The best certificate for a host among the certificates of one customer, or null.
     *
     * @param  iterable<Certificate>  $certificates
     */
    public static function forHost(iterable $certificates, string $host): ?Certificate
    {
        $best = null;
        foreach ($certificates as $c) {
            if (! $c->usable() || ! $c->covers($host)) {
                continue;
            }
            if ($best === null || self::better($c, $best, $host)) {
                $best = $c;
            }
        }

        return $best;
    }

    private static function better(Certificate $a, Certificate $b, string $host): bool
    {
        if ($a->hasName($host) !== $b->hasName($host)) {
            return $a->hasName($host);
        }
        if (! $a->expires_at->eq($b->expires_at)) {
            return $a->expires_at->gt($b->expires_at);
        }

        return strcmp($a->id, $b->id) > 0;
    }

    /**
     * The names that a certificate from the panel must not be valid for: the host names of the
     * panel, of the servers, of the plugins, of Adminer and of the mail servers, and the panel
     * domains of the resellers.
     *
     * @return list<string>
     */
    public function reserved(): array
    {
        $names = [...$this->policy->reserved(), ...$this->policy->brandDomains(), DatabaseTools::domain()];
        foreach (Server::whereNotNull('mail_hostname')->pluck('mail_hostname') as $host) {
            $names[] = strtolower((string) $host);
        }

        return array_values(array_unique(array_filter($names)));
    }

    /**
     * The certificates of a server, each with the hosts that use it, in the order of their IDs.
     *
     * @return list<array{certificate: Certificate, hosts: list<string>}>
     */
    public function forServer(Server $server): array
    {
        // The served domains of each website of the server, and the customer of each one.
        $hosts = [];
        foreach (Website::with('domains')->where('server_id', $server->id)->get() as $website) {
            foreach (JobDispatcher::servedDomains($website) as $d) {
                $hosts[$d['name']] = $website->customer_id;
            }
        }
        if ($hosts === []) {
            return [];
        }
        $byCustomer = Certificate::whereIn('customer_id', array_unique(array_values($hosts)))->whereNotNull('certificate')->get()
            ->filter(fn (Certificate $c) => $c->usable())->groupBy('customer_id');
        if ($byCustomer->isEmpty()) {
            return [];
        }
        $excluded = [];
        $reserved = $this->reserved();
        foreach ($byCustomer->flatten() as $c) {
            foreach ($reserved as $name) {
                if ($c->covers($name)) {
                    $excluded[$c->id] = true;
                    break;
                }
            }
        }
        // A certificate that is valid for a host of the server with no certificate from the
        // panel does not go to the server. Its own hosts then have no certificate from the
        // panel either, so the check runs again until nothing changes.
        do {
            $map = [];
            foreach ($hosts as $host => $customerId) {
                $best = self::forHost(($byCustomer->get($customerId) ?? collect())->reject(fn (Certificate $c) => isset($excluded[$c->id])), $host);
                if ($best) {
                    $map[$host] = $best;
                }
            }
            $changed = false;
            foreach (collect($map)->unique('id') as $c) {
                foreach ($hosts as $host => $_) {
                    if (! isset($map[$host]) && $c->covers($host)) {
                        $excluded[$c->id] = true;
                        $changed = true;
                        break;
                    }
                }
            }
        } while ($changed);

        $out = [];
        foreach ($map as $host => $c) {
            $out[$c->id] ??= ['certificate' => $c, 'hosts' => []];
            $out[$c->id]['hosts'][] = $host;
        }
        ksort($out);

        return array_values(array_map(function (array $e) {
            sort($e['hosts']);

            return $e;
        }, $out));
    }

    /**
     * The certificates of a server for the agent, with their private keys. AgentClient adds
     * them when it sends a SetCertificates job: they are not in the job record.
     *
     * @return list<array{id: string, hosts: list<string>, certificatePem: string, privateKeyPem: string}>
     */
    public function forAgent(Server $server): array
    {
        return array_map(fn (array $e) => [
            'id' => $e['certificate']->id,
            'hosts' => $e['hosts'],
            'certificatePem' => (string) $e['certificate']->certificate,
            'privateKeyPem' => (string) $e['certificate']->private_key,
        ], $this->forServer($server));
    }

    /**
     * The state that the agent of the server must report: the ID, the fingerprint and the
     * hosts of each certificate.
     *
     * @return list<array{id: string, sha256: string, hosts: list<string>}>
     */
    public function wanted(Server $server): array
    {
        return array_map(fn (array $e) => ['id' => $e['certificate']->id, 'sha256' => (string) $e['certificate']->fingerprint, 'hosts' => $e['hosts']], $this->forServer($server));
    }

    /** Sends the certificates to a server. The job has no certificates: AgentClient adds them. */
    public function send(Server $server, string $title): AgentJob
    {
        return $this->jobs->dispatch($server, AgentAction::SetCertificates, [], $title);
    }

    /**
     * Sends the certificates again to each server that has a website of the customer or a
     * certificate of the customer now. Gives the jobs.
     *
     * @return list<AgentJob>
     */
    public function syncCustomer(string $customerId, string $why): array
    {
        $ids = Certificate::where('customer_id', $customerId)->pluck('id')->all();
        $servers = Server::whereIn('id', Website::where('customer_id', $customerId)->whereNotNull('server_id')->pluck('server_id'))->get();
        foreach (Server::whereNotNull('agent_certificates')->get() as $s) {
            if (array_intersect(array_column((array) $s->agent_certificates, 'id'), $ids) !== []) {
                $servers->push($s);
            }
        }
        $jobs = [];
        foreach ($servers->unique('id')->sortBy('name') as $server) {
            if ($server->state === 'joining') {
                continue;
            }
            $server->update(['certificates_resync' => null]);
            $jobs[] = $this->send($server, "{$why} on {$server->name}");
        }

        return $jobs;
    }

    /**
     * The domains of a server changed: a website or a domain came or went. The server gets its
     * certificates again when the customer has certificates, or when the server has
     * certificates from the panel.
     */
    public function serverChanged(Server $server, ?string $customerId): void
    {
        if ($server->state === 'joining') {
            return;
        }
        if (($customerId === null || ! Certificate::where('customer_id', $customerId)->exists()) && empty($server->agent_certificates)) {
            return;
        }
        if (self::busy($server)) {
            // A job in the queue gets the certificates when the panel sends it.
            return;
        }
        $this->send($server, "Update the certificates on {$server->name}");
    }

    /**
     * Compares the certificates that a server reported with the certificates that it must
     * have, and sends them again when they differ. The agent sends the report each minute. A
     * certificate that expired, or an edge proxy that lost its configuration, thus gets the
     * correct certificates with no work of a user. After a try with no result, the panel waits
     * 1 minute, then 2, then 4, up to RESYNC_MAX_MINUTES.
     *
     * @param  list<array{id: string, sha256: string, hosts: list<string>}>  $reported
     */
    public function reconcile(Server $server, array $reported): void
    {
        $normal = fn (array $list) => collect($list)
            ->map(fn (array $c) => ['id' => (string) $c['id'], 'sha256' => strtolower((string) $c['sha256']), 'hosts' => collect($c['hosts'] ?? [])->map(fn ($h) => strtolower((string) $h))->sort()->values()->all()])
            ->sortBy('id')->values()->all();
        if ($normal($reported) === $normal($this->wanted($server))) {
            if ($server->certificates_resync !== null) {
                $server->update(['certificates_resync' => null]);
            }

            return;
        }
        if (self::busy($server)) {
            return;
        }
        $tries = (int) ($server->certificates_resync['tries'] ?? 0);
        $last = $server->certificates_resync['at'] ?? null;
        $wait = $tries === 0 ? 0 : min(self::RESYNC_MAX_MINUTES, 2 ** ($tries - 1));
        if ($last !== null && now()->lt(Carbon::parse($last)->addMinutes($wait))) {
            return;
        }
        $server->update(['certificates_resync' => ['at' => now()->toIso8601String(), 'tries' => $tries + 1]]);
        $this->send($server, "Send the certificates to {$server->name} again");
    }

    /**
     * The certificates that the server reported, in the form of the panel, or null when the
     * report has none.
     *
     * @return list<array{id: string, sha256: string, hosts: list<string>}>|null
     */
    public static function normalReport(mixed $reported): ?array
    {
        if (! is_array($reported)) {
            return null;
        }

        return array_values(array_map(fn (array $c) => [
            'id' => (string) ($c['id'] ?? ''),
            'sha256' => strtolower((string) ($c['sha256'] ?? '')),
            'hosts' => array_values(array_map(fn ($h) => strtolower((string) $h), array_filter((array) ($c['hosts'] ?? []), 'is_string'))),
        ], array_filter($reported, 'is_array')));
    }

    /** A SetCertificates job is complete: the panel keeps the certificates that the server has now. */
    public static function jobFinished(AgentJob $job, array $result): void
    {
        $loaded = self::normalReport($result['certificates'] ?? null);
        if ($loaded !== null) {
            $job->server?->update(['agent_certificates' => $loaded]);
        }
    }

    /** True while a SetCertificates job of the server is in the queue or runs. */
    public static function busy(Server $server): bool
    {
        return AgentJob::where('server_id', $server->id)->where('action', AgentAction::SetCertificates->value)
            ->whereIn('state', [JobState::Queued->value, JobState::Running->value])->exists();
    }

    /**
     * The domains that use each certificate now, from the reports of the servers: [certificate
     * ID => list of domains, each with its website].
     *
     * @param  iterable<Certificate>  $certificates
     * @return array<string, list<array{domain: string, websiteId: ?string, websiteName: ?string, serverName: string}>>
     */
    public static function usage(iterable $certificates): array
    {
        $customers = [];
        foreach ($certificates as $c) {
            $customers[$c->id] = $c->customer_id;
        }
        $out = array_fill_keys(array_keys($customers), []);
        if ($customers === []) {
            return $out;
        }
        foreach (Server::whereNotNull('agent_certificates')->get() as $server) {
            $hosts = [];
            foreach ((array) $server->agent_certificates as $c) {
                if (isset($customers[$c['id'] ?? ''])) {
                    $hosts[$c['id']] = (array) ($c['hosts'] ?? []);
                }
            }
            if ($hosts === []) {
                continue;
            }
            // The website of each domain on the server.
            $websites = [];
            foreach (Website::with('domains')->where('server_id', $server->id)->whereIn('customer_id', array_unique(array_intersect_key($customers, $hosts)))->get() as $website) {
                foreach ($website->domains as $d) {
                    $websites[$website->customer_id][$d->name] = $website;
                }
            }
            foreach ($hosts as $id => $list) {
                foreach ($list as $host) {
                    $website = $websites[$customers[$id]][(string) $host] ?? null;
                    $out[$id][] = ['domain' => (string) $host, 'websiteId' => $website?->id, 'websiteName' => $website?->name, 'serverName' => $server->name];
                }
            }
        }

        return array_map(fn (array $list) => collect($list)->sortBy('domain')->values()->all(), $out);
    }

    /**
     * The certificate from the panel that the server of a website uses for each of its domains,
     * from the report of the server: [domain => certificate].
     *
     * @return array<string, Certificate>
     */
    public static function ofWebsite(Website $website): array
    {
        $reported = (array) ($website->server?->agent_certificates ?? []);
        if ($reported === []) {
            return [];
        }
        $certificates = Certificate::whereIn('id', array_column($reported, 'id'))->get(['id', 'customer_id', 'source', 'names', 'expires_at'])->keyBy('id');
        $out = [];
        foreach ($reported as $c) {
            $cert = $certificates->get($c['id'] ?? '');
            if ($cert === null || $cert->customer_id !== $website->customer_id) {
                continue;
            }
            foreach ((array) ($c['hosts'] ?? []) as $host) {
                $out[(string) $host] = $cert;
            }
        }

        return $out;
    }
}
