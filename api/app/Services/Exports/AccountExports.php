<?php

namespace App\Services\Exports;

use App\Enums\AgentAction;
use App\Enums\JobState;
use App\Jobs\BuildAccountExport;
use App\Models\AccountExport;
use App\Models\AccountExportPart;
use App\Models\AgentJob;
use App\Models\Customer;
use App\Models\Database;
use App\Models\DatabaseServer;
use App\Models\DnsZone;
use App\Models\Server;
use App\Models\User;
use App\Models\Website;
use App\Services\DatabaseServers;
use App\Services\JobDispatcher;
use Illuminate\Http\Exceptions\HttpResponseException;
use Illuminate\Support\Facades\DB;
use Illuminate\Support\Facades\Log;
use Illuminate\Support\Str;
use Illuminate\Validation\ValidationException;
use Symfony\Component\HttpKernel\Exception\HttpException;

/**
 * Account exports (docs/account-export.md).
 *
 *  1. A user starts an export. The panel keeps the description of the account (Description)
 *     and sends one ExportWebsite job for each website to its server, and one for the
 *     databases of the website in the database servers on each different server.
 *  2. Each agent sends its parts to PUT /api/agent/v1/export-parts/{id} in pieces (receive).
 *     The panel writes each part to a file in the directory of the export.
 *  3. The result of each job has the size and the SHA-256 hash of each part (jobFinished).
 *  4. When all parts are complete, the queue completes the export (build): it writes
 *     manifest.json, account.json, README.txt and the zone files.
 *  5. The users of the account download the archive until it expires (expire). The panel
 *     sends the archive as one tar file from these files and the parts (archive).
 *
 * The panel keeps the parts on its own disk, in the directory "path" of the settings
 * (/data/exports on a server). It refuses a piece when the disk has less free space than the
 * reserve, so that an export cannot fill the disk of the panel.
 */
class AccountExports
{
    /**
     * The largest piece of a part in one request. The agent has the same limit
     * (panel.ExportChunk). It is less than post_max_size (8 MiB), which Laravel checks for each
     * request (ValidatePostSize).
     */
    public const CHUNK_BYTES = 4 * 1024 * 1024;

    /** The format of the archive (manifest.json). */
    public const FORMAT = 'cf-export/1';

    public function __construct(private JobDispatcher $jobs) {}

    /**
     * Starts an export of the account of a customer.
     *
     * @throws ValidationException
     */
    public function start(Customer $customer, ?User $user, bool $files, bool $databases): AccountExport
    {
        if (AccountExport::where('customer_id', $customer->id)->where('state', AccountExport::RUNNING)->exists()) {
            throw ValidationException::withMessages(['export' => 'An export of this account is in progress. Wait until it is complete.']);
        }
        if ($databases && ($moving = DatabaseServer::where('customer_id', $customer->id)->where('status', DatabaseServer::MOVING)->first())) {
            DatabaseServers::refuseMoving($moving, 'export');
        }
        self::refuseLowSpace();

        return DB::transaction(function () use ($customer, $user, $files, $databases) {
            $export = AccountExport::create([
                'customer_id' => $customer->id,
                'user_id' => $user?->id,
                'state' => AccountExport::RUNNING,
                'files' => $files,
                'databases' => $databases,
                'description' => Description::of($customer, $files, $databases),
            ]);
            $sent = 0;
            if ($files || $databases) {
                $websites = Website::where('customer_id', $customer->id)->with(['server', 'databases.databaseServer.server'])->orderBy('name')->get();
                foreach ($websites as $website) {
                    $sent += $this->sendJobs($export, $website, $files, $databases);
                }
            }
            if ($sent === 0) {
                // An account with no website: the panel has all parts already.
                BuildAccountExport::dispatch($export->id)->afterCommit();
            }
            self::prune($customer, $export);

            return $export;
        });
    }

