<?php

namespace App\Http\Controllers\Api;

use App\Enums\AgentAction;
use App\Http\Controllers\Controller;
use App\Http\Resources\JobResource;
use App\Models\Website;
use App\Services\AgentCallException;
use App\Services\AgentClient;
use App\Services\Audit;
use App\Services\JobDispatcher;
use App\Services\MalwareScans;
use App\Validation\Input;
use App\Validation\InputRules;
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
 *
 * An archive can take a long time, so the agent makes and extracts an archive in a job. The
 * job keeps the summary of the agent (AgentAction::keepsResult).
 */
class FileController extends Controller
{
    use Concerns;

    /** The largest file that the file manager writes. The agent has the same limit (sitefiles.MaxWrite). */
    public const MAX_WRITE_BYTES = 5 * 1024 * 1024;

    /** The names of the archives that the agent makes. It extracts these and ".tar.bz2" and ".tbz2". */
    public const NEW_ARCHIVE = '/\.(zip|tar\.gz|tgz|tar)$/i';

    public const ARCHIVE = '/\.(zip|tar\.gz|tgz|tar|tar\.bz2|tbz2)$/i';

    public function __construct(private AgentClient $agent, private JobDispatcher $jobs) {}

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
        // A file that the last malware scan found is gone.
        MalwareScans::fileRemoved($website, $path);

        return response()->json(null, 204);
    }

    /**
     * Changes the permission bits of a file or a directory. With "recursive", "mode" is the
     * mode of the directory and of each directory in it, and "fileMode" is the mode of each
     * file in it. The agent does not change a link.
     */
    public function permissions(Request $request, string $websiteId): JsonResponse
    {
        $website = $this->website($request, $websiteId, write: true);
        $path = $this->path($request, 'path');
        $data = $request->validate([
            'mode' => ['required', 'string', Input::rule('fileMode')],
            'recursive' => ['sometimes', 'boolean'],
            'fileMode' => ['required_if_accepted:recursive', 'nullable', 'string', Input::rule('fileMode')],
        ]);
        $recursive = (bool) ($data['recursive'] ?? false);
        $mode = self::fourDigits($data['mode']);
        $fileMode = $recursive ? self::fourDigits((string) $data['fileMode']) : '';
        $answer = $this->call($website, 'SetFileMode', ['path' => $path, 'mode' => $mode, 'recursive' => $recursive, 'fileMode' => $fileMode]);
        Audit::record($request, 'Changed permissions', "{$website->name}: {$path} to {$mode}".($recursive ? " (folders), {$fileMode} (files)" : ''), $website->customer_id);

        return response()->json([
            'file' => self::entry(($answer['file'] ?? []) + ['name' => basename($path)], dirname($path)),
            'changed' => (int) ($answer['changed'] ?? 0),
        ]);
    }

    /**
     * Puts files and folders in a new archive. Each path goes in the archive with its name, at
     * the top level. The agent does not replace a file.
     */
    public function archive(Request $request, string $websiteId): JsonResponse
    {
        $website = $this->website($request, $websiteId, write: true);
        $data = $request->validate([
            'paths' => ['required', 'array', 'min:1', 'max:'.InputRules::MAX_ARCHIVE_PATHS],
            'paths.*' => ['required', 'string', 'distinct', Input::rule('filePath')],
            'path' => ['required', 'string', Input::rule('filePath'), 'regex:'.self::NEW_ARCHIVE],
        ], ['path.regex' => 'The name of the archive must end with .zip, .tar.gz, .tgz or .tar.']);
        $paths = array_values($data['paths']);
        $names = array_map('basename', $paths);
        if (count($paths) > 1 && in_array('/', $paths, true)) {
            throw ValidationException::withMessages(['paths' => 'The directory of the website has all other files. Select it alone.']);
        }
        if (count(array_unique($names)) !== count($names)) {
            throw ValidationException::withMessages(['paths' => 'Two of the selected paths have the same name. Select paths in one folder.']);
        }
        $job = $this->jobs->dispatch($website->server, AgentAction::MakeArchive, [
            'websiteId' => $website->id,
            'paths' => $paths,
            'path' => $data['path'],
        ], 'Make archive', $website, $data['path']);
        Audit::record($request, 'Made archive', "{$website->name}: {$data['path']}", $website->customer_id);

        return response()->json(['job' => new JobResource($job)], 202);
    }

    /**
     * Extracts an archive into a folder. The default folder is the folder of the archive. The
     * agent makes the folder when it does not exist. A file that exists stays, unless
     * "overwrite" is true.
     */
    public function extract(Request $request, string $websiteId): JsonResponse
    {
        $website = $this->website($request, $websiteId, write: true);
        $data = $request->validate([
            'path' => ['required', 'string', Input::rule('filePath'), 'regex:'.self::ARCHIVE],
            'to' => ['sometimes', 'string', Input::rule('filePath')],
            'overwrite' => ['sometimes', 'boolean'],
        ], ['path.regex' => 'The file manager extracts .zip, .tar, .tar.gz, .tgz, .tar.bz2 and .tbz2 archives.']);
        $to = $data['to'] ?? dirname($data['path']);
        $overwrite = (bool) ($data['overwrite'] ?? false);
        $job = $this->jobs->dispatch($website->server, AgentAction::ExtractArchive, [
            'websiteId' => $website->id,
            'path' => $data['path'],
            'to' => $to,
            'overwrite' => $overwrite,
        ], 'Extract archive', $website, $data['path']);
        Audit::record($request, 'Extracted archive', "{$website->name}: {$data['path']} to {$to}".($overwrite ? ' (replace files)' : ''), $website->customer_id);

        return response()->json(['job' => new JobResource($job)], 202);
    }

    /** A mode with 4 digits, for example "0644" for "644". */
    private static function fourDigits(string $mode): string
    {
        return str_pad($mode, 4, '0', STR_PAD_LEFT);
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
