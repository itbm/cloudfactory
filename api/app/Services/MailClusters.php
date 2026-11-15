<?php

namespace App\Services;

use App\Enums\AgentAction;
use App\Enums\JobState;
use App\Models\AgentJob;
use App\Models\MailCluster;
use App\Models\Server;

/**
 * The setup of a mail cluster. The panel makes a cluster in steps, and each step starts when
 * the jobs of the step before it are complete:
 *
 * 1. "prepare": each server makes its keys and gives the public keys (PrepareMailNode).
 * 2. "bootstrap": the first server gets the keys of all members, makes the private network
 *    and starts the database cluster (SetMailNode).
 * 3. "join": the other servers do the same and join the database cluster.
 * 4. "first": the mail server of the first server starts and writes the settings to the
 *    database (SetMailServer).
 * 5. "rest": the other mail servers start with those settings.
 *
 * A job that fails stops the setup. An administrator starts the same step again (retry).
 */
class MailClusters
{
    /** The mail actions that each member of a cluster can do: the members share their data. */
    private const SHARED = [
        AgentAction::SetMailDomain, AgentAction::DeleteMailDomain, AgentAction::SetMailbox,
        AgentAction::DeleteMailbox, AgentAction::SetMailAlias, AgentAction::DeleteMailAlias,
    ];

    public function __construct(private JobDispatcher $jobs) {}

    /** Starts the step of the cluster, or starts it again after a failure. */
    public function run(MailCluster $cluster): void
    {
        $cluster->update(['status' => MailCluster::SETTING_UP, 'error' => null]);
        $members = $cluster->load('servers')->members();
        $first = $members->first();
        $todo = match ($cluster->phase) {
            'prepare' => $members->filter(fn (Server $s) => self::stepOf($s) === 'new'),
            'bootstrap' => collect([$first])->filter(fn (Server $s) => self::stepOf($s) === 'prepared'),
            'join' => $members->skip(1)->filter(fn (Server $s) => self::stepOf($s) === 'prepared'),
            'first' => collect([$first])->filter(fn (Server $s) => self::stepOf($s) === 'joined'),
            'rest' => $members->skip(1)->filter(fn (Server $s) => self::stepOf($s) === 'joined'),
            default => collect(),
        };
        if ($todo->isEmpty()) {
            $this->next($cluster);

            return;
        }
        foreach ($todo as $server) {
            match ($cluster->phase) {
                'prepare' => $this->jobs->dispatch($server, AgentAction::PrepareMailNode, ['mailClusterId' => $cluster->id], "Prepare {$server->name} for mail cluster {$cluster->name}"),
                'bootstrap', 'join' => $this->jobs->dispatch($server, AgentAction::SetMailNode, self::nodePayload($cluster, $server, $cluster->phase === 'bootstrap'), "Add {$server->name} to mail cluster {$cluster->name}"),
                default => $this->startMail($cluster, $server),
            };
        }
    }

    private function startMail(MailCluster $cluster, Server $server): void
    {
        $server->update(['mail_status' => 'starting']);
        $this->jobs->dispatch($server, AgentAction::SetMailServer, ['hostname' => $server->mail_hostname, 'mailClusterId' => $cluster->id], "Start mail server of {$server->name} in mail cluster {$cluster->name}");
    }

    /** Goes to the step after a complete step. */
    private function next(MailCluster $cluster): void
    {
        $at = array_search($cluster->phase, MailCluster::PHASES, true);
        $phase = MailCluster::PHASES[min($at + 1, count(MailCluster::PHASES) - 1)];
        if ($phase === 'done') {
            $cluster->update(['phase' => 'done', 'status' => MailCluster::RUNNING, 'error' => null]);

            return;
        }
        $cluster->update(['phase' => $phase]);
        $this->run($cluster);
    }

