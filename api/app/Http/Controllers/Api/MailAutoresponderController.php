<?php

namespace App\Http\Controllers\Api;

use App\Http\Controllers\Controller;
use App\Models\Mailbox;
use App\Models\MailDomain;
use App\Services\AgentCallException;
use App\Services\AgentClient;
use App\Services\Audit;
use App\Services\MailClusters;
use App\Validation\Input;
use Illuminate\Database\Eloquent\Builder;
use Illuminate\Http\JsonResponse;
use Illuminate\Http\Request;
use Illuminate\Validation\ValidationException;

/**
 * The autoresponder of a mailbox. When it is on, the mail server answers each new message with
 * a text, one time for each sender in 30 days.
 *
 * The mail server keeps the autoresponder, and the user can also change it in the webmail. The
 * panel keeps no copy: it reads and changes the autoresponder through the agent, with no job.
 */
class MailAutoresponderController extends Controller
{
    public function __construct(private AgentClient $agent) {}

    public function show(Request $request, string $id): JsonResponse
    {
        $mailbox = $this->mailbox($request, $id);

        return response()->json(['autoresponder' => self::present($this->call($mailbox, 'GetAutoresponder', self::address($mailbox)))]);
    }

    /**
     * Sets the autoresponder. "startsAt" and "endsAt" are RFC 3339 times, or null for no start
     * or no end. The mail server keeps the text when the autoresponder is off.
     */
    public function update(Request $request, string $id): JsonResponse
    {
        $mailbox = $this->mailbox($request, $id);
        abort_if($mailbox->customer?->isSuspended() ?? false, 409, 'This customer is suspended. Lift the suspension before you change the autoresponder.');
        $data = $request->validate([
            'enabled' => ['required', 'boolean'],
            'subject' => ['present', 'nullable', 'string', Input::rule('autoresponderSubject')],
            'text' => ['present', 'nullable', 'string', 'required_if:enabled,true', Input::rule('autoresponderText')],
            'startsAt' => ['present', 'nullable', 'date_format:Y-m-d\TH:i:sp'],
            'endsAt' => ['present', 'nullable', 'date_format:Y-m-d\TH:i:sp'],
        ], [
            'text.required_if' => 'Type the text of the answer.',
            'startsAt.date_format' => 'Give the start as an RFC 3339 time, for example 2026-12-20T08:00:00Z.',
            'endsAt.date_format' => 'Give the end as an RFC 3339 time, for example 2027-01-04T23:59:59Z.',
        ]);
        $start = self::utc($data['startsAt']);
        $end = self::utc($data['endsAt']);
        if ($start !== '' && $end !== '' && $end <= $start) {
            throw ValidationException::withMessages(['endsAt' => 'The end must be after the start.']);
        }
        $autoresponder = self::present($this->call($mailbox, 'SetAutoresponder', self::address($mailbox) + ['autoresponder' => [
            'enabled' => $data['enabled'],
            'subject' => (string) $data['subject'],
            'text' => (string) $data['text'],
            'startsAt' => $start,
            'endsAt' => $end,
        ]]));
        Audit::record($request, $autoresponder['enabled'] ? 'Turned on autoresponder' : 'Turned off autoresponder', $mailbox->address(), $mailbox->customer_id);

        return response()->json(['autoresponder' => $autoresponder]);
    }

    /** An active mailbox that the user can see. */
    private function mailbox(Request $request, string $id): Mailbox
    {
        $ids = $request->user()->visibleCustomerIds();
        $mailbox = Mailbox::with(['domain.server', 'customer'])->when($ids !== null, fn (Builder $q) => $q->whereIn('customer_id', $ids))->findOrFail($id);
        if ($mailbox->status !== MailDomain::ACTIVE) {
            throw ValidationException::withMessages(['mailboxId' => match ($mailbox->status) {
                MailDomain::PENDING => 'The panel makes this mailbox. Wait until it is complete.',
                MailDomain::REMOVING => 'The panel removes this mailbox.',
                default => 'This mailbox is not on the mail server.',
            }]);
        }

        return $mailbox;
    }

    /** @return array{domain: string, localPart: string} */
    private static function address(Mailbox $m): array
    {
        return ['domain' => $m->domain->name, 'localPart' => $m->local_part];
    }

    /** An RFC 3339 time in UTC, or "" for no time. */
    private static function utc(?string $time): string
    {
        return $time === null ? '' : (new \DateTimeImmutable($time))->setTimezone(new \DateTimeZone('UTC'))->format('Y-m-d\TH:i:s\Z');
    }

    /**
     * @param  array<string, mixed>  $payload
     * @return array<string, mixed>
     */
    private function call(Mailbox $mailbox, string $method, array $payload): array
    {
        try {
            return $this->agent->call(MailClusters::member($mailbox->domain->server), $method, $payload);
        } catch (AgentCallException $e) {
            abort($e->status(), match ($e->agentCode) {
                'unavailable' => 'The panel cannot connect to the mail server of this mailbox. Try again later.',
                'not_found' => 'The mail server does not have this mailbox.',
                default => $e->userMessage(),
            });
        }
    }

    /**
     * The autoresponder for the API, from the JSON of the agent. The JSON of the agent has no
     * field with an empty value.
     *
     * @param  array<string, mixed>  $a
     * @return array{enabled: bool, subject: ?string, text: string, startsAt: ?string, endsAt: ?string, filterScript: ?string}
     */
    public static function present(array $a): array
    {
        $text = fn (string $key) => ($a[$key] ?? '') === '' ? null : (string) $a[$key];

        return [
            'enabled' => (bool) ($a['enabled'] ?? false),
            // Null: the subject of the answer is "Auto:" and the subject of the message.
            'subject' => $text('subject'),
            'text' => (string) ($a['text'] ?? ''),
            'startsAt' => $text('startsAt'),
            'endsAt' => $text('endsAt'),
            // Not null: the name of the active filter rules (a Sieve script) of the mailbox.
            // They stop while the autoresponder is on.
            'filterScript' => $text('filterScript'),
        ];
    }
}
