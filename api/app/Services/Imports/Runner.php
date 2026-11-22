<?php

namespace App\Services\Imports;

use App\Enums\AgentAction;
use App\Enums\JobState;
use App\Http\Controllers\Api\CronJobController;
use App\Http\Controllers\Api\DatabaseController;
use App\Http\Controllers\Api\DnsZoneController;
use App\Http\Controllers\Api\DomainController;
use App\Http\Controllers\Api\MailAliasController;
use App\Http\Controllers\Api\MailboxController;
use App\Http\Controllers\Api\MailDomainController;
use App\Http\Controllers\Api\WebsiteController;
use App\Models\AgentJob;
use App\Models\DatabaseServer;
use App\Models\DnsZone;
use App\Models\Import;
use App\Models\Mailbox;
use App\Models\MailDomain;
use App\Models\Server;
use App\Models\Website;
use App\Services\Dns\Import as DnsImport;
use App\Services\Dns\ZoneFile;
use App\Services\Dns\Zones;
use App\Services\JobDispatcher;
use Illuminate\Database\Eloquent\ModelNotFoundException;
use Illuminate\Http\JsonResponse;
use Illuminate\Http\Request;
use Illuminate\Support\Facades\DB;
use Illuminate\Support\Str;
use Illuminate\Validation\ValidationException;
use RuntimeException;
use Symfony\Component\HttpKernel\Exception\HttpExceptionInterface;
use Throwable;

/**
 * Runs an import (docs/imports.md).
 *
 * 1. fetch() sends FetchImport to the web server of the import. The agent downloads the
 *    archive and gives the description of the account. The import is then "ready".
 * 2. start() makes the records that the user selected, with the controllers of the API: the
 *    websites, their domains, databases and cron jobs, the mail domains, mailboxes and
 *    addresses, and the DNS records. Thus the import has the same checks, plan limits, hooks
 *    and audit entries as the API. Each job of these records has the import.
 * 3. The content parts (the files, the dumps and the messages) wait for the jobs of their
 *    records. When a job of the import ends, jobFinished() sends the content jobs that can
 *    start. A server that needs the archive and does not have it downloads it first, with the
 *    hash of the first download.
 *
 * Each part of the import is an item with a state: "waiting", "running", "done", "failed" or
 * "skipped" (a part before it failed). The import is "done" when no item waits or runs.
 */
final class Runner
{
    /** The service names of cPanel in a zone. Their records point to the old server. */
    private const PANEL_NAMES = ['cpanel', 'whm', 'webmail', 'webdisk', 'cpcalendars', 'cpcontacts', 'cpanel-dcv-test-record', '_cpanel-dcv-test-record'];

    /** The size of a new mailbox when the old panel had no limit: 1 GiB, or more for the messages. */
    private const DEFAULT_QUOTA = 1024 ** 3;

    /** @var list<array<string, mixed>> */
    private array $items = [];

    private int $next = 0;

    public function __construct(private JobDispatcher $jobs) {}

    /** The largest archive. The default is 50 GiB. */
    public static function maxBytes(): int
    {
        return (int) config('cloudfactory.imports.max_bytes', 50 * 1024 ** 3);
    }

    /** Sends the first download, with the inspection, to the web server of the import. */
    public function fetch(Import $import): AgentJob
    {
        return JobDispatcher::forImport($import->id, fn () => $this->jobs->dispatch($import->server, AgentAction::FetchImport, [
            'importId' => $import->id,
            'url' => $import->source === Import::URL ? $import->url : '',
            'maxBytes' => self::maxBytes(),
            'inspect' => true,
        ], 'Read the archive of the import', customerId: $import->customer_id));
    }

