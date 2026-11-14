<?php

namespace App\Services;

use App\Enums\AgentAction;
use App\Enums\JobState;
use App\Jobs\SendJobToAgent;
use App\Models\AgentJob;
use App\Models\DatabaseServer;
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
    ): AgentJob {
        $job = AgentJob::create([
            'server_id' => $server->id,
            'website_id' => $website?->id,
            // The job of a database server has no website. The owner of the database server
            // can see the job.
            'database_server_id' => $databaseServer?->id,
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
                // Batch 1, finding 11: the directory in /app that the web server serves.
                'documentRoot' => $website->document_root ?? 'public',
                // FR-307: the port and the start command of a custom image.
                'containerPort' => $website->image->custom ? (int) $website->image->container_port : 0,
                'command' => $website->image->custom ? ($website->image->command ?? []) : [],
                // CF-03: the edge proxy serves only the domains that the customer verified.
                'domains' => $website->domains->filter(fn ($d) => $d->verified())->map(fn ($d) => ['name' => $d->name, 'kind' => $d->kind->value])->values()->all(),
                // The customer whose database servers the website can use on its server. An empty
                // value is a website with the shared database containers of an earlier release.
                'databaseInstance' => $website->database_instance ?? '',
            ],
        ];
    }
}
