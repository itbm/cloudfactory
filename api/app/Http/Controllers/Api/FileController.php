<?php

namespace App\Http\Controllers\Api;

use App\Http\Controllers\Controller;
use App\Models\Website;
use App\Services\AgentCallException;
use App\Services\AgentClient;
use App\Services\Audit;
use App\Validation\Input;
use Illuminate\Http\JsonResponse;
use Illuminate\Http\Request;
use Illuminate\Validation\Rule;
use Illuminate\Validation\ValidationException;

/**
 * The file manager of a website. The agent does each request as the Unix user of the website,
 * in the directory of the website (agent/internal/sitefiles). The panel keeps no file.
 *
 * A path starts with "/", which is the directory of the website. The container of the website
 * has that directory at /app.
 */
class FileController extends Controller
{
    use Concerns;

    /** The largest file that the file manager writes. The agent has the same limit (sitefiles.MaxWrite). */
    public const MAX_WRITE_BYTES = 5 * 1024 * 1024;

    public function __construct(private AgentClient $agent) {}

    public function index(Request $request, string $websiteId): JsonResponse
    {
        $website = $this->website($request, $websiteId);
        $path = $this->path($request, 'path', '/');
        $list = $this->call($website, 'ListFiles', ['path' => $path]);

        return response()->json([
            'path' => $path,
            'entries' => array_map(fn (array $e) => self::entry($e, $path), $list['entries'] ?? []),
            'truncated' => (bool) ($list['truncated'] ?? false),
        ]);
    }

    /** The content of one file. A file that is not UTF-8 text has the encoding "base64". */
    public function show(Request $request, string $websiteId): JsonResponse
    {
        $website = $this->website($request, $websiteId);
        $path = $this->path($request, 'path');
        $file = $this->call($website, 'ReadFile', ['path' => $path]);
        $bytes = (string) base64_decode((string) ($file['content'] ?? ''), true);
        $text = mb_check_encoding($bytes, 'UTF-8') && ! str_contains($bytes, "\0");

        return response()->json([
            'file' => self::entry(($file['file'] ?? []) + ['name' => basename($path)], dirname($path)),
            'encoding' => $text ? 'utf8' : 'base64',
            'content' => $text ? $bytes : base64_encode($bytes),
        ]);
    }

    public function store(Request $request, string $websiteId): JsonResponse
    {
        $website = $this->website($request, $websiteId, write: true);
        $path = $this->path($request, 'path');
        $data = $request->validate([
            // An empty file has the content "".
            'content' => ['present', 'nullable', 'string'],
            'encoding' => ['sometimes', Rule::in(['utf8', 'base64'])],
            'createOnly' => ['sometimes', 'boolean'],
        ]);
        $content = (string) ($data['content'] ?? '');
        if (($data['encoding'] ?? 'utf8') === 'base64') {
            $content = base64_decode($content, true);
            if ($content === false) {
                throw ValidationException::withMessages(['content' => 'The content is not valid base64 text.']);
            }
        }
        if (strlen($content) > self::MAX_WRITE_BYTES) {
            throw ValidationException::withMessages(['content' => 'The file has more than 5 MB. Use SFTP to upload it.']);
        }
        $entry = $this->call($website, 'WriteFile', ['path' => $path, 'content' => base64_encode($content), 'createOnly' => (bool) ($data['createOnly'] ?? false)]);
        Audit::record($request, 'Saved file', "{$website->name}: {$path}", $website->customer_id);

        return response()->json(['file' => self::entry($entry, dirname($path))]);
    }

    public function makeDirectory(Request $request, string $websiteId): JsonResponse
    {
        $website = $this->website($request, $websiteId, write: true);
        $path = $this->path($request, 'path');
        $entry = $this->call($website, 'MakeDirectory', ['path' => $path]);
        Audit::record($request, 'Made directory', "{$website->name}: {$path}", $website->customer_id);

        return response()->json(['file' => self::entry($entry, dirname($path))], 201);
    }

    public function move(Request $request, string $websiteId): JsonResponse
    {
        $website = $this->website($request, $websiteId, write: true);
        $from = $this->path($request, 'from');
        $to = $this->path($request, 'to');
        $entry = $this->call($website, 'MoveFile', ['from' => $from, 'to' => $to]);
        Audit::record($request, 'Moved file', "{$website->name}: {$from} to {$to}", $website->customer_id);

        return response()->json(['file' => self::entry($entry, dirname($to))]);
    }

    public function destroy(Request $request, string $websiteId): JsonResponse
    {
        $website = $this->website($request, $websiteId, write: true);
        $path = $this->path($request, 'path');
        if ($path === '/') {
            throw ValidationException::withMessages(['path' => 'You cannot delete the directory of the website.']);
        }
        $this->call($website, 'DeleteFile', ['path' => $path]);
        Audit::record($request, 'Deleted file', "{$website->name}: {$path}", $website->customer_id);

        return response()->json(null, 204);
    }

    /**
     * The website of a request. The panel could not make a failed website, so its directory is
     * possibly not on the server. A suspended customer can read the files, but not change them.
     */
    private function website(Request $request, string $websiteId, bool $write = false): Website
    {
        $website = $this->findWebsite($request, $websiteId);
        WebsiteController::refuseFailed($website);
        abort_if($write && ($website->customer?->isSuspended() ?? false), 409, 'This customer is suspended. Lift the suspension before you change files.');

        return $website;
    }

    private function path(Request $request, string $field, ?string $default = null): string
    {
        $data = $request->validate([$field => [$default === null ? 'required' : 'sometimes', 'string', Input::rule('filePath')]]);

        return $data[$field] ?? $default;
    }

    /**
     * @param  array<string, mixed>  $payload
     * @return array<string, mixed>
     */
    private function call(Website $website, string $method, array $payload): array
    {
        try {
            return $this->agent->call($website->server, $method, ['websiteId' => $website->id] + $payload);
        } catch (AgentCallException $e) {
            abort($e->status(), $e->userMessage());
        }
    }

    /**
     * An entry for the API, from the JSON of the agent.
     *
     * @param  array<string, mixed>  $e
     * @return array<string, mixed>
     */
    public static function entry(array $e, string $directory): array
    {
        $name = (string) ($e['name'] ?? '');

        return [
            'name' => $name,
            'path' => rtrim($directory, '/').'/'.$name,
            'type' => (string) ($e['type'] ?? 'file'),
            'sizeBytes' => (int) ($e['sizeBytes'] ?? 0),
            'modifiedAt' => $e['modified'] ?? null,
            'mode' => (string) ($e['mode'] ?? ''),
            'target' => ($e['target'] ?? '') === '' ? null : (string) $e['target'],
        ];
    }
}
