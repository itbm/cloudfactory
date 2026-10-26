<?php

namespace App\Http\Resources;

use App\Models\AgentJob;
use Illuminate\Http\Request;
use Illuminate\Http\Resources\Json\JsonResource;

/** @mixin AgentJob */
class JobResource extends JsonResource
{
    public function toArray(Request $request): array
    {
        return [
            'id' => $this->id,
            'title' => $this->title,
            'ref' => $this->ref,
            'action' => $this->action->value,
            'state' => $this->state->value,
            'steps' => $this->steps,
            'step' => $this->step,
            'error' => $this->error,
            'websiteId' => $this->website_id,
            'serverId' => $this->server_id,
            'createdAt' => $this->created_at?->toIso8601String(),
            'finishedAt' => $this->finished_at?->toIso8601String(),
        ];
    }
}
