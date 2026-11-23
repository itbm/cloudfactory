<?php

namespace App\Services;

use App\Enums\AgentAction;
use App\Models\AgentJob;
use App\Models\Server;

/**
 * The spam filter and the outbound relay of the mail server of a server. An administrator sets
 * them. The panel keeps them in "mail_settings" of the server, and the password of the relay
 * encrypted in "mail_relay_password". The job record does not have the password: the panel
 * adds it each time that it sends the job (passwordFor).
 *
 * The default settings are the settings of a new mail server: the spam filter is on, a message
 * with the score 5 or more goes to the junk folder, the server refuses no message for its
 * score, and the server sends each message directly to the mail server of the recipient.
 */
class MailSettings
{
    public const DEFAULTS = ['spamFilter' => true, 'spamScore' => 5.0, 'rejectScore' => null, 'relay' => null];

    /** The limits of the scores and of the relay. The agent has the same limits. */
    public const MIN_SPAM_SCORE = 1;

    public const MAX_SPAM_SCORE = 50;

    public const MAX_REJECT_SCORE = 100;

    public const RELAY_PORTS = [25, 465, 587, 2525];

    /**
     * The settings of the mail server of a server.
     *
     * @return array{spamFilter: bool, spamScore: float, rejectScore: ?float, relay: ?array{host: string, port: int, username: ?string}}
     */
    public static function of(Server $server): array
    {
        $s = [...self::DEFAULTS, ...($server->mail_settings ?? [])];

        return [
            'spamFilter' => (bool) $s['spamFilter'],
            'spamScore' => (float) $s['spamScore'],
            'rejectScore' => $s['rejectScore'] === null ? null : (float) $s['rejectScore'],
            'relay' => is_array($s['relay']) ? [
                'host' => (string) $s['relay']['host'],
                'port' => (int) $s['relay']['port'],
                'username' => $s['relay']['username'] ?? null,
            ] : null,
        ];
    }

    /**
     * The settings for the API. The panel never shows the password of the relay again.
     *
     * @return array<string, mixed>
     */
    public static function present(Server $server): array
    {
        $s = self::of($server);
        if ($s['relay'] !== null) {
            $s['relay']['hasPassword'] = ($server->mail_relay_password ?? '') !== '';
        }

        return $s;
    }

    /**
     * The values of a SetMailSettings job, with no password.
     *
     * @param  array{spamFilter: bool, spamScore: float, rejectScore: ?float, relay: ?array{host: string, port: int, username: ?string}}  $settings
     * @return array<string, mixed>
     */
    public static function payload(array $settings): array
    {
        return [
            'spamFilter' => $settings['spamFilter'],
            'spamScore' => $settings['spamScore'],
            // 0: the mail server refuses no message for its score.
            'rejectScore' => $settings['rejectScore'] ?? 0,
            'relay' => $settings['relay'] === null ? null : [
                'host' => $settings['relay']['host'],
                'port' => $settings['relay']['port'],
                'username' => $settings['relay']['username'] ?? '',
            ],
        ];
    }

    /**
     * The relay of a SetMailSettings job with its current password.
     *
     * @return array<string, mixed>
     */
    public static function passwordFor(AgentJob $job): array
    {
        $relay = $job->payload['relay'] ?? null;
        if ($job->action !== AgentAction::SetMailSettings || ! is_array($relay) || ($relay['username'] ?? '') === '') {
            return [];
        }

        return ['relay' => [...$relay, 'password' => (string) ($job->server->mail_relay_password ?? '')]];
    }
}
