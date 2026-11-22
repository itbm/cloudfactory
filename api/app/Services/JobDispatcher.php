<?php

namespace App\Services;

use App\Enums\AgentAction;
use App\Enums\JobState;
use App\Jobs\SendJobToAgent;
use App\Models\AgentJob;
use App\Models\DatabaseServer;
use App\Models\DnsZone;
use App\Models\Server;
use App\Models\Website;

/** Spec section 6.3, step 4: put a job in the queue for the agent on the correct server. */
class JobDispatcher
{
    /**
     * FR-1104: the health check after a change of the image (RolloutEngine) and after a deploy
     * (FR-1008). The agent checks the website for 5 minutes after the switch, and goes back
     * when more than 5% of the checks fail. Batch 1, finding 6: status 0 accepts each answer
     * below 500, for example a redirect to a login page. The agent makes about 30 checks.
     */
    public const HEALTH_CHECK = ['status' => 0, 'maxErrorRate' => 0.05, 'seconds' => 300];

    /** The import whose jobs the panel makes now (forImport), or null. */
    private static ?string $importId = null;

    /**
     * Runs $fn, and gives each job that it makes the import. The panel then continues the
     * import when the job ends (Imports\Runner::jobFinished). The import uses the controllers
     * of the API to make its websites, databases and mail, so their jobs get the import here.
     *
     * @template T
     *
     * @param  callable(): T  $fn
     * @return T
     */
    public static function forImport(string $importId, callable $fn): mixed
    {
        $previous = self::$importId;
        self::$importId = $importId;
        try {
            return $fn();
        } finally {
            self::$importId = $previous;
        }
    }

    public function dispatch(
        Server $server,
        AgentAction $action,
        array $payload,
        string $title,
        ?Website $website = null,
        ?string $ref = null,
        ?string $rolloutId = null,
        ?DatabaseServer $databaseServer = null,
        ?DnsZone $dnsZone = null,
        ?string $customerId = null,
    ): AgentJob {
        // The members of a mail cluster share their data. A different member does the job of
        // a member that is offline.
        $server = MailClusters::target($server, $action);
        $job = AgentJob::create([
            'server_id' => $server->id,
            'website_id' => $website?->id,
            // The job of a database server has no website. The owner of the database server
            // can see the job.
            'database_server_id' => $databaseServer?->id,
            // The job of one DNS zone. The owner of the zone can see the job.
            'dns_zone_id' => $dnsZone?->id,
            // A mail job: the users of this customer can see it.
            // Adversarial review, finding A9-10: each job of a customer has the customer. The
            // owner can then read the job after the website, the database server or the zone
            // is removed.
            'customer_id' => $customerId ?? $website?->customer_id ?? $databaseServer?->customer_id ?? $dnsZone?->customer_id,
            'rollout_id' => $rolloutId,
            'import_id' => self::$importId,
            'action' => $action,
            'title' => $title,
            'ref' => $ref,
            'payload' => $payload,
            'steps' => $action->steps(),
            'step' => 0,
            'state' => JobState::Queued,
        ]);

        SendJobToAgent::dispatch($job->id)->afterCommit();

        return $job;
    }

    /** The website fields that the agent needs to apply the desired state. */
    public static function websitePayload(Website $website): array
    {
        $website->loadMissing(['image', 'domains']);

        return [
            'website' => [
                'id' => $website->id,
                'unixUser' => $website->unix_user,
                'image' => $website->image->reference.':'.$website->currentTag(),
                'imageDigest' => $website->image->digest,
                'runtime' => $website->image->family,
                'phpVersion' => $website->image->php_version,
                'workerMode' => $website->worker_mode,
                'extensions' => $website->extensions,
                'phpIni' => (object) $website->php_ini,
                'limits' => $website->limits,
                // Batch 1, finding 11: the directory in /app that the web server serves. For a
                // Node.js or Python website, the application directory.
                'documentRoot' => $website->document_root ?? ($website->image->isApp() ? '.' : 'public'),
                // FR-307: the port and the start command of a custom image.
                'containerPort' => $website->image->custom ? (int) $website->image->container_port : 0,
                // A worker, and a Node.js or Python website, runs the command of the customer in
                // the shell of its container, as a cron job does. The container is the gVisor
                // sandbox of the website.
                'command' => $website->hasStartCommand() && $website->command !== null
                    ? ['sh', '-c', $website->command]
                    : ($website->image->custom ? ($website->image->command ?? []) : []),
                // The environment variables are not in the job: the panel adds the current
                // values each time that it sends the job (AgentClient).
                'worker' => $website->isWorker(),
                // CF-03: the edge proxy serves only the domains that the customer verified.
                'domains' => self::servedDomains($website),
                // The customer of the website. The website can use the database servers of this
                // customer on its server.
                'databaseInstance' => $website->customer_id,
            ],
        ];
    }

    /**
     * The domains of a website that the edge proxy serves: the verified domains (CF-03). The
     * agent needs a primary domain when there are domains. When the primary domain is not
     * verified, the first verified alias is the primary domain for the agent (adversarial
     * review, finding A2-08). Without this, such a website could not change its runtime.
     *
     * @return list<array{name: string, kind: string}>
     */
    public static function servedDomains(Website $website): array
    {
        $domains = $website->domains->filter(fn ($d) => $d->verified())->map(fn ($d) => ['name' => $d->name, 'kind' => $d->kind->value])->values()->all();
        if ($domains && ! in_array('primary', array_column($domains, 'kind'), true)) {
            foreach ($domains as $i => $domain) {
                if ($domain['kind'] === 'alias') {
                    $domains[$i]['kind'] = 'primary';
                    break;
                }
            }
        }

        return $domains;
    }
}
