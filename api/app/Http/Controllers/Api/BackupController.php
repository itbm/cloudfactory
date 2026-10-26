<?php

namespace App\Http\Controllers\Api;

use App\Enums\AgentAction;
use App\Http\Controllers\Controller;
use App\Http\Resources\JobResource;
use App\Models\Setting;
use App\Services\Audit;
use App\Services\JobDispatcher;
use Illuminate\Http\JsonResponse;
use Illuminate\Http\Request;
use Illuminate\Validation\Rule;

/** FR-901 to FR-903: backups and restore. */
class BackupController extends Controller
{
    use Concerns;

    public function __construct(private JobDispatcher $jobs) {}

    public function index(Request $request, string $websiteId): JsonResponse
    {
        $website = $this->findWebsite($request, $websiteId);

        return response()->json(['data' => $website->snapshots()->limit(100)->get()->map(fn ($s) => [
            'id' => $s->id,
            'kind' => $s->kind,
            'sizeBytes' => $s->size_bytes,
            'databases' => $s->databases,
            'mailboxes' => $s->mailboxes,
            'createdAt' => $s->created_at?->toIso8601String(),
        ])]);
    }

    public function store(Request $request, string $websiteId): JsonResponse
    {
        $website = $this->findWebsite($request, $websiteId);
        $job = $this->jobs->dispatch($website->server, AgentAction::Backup, [
            'websiteId' => $website->id,
            'unixUser' => $website->unix_user,
            'kind' => 'manual',
            'databases' => $website->databases()->get(['engine', 'name'])->toArray(),
            'target' => self::policy()['target'],
        ], "Back up {$website->name}", $website);
        Audit::record($request, 'Backed up website', $website->name, $website->customer_id);

        return response()->json(['job' => new JobResource($job)], 202);
    }

    /** FR-903: restore a full website, one database, one mailbox or one file. */
    public function restore(Request $request, string $websiteId, string $snapshotId): JsonResponse
    {
        $website = $this->findWebsite($request, $websiteId);
        $snapshot = $website->snapshots()->findOrFail($snapshotId);
        $data = $request->validate([
            'scope' => ['required', Rule::in(['website', 'database', 'mailbox', 'file'])],
            'item' => ['required_unless:scope,website', 'nullable', 'string', 'max:1024'],
        ]);
        if ($data['scope'] === 'database') {
            $request->validate(['item' => [Rule::in($snapshot->databases)]]);
        }
        if ($data['scope'] === 'file') {
            $request->validate(['item' => ['regex:#^/[^\0]*$#', 'not_regex:#(^|/)\.\.(/|$)#']]);
        }
        $what = $data['scope'] === 'website' ? 'full website' : $data['item'];
        $job = $this->jobs->dispatch($website->server, AgentAction::Restore, [
            'websiteId' => $website->id,
            'unixUser' => $website->unix_user,
            'snapshotId' => $snapshot->restic_id ?? $snapshot->id,
            'scope' => $data['scope'],
            'item' => $data['item'] ?? null,
        ], "Restore {$what}", $website);
        Audit::record($request, 'Restored from backup', "{$website->name} {$what}", $website->customer_id);

        return response()->json(['job' => new JobResource($job)], 202);
    }

    public function showPolicy(): JsonResponse
    {
        return response()->json(self::policy());
    }

    /** FR-902: the administrator sets the retention policy. */
    public function updatePolicy(Request $request): JsonResponse
    {
        $data = $request->validate([
            'daily' => ['required', 'integer', 'between:0,365'],
            'weekly' => ['required', 'integer', 'between:0,520'],
            'monthly' => ['required', 'integer', 'between:0,240'],
            'time' => ['required', 'date_format:H:i'],
            'target' => ['required', 'regex:#^(s3|b2|azure|gs)://\S+$#'],
        ]);
        Setting::put('backup_policy', $data);
        Audit::record($request, 'Changed backup policy', "{$data['daily']} daily, {$data['weekly']} weekly, {$data['monthly']} monthly");

        return response()->json($data);
    }

    public static function policy(): array
    {
        return Setting::get('backup_policy', config('cloudfactory.backups'));
    }
}