    /**
     * Makes the records of the selected parts, and starts the content jobs.
     *
     * @param  array{websites: list<array{name: string, imageId?: ?string}>, mailDomains: list<string>, dnsZones: list<string>, rewriteWordPress: bool}  $selection
     */
    public function start(Import $import, Request $request, array $selection): void
    {
        $d = $import->description;
        // The jobs of the new records can end while the import makes the next records. The
        // import continues only when all records are there (status "running").
        $import->update(['status' => Import::STARTING, 'started_at' => now(), 'items' => []]);
        $this->items = [];
        try {
            JobDispatcher::forImport($import->id, function () use ($import, $request, $d, $selection) {
                $chosen = collect($selection['websites'])->keyBy('name');
                foreach ($d['websites'] as $w) {
                    if ($chosen->has($w['name'])) {
                        $this->website($import, $request, $w, $chosen[$w['name']]['imageId'] ?? null, $selection['rewriteWordPress']);
                    }
                }
                foreach ($d['mail']['domains'] as $m) {
                    if (in_array($m['name'], $selection['mailDomains'], true)) {
                        $this->mailDomain($import, $request, $m);
                    }
                }
                foreach ($d['dnsZones'] as $z) {
                    if (in_array($z['name'], $selection['dnsZones'], true)) {
                        $this->zone($import, $request, $z);
                    }
                }
            });
        } finally {
            DB::transaction(function () use ($import) {
                $locked = Import::lockForUpdate()->findOrFail($import->id);
                $locked->status = Import::RUNNING;
                JobDispatcher::forImport($locked->id, fn () => $this->advance($locked, $this->items));
            });
        }
    }

    /** Continues an import when one of its jobs ends (JobOutcome::finish). */
    public function jobFinished(AgentJob $job, bool $succeeded, array $result): void
    {
        $import = Import::lockForUpdate()->find($job->import_id);
        if (! $import) {
            return;
        }
        if ($job->action === AgentAction::FetchImport && $import->status === Import::FETCHING) {
            $this->inspected($import, $job, $succeeded, $result);

            return;
        }
        $items = $import->items ?? [];
        if ($job->action === AgentAction::FetchImport) {
            $servers = $import->servers ?? [];
            $servers[$job->server_id] = $succeeded ? 'ready' : 'failed';
            $import->servers = $servers;
        }
        foreach ($items as &$item) {
            if (($item['job'] ?? null) === $job->id && $item['state'] === 'running') {
                $item['state'] = $succeeded ? 'done' : 'failed';
                $item['error'] = $succeeded ? null : $job->error;
                $item['detail'] = $succeeded ? (self::detail($job->action, $result) ?? $item['detail'] ?? null) : null;
            }
        }
        unset($item);
        if ($import->status === Import::RUNNING) {
            JobDispatcher::forImport($import->id, fn () => $this->advance($import, $items));
        } else {
            $import->update(['items' => $items, 'servers' => $import->servers]);
        }
    }

    /** The result of the first download: the description of the account. */
    private function inspected(Import $import, AgentJob $job, bool $succeeded, array $result): void
    {
        if (! $succeeded) {
            $this->fail($import, (string) $job->error);

            return;
        }
        try {
            $description = Description::normalise($result['account'] ?? null);
        } catch (\InvalidArgumentException $e) {
            $this->fail($import, $e->getMessage());

            return;
        }
        $import->update([
            'status' => Import::READY,
            'description' => $description,
            'panel' => $description['source']['panel'],
            'account' => $description['source']['account'] ?: null,
            'sha256' => is_string($result['sha256'] ?? null) && preg_match('/^[0-9a-f]{64}$/', $result['sha256']) ? $result['sha256'] : null,
            'size_bytes' => is_int($result['bytes'] ?? null) ? $result['bytes'] : $import->size_bytes,
            'servers' => [$import->server_id => 'ready'],
            'error' => null,
        ]);
    }

