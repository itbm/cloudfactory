<?php

namespace App\Http\Controllers\Api;

use App\Enums\NotificationEvent;
use App\Enums\Role;
use App\Http\Controllers\Controller;
use App\Models\Customer;
use App\Models\NotificationTemplate;
use App\Services\Audit;
use App\Services\Notifications\Senders;
use App\Services\Notifications\Templates;
use Illuminate\Http\JsonResponse;
use Illuminate\Http\Request;
use Illuminate\Validation\ValidationException;

/**
 * The subject and the text of the email of each event. An administrator changes the templates
 * of the panel. A reseller changes the templates of the emails to its customers. "Reset" goes
 * back to the template of the panel, or to the default.
 */
class NotificationTemplateController extends Controller
{
    public function index(Request $request): JsonResponse
    {
        $scope = MailServerController::scope($request);

        return response()->json(['data' => array_map(fn (NotificationEvent $e) => self::present($e, $scope), self::events($scope))]);
    }

    public function update(Request $request, string $event): JsonResponse
    {
        $scope = MailServerController::scope($request);
        $e = self::find($event, $scope);
        $data = $request->validate([
            'subject' => ['required', 'string', 'max:'.Templates::SUBJECT_MAX, 'not_regex:/[\r\n]/'],
            'body' => ['required', 'string', 'max:'.Templates::BODY_MAX],
        ]);
        self::checkVariables($e, $data['subject'], $data['body']);
        NotificationTemplate::updateOrCreate(
            ['customer_id' => $scope?->id, 'event' => $e->value],
            ['subject' => $data['subject'], 'body' => str_replace("\r\n", "\n", $data['body'])],
        );
        Audit::record($request, 'Changed email template', $e->label(), $scope?->id);

        return response()->json(self::present($e, $scope));
    }

    public function destroy(Request $request, string $event): JsonResponse
    {
        $scope = MailServerController::scope($request);
        $e = self::find($event, $scope);
        if (NotificationTemplate::where('customer_id', $scope?->id)->where('event', $e->value)->delete()) {
            Audit::record($request, 'Reset email template', $e->label(), $scope?->id);
        }

        return response()->json(self::present($e, $scope));
    }

    /** The email with the example values, before the user saves the template. */
    public function preview(Request $request, string $event): JsonResponse
    {
        $scope = MailServerController::scope($request);
        $e = self::find($event, $scope);
        $data = $request->validate([
            'subject' => ['required', 'string', 'max:'.Templates::SUBJECT_MAX],
            'body' => ['required', 'string', 'max:'.Templates::BODY_MAX],
        ]);
        self::checkVariables($e, $data['subject'], $data['body']);
        $brand = Senders::brand($scope);
        $values = ['brand' => $brand['name'], 'panelUrl' => $brand['url']] + $e->variables();
        $text = Templates::fill(str_replace("\r\n", "\n", $data['body']), $values);

        return response()->json([
            'subject' => Templates::fill($data['subject'], $values, oneLine: true),
            'text' => $text,
            'html' => Templates::html($text, $brand),
        ]);
    }

    /**
     * The events of the emails that a scope sends. A reseller sends emails to its customers
     * only, so it does not see the events of administrators.
     *
     * @return list<NotificationEvent>
     */
    private static function events(?Customer $scope): array
    {
        return array_values(array_filter(NotificationEvent::cases(), fn (NotificationEvent $e) => ! $scope || $e->availableTo(Role::Owner)));
    }

    private static function find(string $event, ?Customer $scope): NotificationEvent
    {
        $e = NotificationEvent::tryFrom($event);
        if (! $e || ! in_array($e, self::events($scope), true)) {
            abort(response()->json(['message' => 'This event does not exist.'], 404));
        }

        return $e;
    }

    private static function checkVariables(NotificationEvent $e, string $subject, string $body): void
    {
        $errors = [];
        foreach (['subject' => $subject, 'body' => $body] as $field => $text) {
            if ($unknown = Templates::unknownVariables($e, $text)) {
                $errors[$field] = 'This event has no '.implode(', ', array_map(fn ($v) => '{{'.$v.'}}', $unknown)).'. Use: '.implode(', ', array_map(fn ($v) => '{{'.$v.'}}', array_keys($e->variables()))).'.';
            }
        }
        if ($errors) {
            throw ValidationException::withMessages($errors);
        }
    }

    /** @return array<string, mixed> */
    private static function present(NotificationEvent $e, ?Customer $scope): array
    {
        $current = Templates::effective($e, $scope);
        // For a reseller, "panel" and "default" are both the template that it did not change.
        $inherited = $scope ? Templates::effective($e, null) : ['subject' => $e->defaultSubject(), 'body' => $e->defaultBody()];

        return [
            'event' => $e->value,
            'label' => $e->label(),
            'description' => $e->description(),
            'subject' => $current['subject'],
            'body' => $current['body'],
            'changed' => $current['source'] === ($scope ? 'reseller' : 'panel'),
            'originalSubject' => $inherited['subject'],
            'originalBody' => $inherited['body'],
            'variables' => array_keys($e->variables()),
        ];
    }
}