    /**
     * Sends the jobs of one website: one to the server of the website, and one to each
     * different server with a database server that has a database of the website. Gives the
     * number of jobs.
     */
    private function sendJobs(AccountExport $export, Website $website, bool $files, bool $databases): int
    {
        $byServer = $databases ? $website->databases->filter(fn (Database $d) => in_array($d->engine, Database::SQL_ENGINES, true) && $d->databaseServer)
            ->sortBy('name')->groupBy(fn (Database $d) => $d->databaseServer->server_id) : collect();
        $jobs = 0;
        $withFiles = $files && Description::hasFiles($website);
        $local = $byServer->pull($website->server_id, collect());
        if ($withFiles || $local->isNotEmpty()) {
            $parts = [];
            $filesPart = $withFiles ? $this->part($export, $website->server, Description::filesPath($website->name)) : null;
            if ($filesPart) {
                $parts[] = $filesPart;
            }
            $dumps = $this->dumpParts($export, $website, $website->server, $local, $parts);
            $job = $this->jobs->dispatch($website->server, AgentAction::ExportWebsite, [
                'websiteId' => $website->id,
                'unixUser' => $website->unix_user,
                'filesPart' => $filesPart?->id ?? '',
                'databases' => $dumps,
            ], "Export {$website->name}", $website);
            self::attach($parts, $job);
            $jobs++;
        }
        foreach ($byServer as $databases) {
            $server = $databases->first()->databaseServer->server;
            $parts = [];
            $dumps = $this->dumpParts($export, $website, $server, $databases, $parts);
            $job = $this->jobs->dispatch($server, AgentAction::ExportWebsite, [
                'websiteId' => $website->id,
                'unixUser' => $website->unix_user,
                'databases' => $dumps,
                'databasesOnly' => true,
                // The agent of a different server has no record of the website.
                'customer' => $website->customer_id,
            ], "Export {$website->name}: databases on {$server->name}", $website);
            self::attach($parts, $job);
            $jobs++;
        }

        return $jobs;
    }

    /**
     * @param  iterable<Database>  $databases
     * @param  list<AccountExportPart>  $parts
     * @return list<array{engine: string, name: string, databaseServer: string, part: string}>
     */
    private function dumpParts(AccountExport $export, Website $website, Server $server, iterable $databases, array &$parts): array
    {
        $dumps = [];
        foreach ($databases as $d) {
            $part = $this->part($export, $server, Description::dumpPath($website->name, $d->engine, $d->name));
            $parts[] = $part;
            $dumps[] = ['engine' => $d->engine, 'name' => $d->name, 'databaseServer' => $d->database_server_id, 'part' => $part->id];
        }

        return $dumps;
    }

    private function part(AccountExport $export, Server $server, string $path): AccountExportPart
    {
        return AccountExportPart::create(['account_export_id' => $export->id, 'server_id' => $server->id, 'path' => $path]);
    }

    /** @param list<AccountExportPart> $parts */
    private static function attach(array $parts, AgentJob $job): void
    {
        AccountExportPart::whereIn('id', array_map(fn ($p) => $p->id, $parts))->update(['agent_job_id' => $job->id]);
    }

    /**
     * Receives one piece of a part from the agent that sends the part. Gives the size of the
     * part that the panel has now.
     *
     * @param  resource  $body
     *
     * @throws HttpException|HttpResponseException
     */
    public function receive(Server $server, string $partId, int $offset, $body): int
    {
        return DB::transaction(function () use ($server, $partId, $offset, $body) {
            $part = AccountExportPart::lockForUpdate()->find($partId);
            // A part of a different server is not found, as a part that does not exist.
            abort_if(! $part || $part->server_id !== $server->id, 404, 'The panel has no such export part for this server.');
            $export = $part->export;
            abort_if(! $export || $export->state !== AccountExport::RUNNING, 410, 'The export is not in progress.');
            $job = $part->job;
            abort_if(! $job || $job->state->finished(), 410, 'The job of this export part is complete.');
            $piece = stream_get_contents($body, self::CHUNK_BYTES + 1);
            abort_if($piece === false, 400, 'The panel could not read the piece.');
            abort_if(strlen($piece) > self::CHUNK_BYTES, 413, 'A piece has more than '.self::CHUNK_BYTES.' bytes.');
            if ($offset !== 0 && $offset !== $part->received_bytes) {
                // The agent compares sizeBytes with the end of its piece: when they agree, the
                // panel has the piece from an earlier try.
                throw new HttpResponseException(response()->json(['message' => 'The piece does not start at the end of the part.', 'sizeBytes' => $part->received_bytes], 409));
            }
            if (self::freeBytes() - strlen($piece) < self::reserveBytes()) {
                abort(507, 'The panel does not have enough free disk space for the export. An administrator must make space on the disk of the panel.');
            }
            $file = self::partFile($part);
            if (! is_dir(dirname($file)) && ! mkdir(dirname($file), 0700, true) && ! is_dir(dirname($file))) {
                abort(500, 'The panel could not make the directory of the export.');
            }
            // Offset 0 starts the part again: the job runs again after a restart of the agent.
            $out = fopen($file, $offset === 0 ? 'wb' : 'ab');
            $written = $out === false ? false : fwrite($out, $piece);
            if ($out !== false) {
                fclose($out);
            }
            abort_if($written !== strlen($piece), 507, 'The panel could not write the piece. Its disk is possibly full.');
            $part->update(['received_bytes' => $offset + strlen($piece), 'size_bytes' => null, 'sha256' => null]);

            return $part->received_bytes;
        });
    }

