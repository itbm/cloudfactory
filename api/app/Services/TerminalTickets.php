<?php

namespace App\Services;

use App\Models\AgentJob;
use App\Models\Server;
use App\Models\TerminalSession;

/**
 * The tickets of the terminal relay (FR-802). The relay (agent/internal/relay) connects the
 * browser of a user to the agent of a server. It has no database: each side shows a ticket that
 * the panel made.
 *
 * A ticket is "<time limit>.<HMAC>": the HMAC-SHA256 of the role, the session and the time
 * limit, with a key that the panel and the relay both make from APP_KEY. The relay accepts a
 * ticket one time.
 */
class TerminalTickets
{
    public const BROWSER = 'browser';

    public const AGENT = 'agent';

    /** The message of the HMAC that makes the key of the tickets from APP_KEY. The relay uses the same label. */
    private const LABEL = 'cloudfactory terminal relay';

    /** The relay on the panel server, for the agent on that server (quadlet.PanelTerminalPort). */
    public const LOCAL_RELAY = 'http://169.254.77.1:19082';

    public static function make(string $role, string $sessionId, int $until): string
    {
        $key = hash_hmac('sha256', self::LABEL, (string) config('app.key'), true);

        return $until.'.'.hash_hmac('sha256', "{$role}\n{$sessionId}\n{$until}", $key);
    }

    /** The ticket of the browser. The browser connects at once. */
    public static function browser(TerminalSession $session): string
    {
        return self::make(self::BROWSER, $session->id, now()->addSeconds(TerminalSession::BROWSER_TICKET_SECONDS)->getTimestamp());
    }

    /** The path of the WebSocket of the browser on the panel domain. */
    public static function browserPath(TerminalSession $session): string
    {
        return "/api/terminal/{$session->id}/browser";
    }

    /**
     * The address of the relay and the ticket of the agent, for the job OpenTerminal. They are
     * not in the job: the panel makes a new ticket each time that it sends the job.
     *
     * @return array{relayUrl: string, ticket: string}
     */
    public static function forAgent(AgentJob $job, Server $server): array
    {
        $sessionId = (string) ($job->payload['sessionId'] ?? '');

        return [
            // The agent on the panel server connects to the relay on the private service address.
            // The other agents connect to the panel domain, where the edge proxy sends
            // /api/terminal/* to the relay.
            'relayUrl' => $server->local ? self::LOCAL_RELAY : 'https://'.config('cloudfactory.panel_domain'),
            'ticket' => self::make(self::AGENT, $sessionId, now()->addSeconds(TerminalSession::AGENT_TICKET_SECONDS)->getTimestamp()),
        ];
    }
}
