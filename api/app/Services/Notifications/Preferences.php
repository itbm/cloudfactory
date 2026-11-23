<?php

namespace App\Services\Notifications;

use App\Enums\NotificationEvent;
use App\Models\User;

/** The events that a user receives by email. */
class Preferences
{
    public static function wants(User $user, NotificationEvent $event): bool
    {
        if (! $event->availableTo($user->role)) {
            return false;
        }
        if ($event->required()) {
            return true;
        }
        $choices = $user->notification_preferences ?? [];

        return (bool) ($choices[$event->value] ?? $event->defaultOn($user->role));
    }

    /**
     * The events of the role of the user, and whether the user receives each one.
     *
     * @return list<array{event: string, label: string, description: string, email: bool, required: bool}>
     */
    public static function list(User $user): array
    {
        $out = [];
        foreach (NotificationEvent::cases() as $event) {
            if (! $event->availableTo($user->role)) {
                continue;
            }
            $out[] = [
                'event' => $event->value,
                'label' => $event->label(),
                'description' => $event->description(),
                'email' => self::wants($user, $event),
                'required' => $event->required(),
            ];
        }

        return $out;
    }
}
