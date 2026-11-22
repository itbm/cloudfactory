<?php

namespace App\Http\Controllers\Api;

use App\Enums\AgentAction;
use App\Enums\JobState;
use App\Http\Controllers\Controller;
use App\Http\Resources\JobResource;
use App\Models\AgentJob;
use App\Models\Customer;
use App\Models\Import;
use App\Models\Server;
use App\Services\Audit;
use App\Services\Imports\Description;
use App\Services\Imports\Planner;
use App\Services\Imports\Runner;
use App\Services\JobDispatcher;
use Illuminate\Http\JsonResponse;
use Illuminate\Http\Request;
use Illuminate\Support\Facades\DB;
use Illuminate\Support\Facades\Storage;
use Illuminate\Validation\Rule;
use Illuminate\Validation\ValidationException;

/**
 * The import of an account from a different hosting panel: cPanel, DirectAdmin, Plesk or
 * HestiaCP (docs/imports.md). Only an administrator imports an account: the import makes
 * websites on a server that the administrator selects.
 *
 * 1. POST /imports with a URL of the backup archive, or with the name and the size of a file.
 *    The browser or the cf command then sends the file in parts (PUT /imports/{id}/archive).
 * 2. The web server downloads the archive and reads it. GET /imports/{id} then has the plan.
 * 3. POST /imports/{id}/start with the parts to import.
 */
class ImportController extends Controller
{
    /** The largest part of an upload. A PHP server accepts it with its usual settings. */
    public const CHUNK_BYTES = 4 * 1024 ** 2;

    public function __construct(private Runner $runner, private JobDispatcher $jobs) {}

    public function index(Request $request): JsonResponse
    {
        $imports = Import::with(['customer', 'server'])->latest()->limit(200)->get();

        return response()->json(['data' => $imports->map(fn (Import $i) => self::present($i))->values()]);
    }

    public function show(string $id): JsonResponse
    {
        $import = Import::with(['customer', 'server'])->findOrFail($id);

        return response()->json(self::present($import, full: true));
    }

    public function store(Request $request): JsonResponse
    {
        $data = $request->validate([
            'customerId' => ['required', 'string', 'exists:customers,id'],
            'serverId' => ['required', 'string', 'exists:servers,id'],
            // An HTTPS URL of the archive, for example a link to object storage. Each server
            // that needs the archive downloads it from there.
            'url' => ['required_without:fileName', 'prohibits:fileName', 'string', 'max:2048', 'url:https'],
            // Or a file that the client sends in parts.
            'fileName' => ['required_without:url', 'string', 'max:255', 'regex:/^[^\/\\\\\x00-\x1f]+$/'],
            'sizeBytes' => ['required_with:fileName', 'integer', 'min:1', 'max:'.Runner::maxBytes()],
        ], [
            'url.url' => 'Enter an HTTPS URL.',
            'sizeBytes.max' => 'The archive is larger than the limit of the panel ('.self::size(Runner::maxBytes()).').',
        ]);
        if (isset($data['url'])) {
            $parts = parse_url($data['url']);
            if (isset($parts['user']) || isset($parts['pass'])) {
                throw ValidationException::withMessages(['url' => 'Enter a URL with no user name and no password.']);
            }
        }
        $customer = Customer::findOrFail($data['customerId']);
        abort_if($customer->isSuspended(), 409, 'This customer is suspended. Lift the suspension before you import an account.');
        $server = Server::findOrFail($data['serverId']);
        if (! in_array('web', $server->roles ?? [], true)) {
            throw ValidationException::withMessages(['serverId' => 'Select a server with the web role.']);
        }
        if (! $server->hasAgentFeature('imports')) {
            throw ValidationException::withMessages(['serverId' => "The agent on the server {$server->name} cannot import an account. Update the agent of the server, then try again."]);
        }
        $url = $data['url'] ?? null;
        if (! $url) {
            // The panel keeps the archive until the import ends.
            $free = @disk_free_space(Storage::disk('local')->path('')) ?: 0;
            $reserve = (int) config('cloudfactory.imports.reserve_bytes', 2 * 1024 ** 3);
            if ($free < $data['sizeBytes'] + $reserve) {
                throw ValidationException::withMessages(['sizeBytes' => 'The panel has '.self::size((int) $free).' of free disk space. The archive needs '.self::size($data['sizeBytes']).' and a reserve of '.self::size($reserve).'. Give a URL of the archive, or make space on the panel.']);
            }
        }
        $import = Import::create([
            'customer_id' => $customer->id,
            'server_id' => $server->id,
            'user_id' => $request->user()->id,
            'source' => $url ? Import::URL : Import::UPLOAD,
            'url' => $url,
            'file_name' => $url ? basename((string) parse_url($url, PHP_URL_PATH)) ?: null : $data['fileName'],
            'size_bytes' => $data['sizeBytes'] ?? 0,
            'status' => $url ? Import::FETCHING : Import::UPLOADING,
        ]);
        $job = $url ? $this->runner->fetch($import) : null;
        Audit::record($request, 'Started account import', $import->file_name ?? 'archive', $customer->id);

        return response()->json(['import' => self::present($import->load(['customer', 'server'])), 'job' => $job ? new JobResource($job) : null], 201);
    }