    /** Records the end of a job of the setup, and starts the next step when the step is complete. */
    public function finished(AgentJob $job, bool $succeeded, array $result): void
    {
        $cluster = MailCluster::find($job->payload['mailClusterId'] ?? '');
        $server = $job->server;
        if (! $cluster || $server->mail_cluster_id !== $cluster->id) {
            return;
        }
        if (! $succeeded) {
            $cluster->update(['status' => MailCluster::FAILED, 'error' => "{$server->name}: ".($job->fresh()->error ?? 'the job failed')]);

            return;
        }
        $member = $server->mail_cluster_member;
        if ($job->action === AgentAction::PrepareMailNode) {
            $keys = self::keys($result['keys'] ?? null);
            if ($keys === null) {
                $cluster->update(['status' => MailCluster::FAILED, 'error' => "{$server->name}: the agent gave no keys."]);

                return;
            }
            $member['keys'] = $keys;
        }
        $member['step'] = match ($job->action) {
            AgentAction::PrepareMailNode => 'prepared',
            AgentAction::SetMailNode => 'joined',
            default => 'running',
        };
        $server->update(['mail_cluster_member' => $member]);
        if ($cluster->status === MailCluster::FAILED) {
            // A different job of the step failed. The administrator starts the step again.
            return;
        }
        $open = AgentJob::whereIn('server_id', $cluster->servers()->pluck('id'))
            ->whereIn('action', [AgentAction::PrepareMailNode->value, AgentAction::SetMailNode->value, AgentAction::SetMailServer->value])
            ->whereIn('state', [JobState::Queued->value, JobState::Running->value])->exists();
        if (! $open) {
            $this->run($cluster->fresh());
        }
    }

    private static function stepOf(Server $server): string
    {
        return $server->mail_cluster_member['step'] ?? 'new';
    }

    /**
     * The public keys of a member from the result of a job. The agent of the server checks
     * each key again before it uses the key.
     *
     * @return array{wireguardKey: string, keepKey: string, nodeKey: string}|null
     */
    private static function keys(mixed $keys): ?array
    {
        $out = [];
        foreach (['wireguardKey' => '/^[A-Za-z0-9+\/]{43}=$/', 'keepKey' => '/^-----BEGIN PUBLIC KEY-----\n[A-Za-z0-9+\/=\n]{40,200}-----END PUBLIC KEY-----\n$/', 'nodeKey' => '/^-----BEGIN PUBLIC KEY-----\n[A-Za-z0-9+\/=\n]{40,200}-----END PUBLIC KEY-----\n$/'] as $name => $rule) {
            if (! is_array($keys) || ! is_string($keys[$name] ?? null) || ! preg_match($rule, $keys[$name])) {
                return null;
            }
            $out[$name] = $keys[$name];
        }

        return $out;
    }

    /** The values of SetMailNode, without the secrets (refer to secretsFor). */
    private static function nodePayload(MailCluster $cluster, Server $server, bool $bootstrap): array
    {
        return [
            'mailClusterId' => $cluster->id,
            'cluster' => $cluster->name,
            'hostname' => $cluster->hostname,
            'self' => $server->mail_cluster_member['id'],
            'members' => $cluster->members()->map(fn (Server $s) => [
                'id' => $s->mail_cluster_member['id'],
                'name' => $s->mail_hostname,
                'endpoint' => $s->mail_cluster_member['endpoint'],
                'address' => $s->mail_cluster_member['address'],
                'region' => $s->mail_cluster_member['region'],
                'wireguardKey' => $s->mail_cluster_member['keys']['wireguardKey'] ?? '',
                'keepKey' => $s->mail_cluster_member['keys']['keepKey'] ?? '',
                'nodeKey' => $s->mail_cluster_member['keys']['nodeKey'] ?? '',
            ])->all(),
            'bootstrap' => $bootstrap,
        ];
    }

    /**
     * The secrets of the cluster for a job. They are not in the job: the panel adds them each
     * time that it sends the job to the agent, as it adds the credentials of the backups.
     *
     * @return array<string, string>
     */
    public static function secretsFor(AgentJob $job): array
    {
        if ($job->action !== AgentAction::SetMailNode) {
            return [];
        }
        $secrets = MailCluster::find($job->payload['mailClusterId'] ?? '')?->secrets ?? [];

        return ['dbPassword' => $secrets['dbPassword'] ?? '', 'keepSecret' => $secrets['keepSecret'] ?? '', 'peerSecret' => $secrets['peerSecret'] ?? ''];
    }

    /**
     * The server that does a mail job. The members of a cluster share their data: when the
     * server of a mail domain is offline, a different member does the job.
     */
    public static function target(Server $server, AgentAction $action): Server
    {
        if ($server->mail_cluster_id === null || $server->state !== 'offline' || ! in_array($action, self::SHARED, true)) {
            return $server;
        }

        return Server::where('mail_cluster_id', $server->mail_cluster_id)->where('state', '!=', 'offline')->where('mail_status', 'running')->orderBy('name')->first() ?? $server;
    }

    /** A private network for a new cluster that no other cluster has: 10.77.N.0/24. */
    public static function freeNetwork(): string
    {
        $used = MailCluster::pluck('network')->all();
        for ($n = 16; $n < 250; $n++) {
            if (! in_array("10.77.{$n}", $used, true)) {
                return "10.77.{$n}";
            }
        }
        abort(409, 'The panel has no free private network for a new mail cluster.');
    }
}