    /** The website, its domains, databases and cron jobs, and the part of its files. */
    private function website(Import $import, Request $request, array $w, ?string $imageId, bool $rewrite): void
    {
        $imageId ??= Planner::imageFor(Planner::phpImages(), $w['runtime']['phpVersion'])?->id;
        $made = $this->record('website', $w['name'], fn () => $this->call($request, WebsiteController::class, 'store', [
            'kind' => 'public', 'name' => $w['name'], 'imageId' => $imageId, 'customerId' => $import->customer_id,
            'serverId' => $import->server_id, 'documentRoot' => $w['runtime']['documentRoot'],
        ]));
        if (! $made) {
            $this->skipParts($w, 'The import could not make the website.');

            return;
        }
        $website = Website::findOrFail($made['website']['id']);
        $websiteJob = $made['job']['id'];
        foreach ($w['domains'] as $domain) {
            if ($domain['kind'] !== 'primary' && $domain['name'] !== $w['name']) {
                $this->record('domain', $domain['name'], fn () => $this->call($request, DomainController::class, 'store',
                    ['name' => $domain['name'], 'kind' => $domain['kind']], ['websiteId' => $website->id]));
            }
        }

        // Each database gets a user with the same name and a new password. The other users of
        // the old panel have passwords that the import does not know.
        $account = $import->account ?? '';
        $config = null;
        foreach ($w['databases'] as $db) {
            $suffix = self::databaseSuffix($db['name'], $account);
            $made = $this->record('database', $db['name'], fn () => $this->call($request, DatabaseController::class, 'store',
                ['engine' => 'mariadb', 'name' => $suffix, 'createUser' => true], ['websiteId' => $website->id]));
            if (! $made) {
                $this->skip('dump', $db['name'], 'The import could not make the database.');

                continue;
            }
            $this->rename($made['name']);
            if ($rewrite && ($w['appConfig']['database'] ?? null) === $db['name']) {
                $port = (int) ($made['port'] ?? 3306);
                $config = ['file' => $w['appConfig']['file'], 'values' => [
                    'DB_NAME' => $made['name'], 'DB_USER' => $made['user'], 'DB_PASSWORD' => $made['password'],
                    'DB_HOST' => $port === 3306 ? $made['host'] : "{$made['host']}:{$port}",
                ]];
            }
            if ($db['content'] !== null) {
                $server = DatabaseServer::findOrFail($made['databaseServerId']);
                $this->content('dump', $made['name'], $server->server_id, AgentAction::ImportDatabase, [
                    'websiteId' => $website->id, 'unixUser' => $website->unix_user, 'engine' => 'mariadb', 'name' => $made['name'],
                    'path' => $db['content']['path'], 'databaseServer' => $server->id, 'customer' => $website->customer_id,
                ], [$websiteJob, $made['job']['id']], "Import database {$made['name']}");
            }
        }
        if (count($w['databaseUsers']) > count($w['databases'])) {
            $this->note('databaseUsers', $w['name'], 'Each database has one user with a new password. Make the other database users of the old panel again.');
        }
        foreach ($w['cronJobs'] as $cron) {
            $this->record('cronJob', $cron['schedule'].' '.Str::limit($cron['command'], 60), fn () => $this->call($request, CronJobController::class, 'store',
                ['schedule' => $cron['schedule'], 'command' => $cron['command'], 'enabled' => false], ['websiteId' => $website->id])
                + ['detail' => 'Turned off. Change the paths of the old panel, then turn it on.']);
        }
        if (($files = $w['content']['files']) !== null) {
            $this->content('files', $w['name'], $import->server_id, AgentAction::ImportFiles, [
                'websiteId' => $website->id, 'unixUser' => $website->unix_user, 'path' => $files['path'], 'exclude' => $files['exclude'],
                ...($config ? ['wordpressConfig' => $config['file'], 'wordpressValues' => $config['values']] : []),
            ], [$websiteJob], "Import files of {$website->name}");
        }
    }