    /**
     * One part of the archive. "offset" is the number of bytes that the panel has: the client
     * sends the parts in order. After the last part, the web server downloads the archive.
     */
    public function upload(Request $request, string $id): JsonResponse
    {
        $import = Import::findOrFail($id);
        abort_if($import->source !== Import::UPLOAD || $import->status !== Import::UPLOADING, 409, 'This import does not wait for a file.');
        $offset = filter_var($request->query('offset'), FILTER_VALIDATE_INT);
        if ($offset === false || $offset < 0) {
            throw ValidationException::withMessages(['offset' => 'Give the offset of the part.']);
        }
        $result = DB::transaction(function () use ($import, $request, $offset) {
            $import = Import::lockForUpdate()->findOrFail($import->id);
            if ($offset !== $import->received_bytes) {
                return response()->json(['message' => "The panel has {$import->received_bytes} bytes of the file. Send the part from that offset.", 'receivedBytes' => $import->received_bytes], 409);
            }
            $disk = Storage::disk('local');
            $disk->makeDirectory(dirname($import->archivePath()));
            $in = $request->getContent(true);
            $out = fopen($disk->path($import->archivePath()), $offset === 0 ? 'wb' : 'ab');
            $written = stream_copy_to_stream($in, $out, self::CHUNK_BYTES + 1);
            fclose($out);
            if ($written === false || $written === 0 || $written > self::CHUNK_BYTES || $import->received_bytes + $written > $import->size_bytes) {
                // The file goes back to the size before this part.
                $h = fopen($disk->path($import->archivePath()), 'rb+');
                ftruncate($h, $import->received_bytes);
                fclose($h);
                throw ValidationException::withMessages(['archive' => 'Send a part of 1 byte to '.self::size(self::CHUNK_BYTES).', and no more than the size of the file.']);
            }
            $import->update(['received_bytes' => $import->received_bytes + $written]);
            $job = null;
            if ($import->received_bytes === $import->size_bytes) {
                $import->update(['status' => Import::FETCHING]);
                $job = $this->runner->fetch($import);
            }

            return response()->json(['receivedBytes' => $import->received_bytes, 'job' => $job ? new JobResource($job) : null]);
        });

        return $result;
    }

    /** Starts the import of the selected parts. */
    public function start(Request $request, string $id): JsonResponse
    {
        $import = Import::findOrFail($id);
        abort_if($import->status !== Import::READY, 409, 'Start an import when its plan is ready, one time only.');
        $d = $import->description;
        $data = $request->validate([
            'websites' => ['present', 'array', 'max:'.Description::MAX_WEBSITES],
            'websites.*.name' => ['required', 'string', 'distinct', Rule::in(array_column($d['websites'], 'name'))],
            'websites.*.imageId' => ['sometimes', 'nullable', 'string', Rule::in(Planner::phpImages()->pluck('id')->all())],
            'mailDomains' => ['present', 'array'],
            'mailDomains.*' => ['string', 'distinct', Rule::in(array_column($d['mail']['domains'], 'name'))],
            'dnsZones' => ['present', 'array'],
            'dnsZones.*' => ['string', 'distinct', Rule::in(array_column($d['dnsZones'], 'name'))],
            // The import changes the database settings in wp-config.php to the new database.
            'rewriteWordPress' => ['sometimes', 'boolean'],
        ], [
            'websites.*.name.in' => 'The account does not have this website.',
            'websites.*.imageId.in' => 'Select an official PHP image.',
        ]);
        if ($data['websites'] === [] && $data['mailDomains'] === [] && $data['dnsZones'] === []) {
            throw ValidationException::withMessages(['websites' => 'Select one or more parts of the account.']);
        }
        // The import makes the records of each part with the API: the token must have the
        // scope of each part.
        $token = $request->user();
        foreach (['databases' => collect($d['websites'])->whereIn('name', array_column($data['websites'], 'name'))->contains(fn ($w) => $w['databases'] !== []),
            'mail' => $data['mailDomains'] !== [], 'dns' => $data['dnsZones'] !== []] as $area => $needed) {
            abort_if($needed && ! $token->tokenCan("{$area}:write"), 403, "This import needs a token with the scope {$area}:write.");
        }
        $locked = DB::transaction(function () use ($import) {
            $row = Import::lockForUpdate()->findOrFail($import->id);
            if ($row->status !== Import::READY) {
                return false;
            }
            $row->update(['status' => Import::STARTING]);

            return true;
        });
        abort_unless($locked, 409, 'This import started already.');
        $this->runner->start($import->refresh(), $request, [
            'websites' => array_values($data['websites']),
            'mailDomains' => array_values($data['mailDomains']),
            'dnsZones' => array_values($data['dnsZones']),
            'rewriteWordPress' => (bool) ($data['rewriteWordPress'] ?? true),
        ]);
        Audit::record($request, 'Imported account', ($import->account ?? $import->file_name ?? 'archive').' ('.(Description::PANEL_NAMES[$import->panel] ?? $import->panel).')', $import->customer_id);

        return response()->json(self::present($import->refresh()->load(['customer', 'server']), full: true), 202);
    }

