<?php

namespace App\Http\Controllers\Api;

use App\Enums\RolloutState;
use App\Http\Controllers\Controller;
use App\Models\AgentJob;
use App\Models\Image;
use App\Models\Rollout;
use App\Models\Website;
use App\Services\Audit;
use App\Services\RolloutEngine;
use Illuminate\Http\JsonResponse;
use Illuminate\Http\Request;
use Illuminate\Validation\ValidationException;

/**
 * FR-1103 to FR-1107: staged image updates. "Pause", "resume" and "roll back" give HTTP status
 * 409 when the state of the rollout does not permit the action (audit finding F06).
 */
class RolloutController extends Controller
{
    public function __construct(private RolloutEngine $engine) {}

    public function index(): JsonResponse
    {
        return response()->json(['data' => Rollout::with('image')->latest()->limit(20)->get()->map(fn ($r) => self::present($r))]);
    }

    public function store(Request $request): JsonResponse
    {
        $data = $request->validate(['imageId' => ['required', 'exists:images,id']]);
        if (Rollout::whereIn('state', RolloutState::activeValues())->exists()) {
            throw ValidationException::withMessages(['imageId' => 'A rollout is in progress. Wait until it is complete, or roll it back.']);
        }
        $image = Image::findOrFail($data['imageId']);
        if (! $image->available_tag) {
            throw ValidationException::withMessages(['imageId' => 'This image is up to date.']);
        }
        if (! $image->signature_verified) {
            throw ValidationException::withMessages(['imageId' => 'The new image has no verified signature.']);
        }
        $rollout = $this->engine->start($image);
        Audit::record($request, 'Started rollout', "{$image->name} {$image->available_tag}");

        return response()->json(self::present($rollout->fresh('image')), 201);
    }

    public function show(string $id): JsonResponse
    {
        return response()->json(self::present(Rollout::with('image')->findOrFail($id)));
    }

    public function pause(Request $request, string $id): JsonResponse
    {
        $rollout = Rollout::findOrFail($id);
        $this->engine->pause($rollout);
        Audit::record($request, 'Paused rollout', $rollout->image->name);

        return response()->json(self::present($rollout->fresh('image')));
    }

    public function resume(Request $request, string $id): JsonResponse
    {
        $rollout = Rollout::findOrFail($id);
        $this->engine->resume($rollout);
        Audit::record($request, 'Resumed rollout', $rollout->image->name);

        return response()->json(self::present($rollout->fresh('image')));
    }

    public function rollBack(Request $request, string $id): JsonResponse
    {
        $rollout = Rollout::findOrFail($id);
        $this->engine->rollBack($rollout);
        Audit::record($request, 'Rolled back rollout', $rollout->image->name);

        return response()->json(self::present($rollout->fresh('image')));
    }

    public static function present(Rollout $r): array
    {
        $names = Website::whereIn('id', array_column($r->rolled_back, 'websiteId'))->pluck('name', 'id');

        return [
            'id' => $r->id,
            'imageId' => $r->image_id,
            'title' => "{$r->image->name} {$r->to_tag}",
            'fromTag' => $r->from_tag,
            'toTag' => $r->to_tag,
            'severity' => $r->severity,
            'state' => $r->state,
            'stages' => array_map(fn ($stage, $i) => [
                'name' => $stage['name'],
                'websites' => $r->stageTarget($i),
                'state' => $i < $r->stage || $r->state === RolloutState::Done ? 'done' : ($i === $r->stage ? 'in_progress' : 'waiting'),
            ], Rollout::STAGES, array_keys(Rollout::STAGES)),
            'websites' => count($r->website_ids),
            'healthy' => count($r->updated_ids),
            'rolledBack' => count($r->rolled_back),
            'running' => AgentJob::where('rollout_id', $r->id)->where('state', 'running')->count(),
            'rollbacks' => array_map(fn ($rb) => ['website' => $names[$rb['websiteId']] ?? $rb['websiteId'], 'reason' => $rb['reason']], $r->rolled_back),
            'startedAt' => $r->created_at?->toIso8601String(),
            // The time of the last change of a rollout that has ended. The panel shows an ended
            // rollout for one day only.
            'finishedAt' => ! $r->state->active() ? $r->updated_at?->toIso8601String() : null,
        ];
    }
}