    /** The mail domain, its mailboxes and addresses, and the part of the messages. */
    private function mailDomain(Import $import, Request $request, array $m): void
    {
        $made = $this->record('mailDomain', $m['name'], fn () => $this->call($request, MailDomainController::class, 'store', [
            'name' => $m['name'], 'customerId' => $import->customer_id,
        ]));
        if (! $made) {
            return;
        }
        // The panel adds a mailbox or an address when the mail server has the domain: these
        // records wait for the job of the domain.
        $domainId = $made['mailDomain']['id'];
        $domainJob = $made['job']['id'];
        $boxes = [];
        foreach ($m['mailboxes'] as $b) {
            $address = $b['localPart'].'@'.$m['name'];
            $id = $this->later('mailbox', $address, [$domainJob], MailboxController::class,
                ['domainId' => $domainId, 'localPart' => $b['localPart'], 'quotaBytes' => self::quota($b)],
                attributes: $b['passwordHash'] !== '' ? [MailboxController::IMPORT_HASH => $b['passwordHash']] : [],
                note: $b['passwordHash'] === '' ? 'The panel does not know the old password. Make a new password for the mailbox on the Email page, and give it to its owner.' : null);
            if ($b['content'] !== null) {
                $boxes[] = ['item' => $id, 'domain' => $m['name'], 'localPart' => $b['localPart'], 'path' => $b['content']['path']];
            }
        }
        foreach ($m['aliases'] as $a) {
            $this->later('mailAlias', $a['localPart'].'@'.$m['name'], [$domainJob], MailAliasController::class,
                ['domainId' => $domainId, 'localPart' => $a['localPart'], 'kind' => $a['kind'], 'destinations' => $a['destinations']]);
        }
        if ($boxes !== []) {
            $this->content('messages', $m['name'], MailDomain::findOrFail($domainId)->server_id, AgentAction::ImportMail,
                ['mailboxes' => $boxes], [$domainJob], "Import messages of {$m['name']}", array_column($boxes, 'item'));
        }
    }

    /** The DNS zone: a new zone, or a zone of the customer that gets the records it does not have. */
    private function zone(Import $import, Request $request, array $z): void
    {
        $this->record('dnsZone', $z['name'], function () use ($import, $request, $z) {
            $zone = DnsZone::where(['name' => $z['name'], 'customer_id' => $import->customer_id])->first();
            if (! $zone) {
                $made = $this->call($request, DnsZoneController::class, 'store', ['name' => $z['name'], 'customerId' => $import->customer_id]);
                $zone = DnsZone::findOrFail($made['zone']['id']);
            }
            $parsed = ZoneFile::parse($zone->name, $z['zoneFile']);
            $records = array_values(array_filter($parsed['records'], fn ($r) => ! in_array(strtolower(explode('.', $r['name'])[0]), self::PANEL_NAMES, true)));
            $result = DB::transaction(function () use ($zone, $records) {
                DnsZone::whereKey($zone->id)->lockForUpdate()->first();

                return DnsImport::apply($zone, $records);
            });
            $count = count($result['added']);
            if ($count > 0) {
                app(Zones::class)->changed($zone, "Import {$count} DNS ".($count === 1 ? 'record' : 'records')." to {$zone->name}");
            }

            return ['detail' => "{$count} ".($count === 1 ? 'record' : 'records').' added'];
        });
    }

    /**
     * Makes one record and adds an item for it. The item ends when the job of the record ends.
     * A record with no job is done at once. A refusal of the panel fails the item.
     *
     * @param  callable(): array<string, mixed>  $make
     * @return array<string, mixed>|null the answer of the panel, or null when it failed.
     */
    private function record(string $kind, string $name, callable $make): ?array
    {
        try {
            $answer = $make();
        } catch (Throwable $e) {
            $this->items[] = $this->item($kind, $name, 'failed', error: self::reason($e));

            return null;
        }
        $job = $answer['job']['id'] ?? null;
        $this->items[] = $this->item($kind, $name, $job ? 'running' : 'done', job: $job, detail: $answer['detail'] ?? null);

        return $answer;
    }