    /**
     * The result of an ExportWebsite job (JobOutcome). A part is complete when the size and
     * the hash in the result agree with the part that the panel has. When the last part is
     * complete, the queue makes the archive.
     *
     * @param  array<string, mixed>  $result
     */
    public static function jobFinished(AgentJob $job, bool $succeeded, ?string $error, array $result): void
    {
        $parts = AccountExportPart::where('agent_job_id', $job->id)->get();
        if ($parts->isEmpty()) {
            return;
        }
        $export = AccountExport::lockForUpdate()->find($parts->first()->account_export_id);
        if (! $export || $export->state !== AccountExport::RUNNING) {
            return;
        }
        if (! $succeeded) {
            self::fail($export, "{$job->title} failed: ".($error ?: 'the server gave no reason.'));

            return;
        }
        foreach ($parts as $part) {
            $sent = $result['parts'][$part->id] ?? null;
            $size = is_array($sent) && is_numeric($sent['sizeBytes'] ?? null) ? (int) $sent['sizeBytes'] : -1;
            $sha = is_array($sent) ? (string) ($sent['sha256'] ?? '') : '';
            $file = self::partFile($part);
            clearstatcache(true, $file);
            if (! preg_match('/^[0-9a-f]{64}$/', $sha) || $size !== $part->received_bytes || ! is_file($file) || filesize($file) !== $size) {
                self::fail($export, "The panel did not get all of {$part->path} from the server.");

                return;
            }
            $part->update(['size_bytes' => $size, 'sha256' => $sha]);
        }
        if (! $export->parts()->whereNull('sha256')->exists()) {
            BuildAccountExport::dispatch($export->id)->afterCommit();
        }
    }

    /**
     * Completes an export when all its parts are complete. The queue runs it
     * (BuildAccountExport). The panel writes the small files of the archive to members.tar:
     * manifest.json, account.json, README.txt and the zone files. It does not copy the parts:
     * a download sends members.tar, then each part with its tar header (archive).
     */
    public static function build(AccountExport $export): void
    {
        $export->refresh();
        if ($export->state !== AccountExport::RUNNING || $export->parts()->whereNull('sha256')->exists()) {
            return;
        }
        $members = self::membersFile($export);
        $partial = $members.'.partial';
        try {
            [$panelFiles, $description] = self::panelFiles($export);
            $parts = $export->parts()->get();
            $finished = now();
            $mtime = $finished->getTimestamp();
            $account = json_encode($description, JSON_PRETTY_PRINT | JSON_UNESCAPED_SLASHES | JSON_UNESCAPED_UNICODE | JSON_THROW_ON_ERROR)."\n";
            $files = ['account.json' => $account, 'README.txt' => self::readme($description), ...$panelFiles];
            $manifest = [
                'format' => self::FORMAT,
                'description' => 'account.json',
                'source' => $description['source'],
                'createdAt' => $finished->utc()->toIso8601String(),
                // The agent of each server calculated the hash of its parts while it sent them.
                'files' => [
                    ...array_map(fn (string $path, string $content) => ['path' => $path, 'bytes' => strlen($content), 'sha256' => hash('sha256', $content)], array_keys($files), $files),
                    ...$parts->map(fn (AccountExportPart $p) => ['path' => $p->path, 'bytes' => $p->size_bytes, 'sha256' => $p->sha256])->all(),
                ],
            ];
            if (! is_dir(dirname($members)) && ! mkdir(dirname($members), 0700, true) && ! is_dir(dirname($members))) {
                throw new \RuntimeException('The panel could not make the directory of the export.');
            }
            $out = fopen($partial, 'wb');
            if ($out === false) {
                throw new \RuntimeException('The panel could not make the archive file.');
            }
            try {
                $tar = new TarWriter($out);
                $tar->addString('manifest.json', json_encode($manifest, JSON_PRETTY_PRINT | JSON_UNESCAPED_SLASHES | JSON_THROW_ON_ERROR)."\n", $mtime);
                foreach ($files as $path => $content) {
                    $tar->addString($path, $content, $mtime);
                }
            } finally {
                fclose($out);
            }
            if (! rename($partial, $members)) {
                throw new \RuntimeException('The panel could not complete the archive file.');
            }
            $export->update(['state' => AccountExport::READY, 'finished_at' => $finished, 'expires_at' => $finished->copy()->addDays(self::days())]);
            $archive = self::archive($export);
            if (! $archive->available()) {
                throw new \RuntimeException('A part of the export is not on the disk.');
            }
            $export->update(['size_bytes' => $archive->size()]);
        } catch (\Throwable $e) {
            Log::error('An account export failed', ['export' => $export->id, 'error' => $e->getMessage()]);
            @unlink($partial);
            self::fail($export, 'The panel could not make the archive: '.$e->getMessage());
        }
    }

