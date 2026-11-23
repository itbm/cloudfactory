<?php

namespace App\Http\Controllers\Agent;

use App\Http\Controllers\Controller;
use App\Models\Server;
use App\Models\TerminalSession;
use App\Models\WebsiteCommand;
use Illuminate\Http\JsonResponse;
use Illuminate\Http\Request;
use Illuminate\Support\Carbon;
use Illuminate\Support\Facades\DB;
use Illuminate\Support\Str;

/**
 * The reports of the agent for the web terminal (FR-802): the commands of a session, and its
 * end. The agent sends the commands while the session is open. When the panel does not get
 * them, the agent closes the terminal.
 */
class TerminalController extends Controller
{
    /** The most commands in one report. */
    private const COMMANDS_MAX = 1000;

    public function commands(Request $request, string $id): JsonResponse
    {
        $session = $this->find($request, $id);
        $data = $request->validate(self::commandRules());
        abort_if($session->state === TerminalSession::ENDED, 409, 'The session ended.');
        $this->add($session, $data['commands']);

        return response()->json(['ok' => true]);
    }

    /** The end of a session. A second report of the same end has no effect. */
    public function end(Request $request, string $id): JsonResponse
    {
        $session = $this->find($request, $id);
        $data = $request->validate([
            ...self::commandRules(),
            'reason' => ['required', 'string', 'max:2000'],
            'exitCode' => ['nullable', 'integer', 'between:-1,255'],
            'recording' => ['nullable', 'string', 'max:'.(TerminalSession::RECORDING_MAX + 4096)],
            'recordingCut' => ['boolean'],
            'startedAt' => ['nullable', 'date'],
            'endedAt' => ['nullable', 'date'],
        ]);
        if ($session->state === TerminalSession::ENDED) {
            return response()->json(['ok' => true]);
        }
        DB::transaction(function () use ($session, $data) {
            $this->add($session, $data['commands']);
            $recording = (string) ($data['recording'] ?? '');
            $session->update([
                'state' => TerminalSession::ENDED,
                'end_reason' => mb_strimwidth($data['reason'], 0, 500, '…'),
                'exit_code' => $data['exitCode'] ?? null,
                'recording' => $recording === '' ? null : $recording,
                'recording_cut' => (bool) ($data['recordingCut'] ?? false),
                'started_at' => $session->started_at ?? self::time($data['startedAt'] ?? null),
                'ended_at' => self::time($data['endedAt'] ?? null) ?? now(),
            ]);
        });

        return response()->json(['ok' => true]);
    }

    /** A session of a website on the server of the agent. */
    private function find(Request $request, string $id): TerminalSession
    {
        /** @var Server $server */
        $server = $request->attributes->get('server');

        return TerminalSession::whereKey($id)->whereHas('website', fn ($q) => $q->where('server_id', $server->id))->firstOrFail();
    }

    /** @return array<string, list<string>> */
    private static function commandRules(): array
    {
        return [
            'commands' => ['present', 'array', 'max:'.self::COMMANDS_MAX],
            'commands.*.command' => ['required', 'string', 'max:'.(WebsiteCommand::TERMINAL_COMMAND_MAX * 4)],
            'commands.*.at' => ['required', 'date'],
        ];
    }

    /**
     * Adds commands to the command log of the website, with the person of the session.
     *
     * @param  list<array{command: string, at: string}>  $commands
     */
    private function add(TerminalSession $session, array $commands): void
    {
        if ($commands === []) {
            return;
        }
        $now = now();
        $rows = [];
        foreach ($commands as $c) {
            $at = self::time($c['at']) ?? $now;
            // The time of the agent, but not later than the panel time: the log stays in order.
            $at = $at->gt($now) ? $now : $at;
            $rows[] = [
                // The ID has the time of the command, so the log stays in order.
                'id' => strtolower((string) Str::ulid($at)),
                'website_id' => $session->website_id,
                'customer_id' => $session->customer_id,
                'user_id' => $session->user_id,
                'actor' => $session->actor,
                'ip' => $session->ip,
                'source' => WebsiteCommand::SOURCE_TERMINAL,
                'terminal_session_id' => $session->id,
                'command' => mb_substr($c['command'], 0, WebsiteCommand::TERMINAL_COMMAND_MAX),
                'state' => WebsiteCommand::DONE,
                'timed_out' => false,
                'started_at' => $at,
                'finished_at' => $at,
                'created_at' => $at,
                'updated_at' => $now,
            ];
        }
        WebsiteCommand::insert($rows);
        $session->increment('command_count', count($rows));
    }

    private static function time(?string $value): ?Carbon
    {
        return $value === null ? null : Carbon::parse($value);
    }
}
