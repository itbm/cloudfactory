<?php

namespace App\Http\Controllers\Api;

use App\Http\Controllers\Controller;
use App\Http\Resources\JobResource;
use App\Models\AgentJob;
use Illuminate\Http\Request;
use Illuminate\Http\Resources\Json\AnonymousResourceCollection;

/** Jobs and their progress. The UI and the Terraform provider wait on these (FR-1606). */
class JobController extends Controller
{
    use Concerns;

    private function query(Request $request)
    {
        $user = $request->user();

        return AgentJob::query()->when(! $user->isAdministrator(), fn ($q) => $q->whereIn('website_id', $this->visibleWebsites($request)->select('id')));
    }

    public function index(Request $request): AnonymousResourceCollection
    {
        $request->validate(['websiteId' => ['nullable', 'string'], 'state' => ['nullable', 'in:queued,running,succeeded,failed']]);

        return JobResource::collection($this->query($request)
            ->when($request->query('websiteId'), fn ($q, $id) => $q->where('website_id', $id))
            ->when($request->query('serverId'), fn ($q, $id) => $q->where('server_id', $id))
            ->when($request->query('state'), fn ($q, $s) => $q->where('state', $s))
            ->latest()
            ->paginate(self::perPage($request)));
    }

    public function show(Request $request, string $id): JobResource
    {
        return new JobResource($this->query($request)->findOrFail($id));
    }
}
