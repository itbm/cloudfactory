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
    ): AgentJob {
        $job = AgentJob::create([
            'server_id' => $server->id,
            'website_id' => $website?->id,
            // The job of a database server has no website. The owner of the database server
            // can see the job.
            'database_server_id' => $databaseServer?->id,
            // The job of one DNS zone. The owner of the zone can see the job.
            'dns_zone_id' => $dnsZone?->id,
            'rollout_id' => $rolloutId,
            'action' => $action,
            'title' => $title,
            'ref' => $ref,
            'payload' => $payload,
            'steps' => $action->steps(),
            'step' => 0,
            'state' => JobState::Queued,
        ]);

        SendJobToAgent::dispatch($job->id);

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
                'domains' => $website->domains->filter(fn ($d) => $d->verified())->map(fn ($d) => ['name' => $d->name, 'kind' => $d->kind->value])->values()->all(),
                // The customer of the website. The website can use the database servers of this
                // customer on its server.
                'databaseInstance' => $website->customer_id,
            ],
        ];
    }
}