    /**
     * The archive of a complete export: members.tar, each part with its tar header, and the end
     * of the archive. The headers have the time of the completion, so that each download gives
     * the same bytes and a download can continue at an offset.
     */
    public static function archive(AccountExport $export): ArchiveStream
    {
        $archive = new ArchiveStream;
        $members = self::membersFile($export);
        clearstatcache(true, $members);
        $archive->file($members, is_file($members) ? (int) filesize($members) : 0);
        $mtime = $export->finished_at?->getTimestamp() ?? 0;
        foreach ($export->parts()->get() as $part) {
            $size = (int) $part->size_bytes;
            $archive->data(TarWriter::header($part->path, $size, $mtime));
            $archive->file(self::partFile($part), $size);
            $archive->data(str_repeat("\0", TarWriter::padding($size)));
        }
        $archive->data(TarWriter::end());

        return $archive;
    }

    /**
     * The files that the panel makes itself (the zone files), and the description with the
     * size of each content.
     *
     * @return array{0: array<string, string>, 1: array<string, mixed>}
     */
    private static function panelFiles(AccountExport $export): array
    {
        $description = $export->description;
        $sizes = $export->parts()->pluck('size_bytes', 'path');
        $files = [];
        $serials = DnsZone::where('customer_id', $export->customer_id)->pluck('serial', 'name');
        foreach ($description['dnsZones'] ?? [] as $i => $zone) {
            $text = ZoneText::render($zone['name'], (int) ($serials[$zone['name']] ?? 1), $zone['records']);
            $files[$zone['content']['path']] = $text;
            $description['dnsZones'][$i]['content']['bytes'] = strlen($text);
        }
        foreach ($description['websites'] ?? [] as $i => $website) {
            if ($path = $website['content']['files']['path'] ?? null) {
                $description['websites'][$i]['content']['files']['bytes'] = (int) ($sizes[$path] ?? 0);
            }
            foreach ($website['databases'] as $j => $database) {
                if ($path = $database['content']['path'] ?? null) {
                    $description['websites'][$i]['databases'][$j]['content']['bytes'] = (int) ($sizes[$path] ?? 0);
                }
            }
            // The panel keeps the description as an array: a map with no entries is a JSON object.
            $description['websites'][$i]['environment'] = (object) $website['environment'];
            $description['websites'][$i]['runtime']['phpIni'] = (object) $website['runtime']['phpIni'];
        }

        return [$files, $description];
    }

    /** A short text for a person who opens the archive. */
    private static function readme(array $description): string
    {
        $name = $description['customer']['name'] ?? 'the account';

        return <<<TEXT
            This archive is an export of the account "{$name}" from a Cloudfactory panel.

            manifest.json   The format of the archive ("cf-export/1") and the SHA-256 hash of each file.
            account.json    The description of the account: the websites, the databases, the DNS
                            zones, the email addresses and the settings ("cf-account/1").
            dns/            A zone file (RFC 1035) for each DNS zone.
            websites/       For each website: files.tar.gz with its files, and databases/ with a
                            dump of each database. A MariaDB dump is SQL text with gzip. A
                            PostgreSQL dump has the custom format of pg_dump: use pg_restore.

            account.json has the environment variables of the websites. Keep the archive safe.

            TEXT;
    }

    /** Marks an export as failed and removes its files. The record stays until it expires. */
    public static function fail(AccountExport $export, string $error): void
    {
        $export->update(['state' => AccountExport::FAILED, 'error' => mb_substr($error, 0, 4000), 'finished_at' => now(), 'expires_at' => now()->addDays(self::days())]);
        self::removeFiles($export);
    }

