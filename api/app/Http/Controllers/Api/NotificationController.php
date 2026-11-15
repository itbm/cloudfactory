<?php

namespace App\Http\Controllers\Api;

use App\Enums\NotificationEvent;
use App\Http\Controllers\Controller;
use App\Jobs\SendEmailNotification;
use App\Models\NotificationDelivery;
use App\Models\User;
use App\Services\Audit;
use App\Services\Notifications\Preferences;
use App\Services\Notifications\Senders;
use Illuminate\Database\Eloquent\Builder;
use Illuminate\Http\JsonResponse;
use Illuminate\Http\Request;
use Illuminate\Validation\Rule;
use Illuminate\Validation\ValidationException;

/**
 * The email notifications of the signed-in user: which events give an email, and the emails
 * that the panel sent. An administrator sees all the emails. A reseller sees the emails to its
 * users and to its customers, and a customer sees the emails to the users of its account.
 */
class NotificationController extends Controller
{
    public function preferences(Request $request): JsonResponse
    {
        return response()->json(['data' => Preferences::list($request->user())]);
    }

    public function updatePreferences(Request $request): JsonResponse
    {
        /** @var User $user */
        $user = $request->user();
        $data = $request->validate([
            'events' => ['required', 'array'],
            'events.*' => ['boolean'],
        ]);
        $choices = $user->notification_preferences ?? [];
        foreach ($data['events'] as $key => $on) {
            $event = NotificationEvent::tryFrom((string) $key);
            if (! $event || ! $event->availableTo($user->role)) {
                throw ValidationException::withMessages(["events.{$key}" => 'Your role does not receive this event.']);
            }
            if ($event->required() && ! $on) {
                throw ValidationException::withMessages(["events.{$key}" => "You cannot turn off \"{$event->label()}\". It is a security email."]);
            }
            $choices[$event->value] = (bool) $on;
        }
        $user->forceFill(['notification_preferences' => $choices])->save();
        Audit::record($request, 'Changed email notifications', $user->email);

        return response()->json(['data' => Preferences::list($user)]);
    }

    public function index(Request $request): JsonResponse
    {
        $data = $request->validate([
            'state' => ['nullable', Rule::in(['queued', 'sent', 'failed', 'skipped'])],
            'event' => ['nullable', Rule::enum(NotificationEvent::class)],
        ]);
        $page = self::visible($request)
            ->when($data['state'] ?? null, fn ($q, $s) => $q->where('state', $s))
            ->when($data['event'] ?? null, fn ($q, $e) => $q->where('event', $e))
            ->latest()
            ->orderByDesc('id')
            ->paginate(self::perPage($request, 50, 200));

        return response()->json([
            'data' => collect($page->items())->map(fn (NotificationDelivery $d) => self::present($d)),
            'meta' => ['currentPage' => $page->currentPage(), 'lastPage' => $page->lastPage(), 'total' => $page->total()],
        ]);
    }

    public function show(Request $request, string $id): JsonResponse
    {
        $delivery = self::visible($request)->findOrFail($id);

        return response()->json(self::present($delivery) + ['body' => $delivery->body]);
    }

    /**
     * Sends a failed or skipped email again, for example after a change of the SMTP server.
     * Only the administrator, or the reseller whose server sends the email, can.
     */
    public function retry(Request $request, string $id): JsonResponse
    {
        $scope = MailServerController::scope($request);
        $delivery = self::visible($request)->findOrFail($id);
        $reseller = $delivery->user ? Senders::brandOwner($delivery->user) : null;
        if ($scope && $reseller?->id !== $scope->id) {
            return response()->json(['message' => 'The panel sends this email. Only an administrator can send it again.'], 403);
        }
        if (! in_array($delivery->state, ['failed', 'skipped'], true)) {
            return response()->json(['message' => 'Only a failed or skipped email can be sent again.'], 409);
        }
        $server = Senders::server($reseller);
        if (! $server) {
            return response()->json(['message' => 'Set up the SMTP server first.'], 409);
        }
        $delivery->update(['state' => 'queued', 'attempts' => 0, 'error' => null, 'sender_id' => $server->customer_id]);
        SendEmailNotification::dispatch($delivery->id);
        Audit::record($request, 'Sent email again', $delivery->subject, $scope?->id);

        return response()->json(self::present($delivery->fresh()));
    }

    /**
     * The emails that the user can see. A sign-in email has the address of the person who
     * signed in, so only that user and the administrators see it.
     *
     * @return Builder<NotificationDelivery>
     */
    private static function visible(Request $request): Builder
    {
        /** @var User $user */
        $user = $request->user();
        $ids = $user->visibleCustomerIds();

        return NotificationDelivery::query()
            ->when($ids !== null, fn ($q) => $q
                ->whereIn('customer_id', $ids)
                ->where(fn ($q) => $q->where('event', '!=', NotificationEvent::NewDeviceSignIn->value)->orWhere('user_id', $user->id)));
    }

    /** @return array<string, mixed> */
    private static function present(NotificationDelivery $d): array
    {
        $event = NotificationEvent::tryFrom($d->event);

        return [
            'id' => $d->id,
            'event' => $d->event,
            'label' => $event?->label() ?? $d->event,
            'recipient' => $d->recipient,
            'subject' => $d->subject,
            'state' => $d->state,
            'error' => $d->error,
            'attempts' => $d->attempts,
            // Null: the server of the panel. Else the reseller whose server sent it.
            'sentBy' => $d->sender_id ? 'reseller' : 'panel',
            'createdAt' => $d->created_at?->toIso8601String(),
            'sentAt' => $d->sent_at?->toIso8601String(),
        ];
    }
}