    /**
     * Adds a content item that waits for jobs, and for the records of other items. A content
     * part of mailboxes starts with the mailboxes that the panel made.
     *
     * @param  list<string>  $waitForItems
     */
    private function content(string $kind, string $name, string $serverId, AgentAction $action, array $payload, array $waitFor, string $title, array $waitForItems = []): void
    {
        $this->items[] = $this->item($kind, $name, 'waiting') + [
            'serverId' => $serverId, 'action' => $action->value, 'payload' => $payload, 'waitFor' => array_values(array_filter($waitFor)),
            'waitForItems' => $waitForItems, 'title' => $title,
        ];
    }

    /**
     * Adds an item of a record that the panel makes when the jobs end (advance). The item
     * keeps the request until then, encrypted with the other items.
     *
     * @return string the ID of the item.
     */
    private function later(string $kind, string $name, array $waitFor, string $controller, array $data, array $attributes = [], ?string $note = null): string
    {
        $item = $this->item($kind, $name, 'waiting') + ['waitFor' => array_values(array_filter($waitFor)), 'call' => compact('controller', 'data', 'attributes', 'note')];
        $this->items[] = $item;

        return $item['id'];
    }

    private function skip(string $kind, string $name, string $reason): void
    {
        $this->items[] = $this->item($kind, $name, 'skipped', error: $reason);
    }

    /** A note: a part that the import made with a change, for example a new password. */
    private function note(string $kind, string $name, string $message): void
    {
        $this->items[] = $this->item($kind, $name, 'done', detail: $message);
    }

    /** The items of the parts of a website that the import could not make. */
    private function skipParts(array $w, string $reason): void
    {
        foreach ($w['databases'] as $db) {
            $this->skip('database', $db['name'], $reason);
        }
        if ($w['content']['files'] !== null) {
            $this->skip('files', $w['name'], $reason);
        }
    }

    /** The last database item gets the new name of the database. */
    private function rename(string $name): void
    {
        $i = array_key_last($this->items);
        $this->items[$i]['detail'] = "New name: {$name}";
    }

    /** @return array<string, mixed> */
    private function item(string $kind, string $name, string $state, ?string $job = null, ?string $error = null, ?string $detail = null): array
    {
        return ['id' => 'i'.(++$this->next), 'kind' => $kind, 'name' => $name, 'state' => $state, 'job' => $job, 'error' => $error, 'detail' => $detail];
    }