    /** Removes an export and its files. */
    public static function remove(AccountExport $export): void
    {
        self::removeFiles($export);
        $export->delete();
    }

    /**
     * The panel keeps the newest exports of each customer (exports.keep in the settings). A new
     * export removes the oldest ones that are complete or failed.
     */
    private static function prune(Customer $customer, AccountExport $new): void
    {
        AccountExport::where('customer_id', $customer->id)->where('state', '!=', AccountExport::RUNNING)->whereKeyNot($new->id)
            ->orderByDesc('created_at')->orderByDesc('id')->get()->slice(max(0, self::keep() - 1))
            ->each(fn (AccountExport $e) => self::remove($e));
    }

    /**
     * The scheduled work (cloudfactory:expire-exports): removes the exports that expired, and
     * fails an export that is not complete after a day, for example because the queue lost the
     * job that makes the archive. Gives the number of exports that changed.
     */
    public static function expire(): int
    {
        $count = 0;
        AccountExport::whereNotNull('expires_at')->where('expires_at', '<=', now())->get()->each(function (AccountExport $e) use (&$count) {
            self::remove($e);
            $count++;
        });
        AccountExport::where('state', AccountExport::RUNNING)->where('created_at', '<=', now()->subDay())->get()->each(function (AccountExport $e) use (&$count) {
            // An open job can still report: the panel waits for it.
            $open = AgentJob::whereIn('id', $e->parts()->select('agent_job_id'))->whereIn('state', [JobState::Queued->value, JobState::Running->value])->exists();
            if (! $open) {
                self::fail($e, 'The export did not complete in one day.');
                $count++;
            }
        });

        return $count;
    }

    private static function removeFiles(AccountExport $export): void
    {
        $dir = self::directory($export);
        if (! is_dir($dir)) {
            return;
        }
        $items = new \RecursiveIteratorIterator(new \RecursiveDirectoryIterator($dir, \FilesystemIterator::SKIP_DOTS), \RecursiveIteratorIterator::CHILD_FIRST);
        foreach ($items as $item) {
            $item->isDir() && ! $item->isLink() ? @rmdir($item->getPathname()) : @unlink($item->getPathname());
        }
        @rmdir($dir);
    }

    /** The name of the archive for a download, for example "acme-ltd-export-20261122-2103.tar". */
    public static function downloadName(AccountExport $export): string
    {
        $name = Str::slug((string) $export->customer?->name) ?: 'account';

        return "{$name}-export-".$export->created_at->utc()->format('Ymd-Hi').'.tar';
    }

    /** The directory of all exports. */
    public static function root(): string
    {
        return rtrim((string) config('cloudfactory.exports.path'), '/');
    }

    public static function directory(AccountExport $export): string
    {
        return self::root().'/'.$export->id;
    }

    public static function partFile(AccountExportPart $part): string
    {
        return self::root().'/'.$part->account_export_id.'/parts/'.$part->id;
    }

    /** The small files of the archive, in tar format, with no end of the archive. */
    public static function membersFile(AccountExport $export): string
    {
        return self::directory($export).'/members.tar';
    }

    public static function days(): int
    {
        return max(1, (int) config('cloudfactory.exports.days', 7));
    }

    public static function keep(): int
    {
        return max(1, (int) config('cloudfactory.exports.keep', 3));
    }

    /** The free space that an export must leave on the disk of the panel. */
    public static function reserveBytes(): int
    {
        $total = @disk_total_space(self::existingRoot());

        return max((int) config('cloudfactory.exports.reserve_bytes', 2 * 1024 ** 3), $total ? (int) ($total * 0.05) : 0);
    }

    public static function freeBytes(): int
    {
        $free = @disk_free_space(self::existingRoot());

        return $free === false ? 0 : (int) $free;
    }

    /**
     * Refuses a new export when the disk of the panel has less free space than the reserve.
     *
     * @throws ValidationException
     */
    private static function refuseLowSpace(): void
    {
        if (self::freeBytes() < self::reserveBytes()) {
            throw ValidationException::withMessages(['export' => 'The panel does not have enough free disk space for an export now. Speak to your provider.']);
        }
    }

    /** The directory of the exports, or its nearest parent that exists. */
    private static function existingRoot(): string
    {
        $dir = self::root();
        while (! is_dir($dir) && dirname($dir) !== $dir) {
            $dir = dirname($dir);
        }

        return $dir;
    }
}
