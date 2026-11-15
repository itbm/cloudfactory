<?php

namespace App\Services;

use App\Enums\AgentAction;
use App\Enums\JobState;
use App\Models\AgentJob;
use App\Models\Database;
use App\Models\DatabaseUser;
use App\Models\Website;
use Illuminate\Support\Collection;

/**
 * Removes a website.
 *
 * The agent of the server of the website removes the website, with its databases and its
 * database users in the database servers on that server (DeleteWebsite).
 *
 * A website can also have databases in a database server on a different server. Only the
 * agent of that server can remove them. Thus the panel first sends a job for each of them to
 * that agent. When the last of these jobs is complete, the panel sends DeleteWebsite
 * (JobOutcome). When one fails, the website stays, and the user can remove it again.
 */
class WebsiteRemoval
{
    /** The payload value that tells JobOutcome to continue with the removal of the website. */
    public const THEN = 'removeWebsite';

    public function __construct(private JobDispatcher $jobs) {}

    /** Starts the removal. It gives the first job. */
    public function start(Website $website): AgentJob
    {
        [$databases, $users] = self::remote($website);
        if ($databases->isEmpty() && $users->isEmpty()) {
            return $this->removeWebsite($website);
        }
        $first = null;
        foreach ($users as $user) {
            $first ??= $this->jobs->dispatch($user->databaseServer->server, AgentAction::DeleteDatabaseUser, [
                'websiteId' => $website->id,
                'unixUser' => $website->unix_user,
                'engine' => $user->engine,
                'name' => $user->name,
                'websiteDatabases' => [],
                'databaseServer' => $user->database_server_id,
                'customer' => $website->customer_id,
                self::THEN => true,
            ], "Remove database user {$user->name}", $website, databaseServer: $user->databaseServer);
        }
        foreach ($databases as $database) {
            $job = $this->jobs->dispatch($database->databaseServer->server, AgentAction::DeleteDatabase, [
                'websiteId' => $website->id,
                'unixUser' => $website->unix_user,
                'engine' => $database->engine,
                'name' => $database->name,
                // The job of each user removes the user. The agent removes the access to the
                // database with the database.
                'users' => [],
                'databaseServer' => $database->database_server_id,
                'customer' => $website->customer_id,
                self::THEN => true,
            ], "Remove database {$database->name}", $website, databaseServer: $database->databaseServer);
            $first ??= $job;
        }

        return $first;
    }

    /**
     * Continues the removal after a job for a database or a database user on a different
     * server is complete. It sends DeleteWebsite when no such database and no such user stays.
     */
    public function continue(Website $website): ?AgentJob
    {
        [$databases, $users] = self::remote($website);
        if ($databases->isNotEmpty() || $users->isNotEmpty()) {
            return null;
        }
        $open = AgentJob::where(['website_id' => $website->id, 'action' => AgentAction::DeleteWebsite])
            ->whereIn('state', [JobState::Queued, JobState::Running])->exists();

        return $open ? null : $this->removeWebsite($website);
    }

    /**
     * The databases and the database users of the website in database servers on a different
     * server. The user of a Valkey database goes with its database.
     *
     * @return array{0: Collection<int, Database>, 1: Collection<int, DatabaseUser>}
     */
    public static function remote(Website $website): array
    {
        $remote = fn ($item) => $item->databaseServer && $item->databaseServer->server_id !== $website->server_id;

        return [
            $website->databases()->with('databaseServer.server')->orderBy('name')->get()->filter($remote)->values(),
            $website->databaseUsers()->with('databaseServer.server')->where('engine', '!=', Database::VALKEY)->orderBy('name')->get()->filter($remote)->values(),
        ];
    }

    /**
     * The agent also removes the databases and the database users of the website, so that a
     * later website with the same Unix user does not get them. The database servers of the
     * customer stay.
     */
    private function removeWebsite(Website $website): AgentJob
    {
        $ref = fn ($item) => ['engine' => $item->engine, 'name' => $item->name, 'databaseServer' => $item->database_server_id];

        return $this->jobs->dispatch($website->server, AgentAction::DeleteWebsite, [
            'websiteId' => $website->id,
            'unixUser' => $website->unix_user,
            'databases' => $website->databases()->orderBy('name')->get()->map($ref)->all(),
            'databaseUsers' => $website->databaseUsers()->orderBy('name')->get()->map($ref)->all(),
        ], "Remove website {$website->name}", $website);
    }
}