    /**
     * Sends the content jobs whose records are complete, and ends the import when no item waits
     * or runs. The caller holds the lock on the import.
     *
     * @param  list<array<string, mixed>>  $items
     */
    private function advance(Import $import, array $items): void
    {
        $servers = $import->servers ?? [];
        $ids = collect($items)->flatMap(fn ($i) => [...($i['waitFor'] ?? []), $i['job'] ?? null])->filter()->unique()->values();
        $jobs = AgentJob::whereIn('id', $ids)->get(['id', 'state', 'error'])->keyBy('id');
        $ended = fn (?string $id) => $id !== null && in_array($jobs[$id]->state ?? null, [JobState::Succeeded, JobState::Failed], true);
        $failed = fn (?string $id) => $id !== null && ($jobs[$id]->state ?? null) === JobState::Failed;
        $index = array_flip(array_column($items, 'id'));
        $stateOf = function (string $id) use (&$items, $index): ?string {
            return isset($index[$id]) ? $items[$index[$id]]['state'] : null;
        };
        foreach ($items as &$item) {
            // A job of a record that ended while the import made the other records.
            if ($item['state'] === 'running' && $ended($item['job'])) {
                $item['state'] = $failed($item['job']) ? 'failed' : 'done';
                $item['error'] = $failed($item['job']) ? $jobs[$item['job']]->error : null;
            }
            if ($item['state'] !== 'waiting') {
                continue;
            }
            $waits = $item['waitFor'] ?? [];
            $records = array_map($stateOf, $item['waitForItems'] ?? []);
            if (collect($waits)->contains(fn ($id) => ! $ended($id)) || array_intersect($records, ['waiting', 'running']) !== []) {
                continue;
            }
            if (! isset($item['call'])) {
                $server = Server::find($item['serverId']);
                $archive = $server ? ($servers[$server->id] ?? null) : null;
                if (! $server || $archive === 'failed') {
                    $item['state'] = 'failed';
                    $item['error'] = $server ? "The server {$server->name} could not download the archive." : 'The server of this part is not on the panel.';
                    unset($item['payload']);

                    continue;
                }
            }
            if (collect($waits)->contains($failed)) {
                $item['state'] = 'skipped';
                $item['error'] = 'A part before this one failed, so the import did not copy it.';
                unset($item['payload'], $item['call']);

                continue;
            }
            if (isset($item['call'])) {
                $this->callLater($import, $item);

                continue;
            }
            // Only the mailboxes that the panel made get their messages.
            if (isset($item['payload']['mailboxes'])) {
                $item['payload']['mailboxes'] = array_values(array_map(fn ($b) => array_diff_key($b, ['item' => true]),
                    array_filter($item['payload']['mailboxes'], fn ($b) => $stateOf($b['item'] ?? '') === 'done' || ! isset($b['item']))));
                if ($item['payload']['mailboxes'] === []) {
                    $item['state'] = 'skipped';
                    $item['error'] = 'The import could not make the mailboxes of this domain.';
                    unset($item['payload']);

                    continue;
                }
            }
            if ($archive !== 'ready') {
                if (! is_array($archive)) {
                    $fetch = $this->jobs->dispatch($server, AgentAction::FetchImport, [
                        'importId' => $import->id,
                        'url' => $import->source === Import::URL ? $import->url : '',
                        'sha256' => $import->sha256,
                        'maxBytes' => self::maxBytes(),
                        'inspect' => false,
                    ], "Download the archive of the import to {$server->name}", customerId: $import->customer_id);
                    $archive = $servers[$server->id] = ['job' => $fetch->id];
                }
                $item['waitFor'] = [$archive['job']];

                continue;
            }
            $job = $this->jobs->dispatch($server, AgentAction::from($item['action']), ['importId' => $import->id, ...$item['payload']],
                $item['title'], customerId: $import->customer_id);
            $item['state'] = 'running';
            $item['job'] = $job->id;
            // The job has the payload now, with the database password. The import does not keep it.
            unset($item['payload']);
        }
        unset($item);
        $open = collect($items)->contains(fn ($i) => in_array($i['state'], ['waiting', 'running'], true));
        $import->update([
            'status' => $open ? Import::RUNNING : Import::DONE,
            'items' => $items,
            'servers' => $servers,
            ...($open ? [] : ['finished_at' => now()]),
        ]);
        if (! $open) {
            $this->removeArchives($import);
        }
    }

    /** Ends an import that cannot continue, and removes its archives. */
    public function fail(Import $import, string $error): void
    {
        $import->update(['status' => Import::FAILED, 'error' => $error, 'finished_at' => now()]);
        $this->removeArchives($import);
    }

    /**
     * Removes the archive from the panel and from each server that downloaded it, or started to.
     *
     * @return list<AgentJob>
     */
    public function removeArchives(Import $import): array
    {
        $jobs = [];
        foreach (array_keys($import->servers ?? [$import->server_id => true]) as $serverId) {
            if ($server = Server::find($serverId)) {
                $jobs[] = JobDispatcher::forImport($import->id, fn () => $this->jobs->dispatch($server, AgentAction::DeleteImport, ['importId' => $import->id],
                    "Remove the archive of an import from {$server->name}", customerId: $import->customer_id));
            }
        }
        $import->removeArchive();
        $import->update(['servers' => []]);

        return $jobs;
    }

