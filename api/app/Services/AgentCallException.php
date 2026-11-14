<?php

namespace App\Services;

/**
 * The agent did not do a request that gives its answer at once (AgentClient::call), for
 * example a request of the file manager. The code is a code of the Connect protocol, for
 * example "not_found", or "unavailable" when the panel cannot connect to the agent.
 */
class AgentCallException extends \RuntimeException
{
    public function __construct(public readonly string $agentCode, string $message)
    {
        parent::__construct($message);
    }

    /** The HTTP status of the answer of the panel. */
    public function status(): int
    {
        return match ($this->agentCode) {
            'not_found' => 404,
            'already_exists' => 409,
            'invalid_argument', 'out_of_range', 'resource_exhausted' => 422,
            'permission_denied' => 403,
            'unavailable', 'unimplemented', 'deadline_exceeded' => 503,
            default => 502,
        };
    }

    /** The text for the user. An error with no clear cause does not show the server from the inside. */
    public function userMessage(): string
    {
        return match ($this->agentCode) {
            'not_found', 'already_exists', 'invalid_argument', 'out_of_range', 'resource_exhausted', 'permission_denied', 'unavailable' => ucfirst($this->getMessage()),
            'unimplemented' => 'The agent of this server is from an earlier release. Update the agent of the server.',
            'deadline_exceeded' => 'The server did not answer in time. Try again.',
            default => 'The server could not do this. Try again later.',
        };
    }
}