    /** Removes the import and its archive from the panel and from each server. */
    public function destroy(Request $request, string $id): JsonResponse
    {
        $import = Import::findOrFail($id);
        abort_if(in_array($import->status, [Import::STARTING, Import::RUNNING], true), 409, 'Wait until the import is complete, then remove it.');
        $jobs = $this->runner->removeArchives($import);
        $import->delete();
        Audit::record($request, 'Removed account import', $import->account ?? $import->file_name ?? 'archive', $import->customer_id);

        return response()->json(['jobs' => JobResource::collection(collect($jobs))->resolve($request)], 202);
    }

    /**
     * The archive of an upload, for an agent that has a download job of the import
     * (/api/agent/v1/imports/{id}/archive).
     */
    public function archive(Request $request, string $id)
    {
        /** @var Server $server */
        $server = $request->attributes->get('server');
        $import = Import::findOrFail($id);
        $wanted = AgentJob::where(['import_id' => $import->id, 'server_id' => $server->id, 'action' => AgentAction::FetchImport->value])
            ->whereIn('state', [JobState::Queued->value, JobState::Running->value])->exists();
        $disk = Storage::disk('local');
        abort_unless($wanted && $import->source === Import::UPLOAD && $disk->exists($import->archivePath()), 404);

        return response()->file($disk->path($import->archivePath()), ['Content-Type' => 'application/octet-stream']);
    }

    /** @return array<string, mixed> */
    public static function present(Import $i, bool $full = false): array
    {
        $d = $i->description;
        $items = collect($i->items ?? []);
        $out = [
            'id' => $i->id,
            'status' => $i->status,
            'source' => $i->source,
            'fileName' => $i->file_name,
            'sizeBytes' => $i->size_bytes,
            'receivedBytes' => $i->received_bytes,
            'panel' => $i->panel,
            'panelName' => $i->panel ? (Description::PANEL_NAMES[$i->panel] ?? $i->panel) : null,
            'account' => $i->account,
            'customerId' => $i->customer_id,
            'customerName' => $i->customer?->name,
            'serverId' => $i->server_id,
            'serverName' => $i->server?->name,
            'error' => $i->error,
            'counts' => $d ? [
                'websites' => count($d['websites']),
                'databases' => array_sum(array_map(fn ($w) => count($w['databases']), $d['websites'])),
                'mailboxes' => array_sum(array_map(fn ($m) => count($m['mailboxes']), $d['mail']['domains'])),
                'dnsZones' => count($d['dnsZones']),
            ] : null,
            'progress' => ['total' => $items->count(), 'done' => $items->where('state', 'done')->count(),
                'failed' => $items->whereIn('state', ['failed', 'skipped'])->count()],
            'createdAt' => $i->created_at?->toIso8601String(),
            'startedAt' => $i->started_at?->toIso8601String(),
            'finishedAt' => $i->finished_at?->toIso8601String(),
        ];
        if ($full) {
            $out['customer'] = $d ? $d['customer'] : null;
            $out['warnings'] = $d ? $d['warnings'] : [];
            $out['plan'] = $d && $i->status === Import::READY ? Planner::plan($i) : null;
            $out['items'] = $items->map(fn ($item) => [
                'id' => $item['id'], 'kind' => $item['kind'], 'name' => $item['name'], 'state' => $item['state'],
                'error' => $item['error'] ?? null, 'detail' => $item['detail'] ?? null, 'jobId' => $item['job'] ?? null,
            ])->values()->all();
        }

        return $out;
    }

    private static function size(int $bytes): string
    {
        return $bytes >= 1024 ** 3 ? round($bytes / 1024 ** 3, 1).' GiB' : round($bytes / 1024 ** 2).' MiB';
    }
}