    /**
     * Makes the record of an item that waited for jobs, as the user who started the import.
     *
     * @param  array<string, mixed>  $item
     */
    private function callLater(Import $import, array &$item): void
    {
        $call = $item['call'];
        unset($item['call']);
        $user = $import->user;
        if (! $user) {
            [$item['state'], $item['error']] = ['failed', 'The user who started the import is not on the panel.'];

            return;
        }
        $request = Request::create("/api/v1/imports/{$import->id}/start", 'POST');
        $request->setUserResolver(fn () => $user);
        try {
            $answer = $this->call($request, $call['controller'], 'store', $call['data'], attributes: $call['attributes']);
        } catch (Throwable $e) {
            [$item['state'], $item['error']] = ['failed', self::reason($e)];

            return;
        }
        $item['job'] = $answer['job']['id'] ?? null;
        $item['state'] = $item['job'] ? 'running' : 'done';
        $item['detail'] = $call['note'];
    }

    /**
     * Calls a controller of the API as the user, with the checks of the API.
     *
     * @return array<string, mixed>
     */
    private function call(Request $original, string $controller, string $method, array $data, array $route = [], array $attributes = []): array
    {
        // A JSON body, as from a client of the API.
        $request = Request::create($original->getPathInfo(), 'POST', server: ['CONTENT_TYPE' => 'application/json'] + $original->server->all(), content: json_encode($data, JSON_THROW_ON_ERROR));
        $request->setUserResolver($original->getUserResolver());
        $request->headers->set('Accept', 'application/json');
        foreach ($attributes as $key => $value) {
            $request->attributes->set($key, $value);
        }
        $response = app()->call([app($controller), $method], ['request' => $request, ...$route]);
        if (! $response instanceof JsonResponse) {
            throw new RuntimeException('The panel gave no answer.');
        }
        $body = $response->getData(true);
        if ($response->getStatusCode() >= 400) {
            throw new RuntimeException(is_string($body['message'] ?? null) ? $body['message'] : 'The panel refused this part.');
        }

        return $body;
    }

    /**
     * The size of a new mailbox: the size on the old panel. With no limit there, 1 GiB, or more
     * for the messages.
     *
     * @param  array<string, mixed>  $b  a mailbox of the description.
     */
    public static function quota(array $b): int
    {
        $quota = $b['quotaBytes'] > 0 ? $b['quotaBytes'] : max(self::DEFAULT_QUOTA, (int) (($b['content']['bytes'] ?? 0) * 1.25));

        return min(max($quota, Mailbox::MIN_QUOTA), Mailbox::MAX_QUOTA);
    }

    /** A short reason for a refused record. */
    private static function reason(Throwable $e): string
    {
        return match (true) {
            $e instanceof ValidationException => (string) (collect($e->errors())->flatten()->first() ?? $e->getMessage()),
            $e instanceof HttpExceptionInterface, $e instanceof RuntimeException => $e->getMessage(),
            $e instanceof ModelNotFoundException => 'The panel does not have a part that this part needs.',
            default => tap('The panel could not make this part.', fn () => report($e)),
        };
    }

    /**
     * The name of a database without the account of the old panel: "alice_wp" becomes "wp".
     * The panel then puts the Unix user of the website before it.
     */
    public static function databaseSuffix(string $name, string $account): string
    {
        $suffix = $account !== '' && str_starts_with($name, $account.'_') ? substr($name, strlen($account) + 1) : $name;
        $suffix = strtolower(preg_replace('/[^A-Za-z0-9_]/', '_', $suffix) ?? '');
        $suffix = ltrim($suffix, '_');

        return substr($suffix !== '' ? $suffix : 'db', 0, 32);
    }

    /** A short summary of the result of a content job. */
    private static function detail(AgentAction $action, array $result): ?string
    {
        return match ($action) {
            AgentAction::ImportFiles => sprintf('%d files copied%s', (int) ($result['files'] ?? 0),
                ($result['rewritten'] ?? []) !== [] ? ', database settings changed in '.implode(', ', (array) $result['rewritten']) : ''),
            AgentAction::ImportMail => sprintf('%d messages copied%s', (int) ($result['messages'] ?? 0),
                ($failed = array_sum(array_map('intval', (array) ($result['failed'] ?? [])))) > 0 ? ", {$failed} refused by the mail server" : ''),
            AgentAction::ImportDatabase => 'Dump loaded',
            default => null,
        };
    }
}
