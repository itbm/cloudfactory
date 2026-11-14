<?php

namespace App\Http\Resources;

use App\Models\AgentJob;
use Illuminate\Http\Request;
use Illuminate\Http\Resources\Json\JsonResource;
use Illuminate\Support\Str;

/** @mixin AgentJob */
class JobResource extends JsonResource
{
    /**
     * Audit finding S9: an administrator sees the full error of the agent. For other users,
     * the error of a host command becomes the name of the step, because the full text has the
     * command line and the output of the command. Other errors are one line of 300 characters
     * at most, for example "the website did not answer".
     */
    private static function errorFor(Request $request, AgentJob $job): ?string
    {
        $error = $job->error;
        if ($error === null || $request->user()?->isAdministrator()) {
            return $error;
        }
        if (preg_match('/: exit status \d+|: signal: |executable file not found/', $error)) {
            $step = $job->steps[$job->step] ?? null;

            return ($step ? "The step \"{$step}\" failed on the server." : 'A step failed on the server.').' Your provider can see the details.';
        }

        return Str::limit(trim((string) strtok($error, "\r\n")), 300);
    }

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
            'error' => self::errorFor($request, $this->resource),
            'websiteId' => $this->website_id,
            'databaseServerId' => $this->database_server_id,
            'serverId' => $this->server_id,
            'createdAt' => $this->created_at?->toIso8601String(),
            'finishedAt' => $this->finished_at?->toIso8601String(),
        ];
    }
}
