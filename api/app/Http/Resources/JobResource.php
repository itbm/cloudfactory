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
     * The marks of an error text that shows the server from the inside: a host command, a
     * path on the host, a socket or a network address (second audit, finding U2).
     */
    private const INTERNAL = '/: exit status \d+|: signal: |executable file not found'
        .'|\/(var|srv|run|etc|usr|home|root|tmp|opt|proc|sys|dev)\/'
        .'|\bdial (tcp|unix)\b|\bunix:|https?:\/\/|\b\d{1,3}(\.\d{1,3}){3}\b'
        .'|permission denied|no such file or directory|connection refused/i';

    /**
     * Audit finding S9: an administrator sees the full error of the agent. For other users,
     * an error that shows the server from the inside becomes the name of the step. Other
     * errors are one line of 300 characters at most, for example "the website did not answer".
     */
    private static function errorFor(Request $request, AgentJob $job): ?string
    {
        $error = $job->error;
        if ($error === null || $request->user()?->isAdministrator()) {
            return $error;
        }
        if (preg_match(self::INTERNAL, $error)) {
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
            'serverId' => $this->server_id,
            'createdAt' => $this->created_at?->toIso8601String(),
            'finishedAt' => $this->finished_at?->toIso8601String(),
        ];
    }
}
