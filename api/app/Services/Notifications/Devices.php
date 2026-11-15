<?php

namespace App\Services\Notifications;

use App\Models\User;
use App\Models\UserDevice;
use Illuminate\Http\Request;

/**
 * The devices that each user signs in from. The web panel sends a random device ID that it
 * keeps in the browser. A client with no ID, for example a script, is known by its user agent.
 */
class Devices
{
    /**
     * Records the device of a sign-in. True when the user signed in before, but never from
     * this device: then the panel sends an email.
     */
    public static function seen(User $user, Request $request): bool
    {
        $agent = mb_substr((string) $request->userAgent(), 0, 500);
        $id = (string) $request->input('deviceId', '');
        $fingerprint = hash('sha256', preg_match('/^[A-Za-z0-9_-]{16,100}$/', $id) ? "id:{$id}" : "ua:{$agent}");
        $device = UserDevice::where('user_id', $user->id)->where('fingerprint', $fingerprint)->first();
        if ($device) {
            $device->update(['last_seen_at' => now(), 'ip' => $request->ip()]);

            return false;
        }
        $first = ! UserDevice::where('user_id', $user->id)->exists();
        UserDevice::create([
            'user_id' => $user->id,
            'fingerprint' => $fingerprint,
            'label' => self::label($agent),
            'ip' => $request->ip(),
            'last_seen_at' => now(),
        ]);
        // Keep the 20 devices that the user used last.
        $old = UserDevice::where('user_id', $user->id)->orderByDesc('last_seen_at')->skip(20)->take(100)->pluck('id');
        if ($old->isNotEmpty()) {
            UserDevice::whereIn('id', $old)->delete();
        }

        return ! $first;
    }

    /** A short name for a user agent, for example "Firefox on Linux". */
    public static function label(string $agent): string
    {
        $browser = match (true) {
            str_contains($agent, 'Edg/') => 'Edge',
            str_contains($agent, 'OPR/') => 'Opera',
            str_contains($agent, 'Firefox/') => 'Firefox',
            str_contains($agent, 'Chrome/') => 'Chrome',
            str_contains($agent, 'Safari/') => 'Safari',
            str_starts_with($agent, 'cf/') => 'The cf command',
            str_contains($agent, 'curl/') => 'curl',
            $agent === '' => 'An unknown client',
            default => mb_substr(strtok($agent, ' ') ?: $agent, 0, 60),
        };
        $os = match (true) {
            str_contains($agent, 'Windows') => 'Windows',
            str_contains($agent, 'iPhone') || str_contains($agent, 'iPad') => 'iOS',
            str_contains($agent, 'Mac OS X') || str_contains($agent, 'Macintosh') => 'macOS',
            str_contains($agent, 'Android') => 'Android',
            str_contains($agent, 'CrOS') => 'ChromeOS',
            str_contains($agent, 'Linux') => 'Linux',
            default => null,
        };

        return $os ? "{$browser} on {$os}" : $browser;
    }
}
