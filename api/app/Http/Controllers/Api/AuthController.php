<?php

namespace App\Http\Controllers\Api;

use App\Http\Controllers\Controller;
use App\Models\User;
use App\Services\Audit;
use App\Services\LoginThrottle;
use App\Services\Totp;
use Illuminate\Http\JsonResponse;
use Illuminate\Http\Request;
use Illuminate\Support\Facades\Cache;
use Illuminate\Support\Facades\Crypt;
use Illuminate\Support\Facades\Hash;
use Illuminate\Support\Str;
use Illuminate\Validation\Rule;
use Illuminate\Validation\Rules\Password;
use Illuminate\Validation\ValidationException;

/** FR-103 and FR-104: sign in, and API tokens with scopes and an expiry date. */
class AuthController extends Controller
{
    /**
     * Sign in with an email, a password and a TOTP code. The answer holds a session token.
     *
     * CF-12: each account has its own limit of failed attempts. CF-14: the panel checks a
     * password hash also when the email is not known, so that the time does not show which
     * emails have accounts. CF-08: the users of a suspended customer cannot sign in.
     * R-08: the panel checks the password and the code together, and gives one error for both,
     * so that the answer does not show that a password is correct.
     */
    public function login(Request $request): JsonResponse
    {
        $data = $request->validate([
            'email' => ['required', 'email'],
            'password' => ['required', 'string'],
            'totp' => ['nullable', 'digits:6'],
        ]);
        $ip = (string) $request->ip();
        if ($seconds = LoginThrottle::lockedFor($data['email'], $ip)) {
            return response()->json(['message' => "Too many failed attempts. Try again in {$seconds} seconds."], 429)
                ->header('Retry-After', (string) $seconds);
        }
        $user = User::where('email', $data['email'])->first();
        $passwordOk = Hash::check($data['password'], $user?->password ?? self::dummyHash()) && $user;
        // The code is checked (and used up) only after a correct password.
        if (! $passwordOk || ! Totp::consume($user, $data['totp'] ?? '')) {
            if (LoginThrottle::fail($data['email'], $ip)) {
                Audit::system('Locked sign-in after failed attempts', "{$data['email']} from {$ip}");
            }
            throw ValidationException::withMessages(['email' => 'The email, the password or the code is not correct.']);
        }
        if ($user->isSuspended()) {
            // Audit finding S8: with no second factor, the answer for a suspended account must
            // not show that the password is correct. A user who gave a correct code has shown
            // more than the password, and gets the reason.
            if (! $user->totp_secret) {
                LoginThrottle::fail($data['email'], $ip);
                throw ValidationException::withMessages(['email' => 'The email, the password or the code is not correct.']);
            }

            return response()->json(['message' => 'This account is suspended. Contact your provider.'], 403);
        }
        LoginThrottle::clear($data['email'], $ip);

        $token = $user->createToken(self::SESSION, ['*'], now()->addHours(12));
        $request->setUserResolver(fn () => $user);
        Audit::record($request, $user->totp_secret ? 'Signed in with password and TOTP' : 'Signed in with password', $user->email);

        return response()->json(['token' => $token->plainTextToken, 'expiresAt' => $token->accessToken->expires_at?->toIso8601String()]);
    }

    /** The name of the token of a sign-in. */
    public const SESSION = 'Web session';

    /**
     * Audit finding S8: sign out. The panel deletes the token of this request, so that a copy
     * of the token does not work after the sign-out.
     */
    public function logout(Request $request): JsonResponse
    {
        $token = $request->user()->currentAccessToken();
        if ($token && method_exists($token, 'delete')) {
            $token->delete();
        }

        return response()->json(null, 204);
    }

    /**
     * R-01: change the password. The user gives the current password and, when TOTP is on, a
     * code. The panel then signs out all other sessions of the user. Audit finding S8: it also
     * deletes the API tokens of the user, because a person who knew the old password could
     * make a token.
     */
    public function changePassword(Request $request): JsonResponse
    {
        $data = $request->validate([
            'currentPassword' => ['required', 'string'],
            'password' => ['required', 'string', 'confirmed', 'different:currentPassword', Password::min(12)],
            'totp' => ['nullable', 'digits:6'],
        ]);
        $user = self::sessionUser($request);
        self::checkSecondFactor($user, $data['currentPassword'], $data['totp'] ?? '', 'currentPassword');
        $user->forceFill(['password' => $data['password'], 'must_change_password' => false])->save();
        $current = $user->currentAccessToken();
        $user->tokens()->when($current?->id, fn ($q, $id) => $q->where('id', '!=', $id))->delete();
        Audit::record($request, 'Changed password', $user->email);

        return response()->json(null, 204);
    }

    /**
     * R-01: the first step to turn on TOTP. The panel makes a new key and keeps it for 10 minutes.
     * TOTP is on only after the user confirms a code from the app (enableTotp).
     */
    public function setupTotp(Request $request): JsonResponse
    {
        $user = self::sessionUser($request);
        if ($user->totp_secret) {
            return response()->json(['message' => 'TOTP is already on. Turn it off first.'], 409);
        }
        $secret = Totp::generateSecret();
        Cache::put(self::pendingTotpKey($user), Crypt::encryptString($secret), now()->addMinutes(10));
        $issuer = rawurlencode((string) config('app.name', 'Cloudfactory'));

        return response()->json([
            'secret' => $secret,
            'uri' => "otpauth://totp/{$issuer}:".rawurlencode($user->email)."?secret={$secret}&issuer={$issuer}&algorithm=SHA1&digits=6&period=30",
        ]);
    }

    /** R-01: turns on TOTP with the key from setupTotp, the password and a code from the app. */
    public function enableTotp(Request $request): JsonResponse
    {
        $data = $request->validate(['password' => ['required', 'string'], 'code' => ['required', 'digits:6']]);
        $user = self::sessionUser($request);
        if ($user->totp_secret) {
            return response()->json(['message' => 'TOTP is already on. Turn it off first.'], 409);
        }
        if (! Hash::check($data['password'], $user->password)) {
            throw ValidationException::withMessages(['password' => 'The password is not correct.']);
        }
        $pending = Cache::get(self::pendingTotpKey($user));
        if (! $pending) {
            throw ValidationException::withMessages(['code' => 'Start the setup again. The key is valid for 10 minutes.']);
        }
        $secret = Crypt::decryptString($pending);
        $step = Totp::step($secret, $data['code']);
        if ($step === null) {
            throw ValidationException::withMessages(['code' => 'The code is not correct. Check the time on your device.']);
        }
        $user->forceFill(['totp_secret' => $secret, 'totp_last_step' => $step])->save();
        Cache::forget(self::pendingTotpKey($user));
        Audit::record($request, 'Set up TOTP', $user->email);

        return response()->json(null, 204);
    }

    /** R-01: turns off TOTP. The user gives the password and a code. */
    public function disableTotp(Request $request): JsonResponse
    {
        $data = $request->validate(['password' => ['required', 'string'], 'code' => ['required', 'digits:6']]);
        $user = self::sessionUser($request);
        if (! $user->totp_secret) {
            return response()->json(null, 204);
        }
        self::checkSecondFactor($user, $data['password'], $data['code'], 'password', 'code');
        $user->forceFill(['totp_secret' => null, 'totp_last_step' => null])->save();
        Audit::record($request, 'Removed TOTP', $user->email);

        return response()->json(null, 204);
    }

    /**
     * R-01: only a sign-in session can change how the user signs in. An API token cannot, also
     * when it has all scopes.
     */
    private static function sessionUser(Request $request): User
    {
        abort_unless(self::isSession($request), 403, 'Sign in to the panel to change your sign-in settings. An API token cannot change them.');

        return $request->user();
    }

    /** True when the request has the token of a sign-in, and not an API token. */
    public static function isSession(Request $request): bool
    {
        $token = $request->user()?->currentAccessToken();

        return $token && method_exists($token, 'can') && $token->can('*') && $token->getAttribute('name') === self::SESSION;
    }

    /** @throws ValidationException */
    private static function checkSecondFactor(User $user, string $password, string $code, string $passwordField, string $codeField = 'totp'): void
    {
        if (! Hash::check($password, $user->password)) {
            throw ValidationException::withMessages([$passwordField => 'The password is not correct.']);
        }
        if (! Totp::consume($user, $code)) {
            throw ValidationException::withMessages([$codeField => 'The code is not correct.']);
        }
    }

    private static function pendingTotpKey(User $user): string
    {
        return "totp-setup:{$user->id}";
    }

    /**
     * CF-14: a fixed hash of a random password that nobody knows. It has the default cost
     * (BCRYPT_ROUNDS=12), so a check for an unknown email takes the same time as a check for a
     * real account. The panel does not make a new hash for each request, because that takes more
     * time than a check.
     */
    public const DUMMY_HASH = '$2y$12$XePybNj9/.6zjnbETTpgDOEnx7ObhvG05470wrnl1SEGb3vnQR9bW';

    /**
     * The dummy hash for the current hash settings. If the settings are not the default, the panel
     * makes one hash with these settings and keeps it in the cache for all later requests.
     */
    public static function dummyHash(): string
    {
        if (! Hash::needsRehash(self::DUMMY_HASH)) {
            return self::DUMMY_HASH;
        }
        $key = 'auth:dummy-hash:'.hash('sha256', json_encode([config('hashing.driver'), config('hashing.bcrypt'), config('hashing.argon')]));

        return Cache::rememberForever($key, fn () => Hash::make(Str::random(40)));
    }

    public function me(Request $request): JsonResponse
    {
        $user = $request->user();

        return response()->json([
            'id' => $user->id,
            'name' => $user->name,
            'email' => $user->email,
            'role' => $user->role->value,
            'customerId' => $user->customer_id,
            'totpEnabled' => $user->totp_secret !== null,
            // The user has a temporary password. The API refuses all other work until the user
            // sets a new password (EnsureActive).
            'mustChangePassword' => (bool) $user->must_change_password,
        ]);
    }

    public function tokens(Request $request): JsonResponse
    {
        return response()->json(['data' => $request->user()->tokens()->latest()->get()->map(fn ($t) => [
            'id' => $t->id,
            'name' => $t->name,
            'scopes' => $t->abilities,
            'createdAt' => $t->created_at?->toIso8601String(),
            'expiresAt' => $t->expires_at?->toIso8601String(),
            'lastUsedAt' => $t->last_used_at?->toIso8601String(),
        ])]);
    }

    /**
     * CF-02: a new token can have only the scopes of the token that makes it. The user must
     * also give their password, and their TOTP code when TOTP is on.
     */
    public function createToken(Request $request): JsonResponse
    {
        $data = $request->validate([
            'name' => ['required', 'string', 'max:100'],
            'scopes' => ['required', 'array', 'min:1'],
            'scopes.*' => ['distinct', Rule::in(config('cloudfactory.scopes'))],
            // FR-104: each token has an expiry date.
            'expiresInDays' => ['required', 'integer', 'min:1', 'max:365'],
            'password' => ['required', 'string'],
            'totp' => ['nullable', 'digits:6'],
        ]);
        $user = $request->user();
        $current = $user->currentAccessToken();
        $wider = array_values(array_filter($data['scopes'], fn (string $scope) => ! self::tokenHas($current, $scope)));
        if ($wider) {
            return response()->json(['message' => 'A new token cannot have scopes that this token does not have: '.implode(', ', $wider).'.'], 403);
        }
        if (! Hash::check($data['password'], $user->password)) {
            throw ValidationException::withMessages(['password' => 'The password is not correct.']);
        }
        if (! Totp::consume($user, $data['totp'] ?? '')) {
            throw ValidationException::withMessages(['totp' => 'The code is not correct.']);
        }
        $token = $user->createToken($data['name'], $data['scopes'], now()->addDays($data['expiresInDays']));
        Audit::record($request, 'Created API token', $data['name']);

        return response()->json([
            'id' => $token->accessToken->id,
            'token' => $token->plainTextToken,
            'scopes' => $data['scopes'],
            'expiresAt' => $token->accessToken->expires_at?->toIso8601String(),
        ], 201);
    }

    /** A write scope also gives the read scope of the same area (EnsureScope). */
    private static function tokenHas(mixed $token, string $scope): bool
    {
        if (! $token || ! method_exists($token, 'can')) {
            return false;
        }

        return $token->can($scope) || (str_ends_with($scope, ':read') && $token->can(substr($scope, 0, -5).':write'));
    }

    public function revokeToken(Request $request, int $id): JsonResponse
    {
        $token = $request->user()->tokens()->findOrFail($id);
        $token->delete();
        Audit::record($request, 'Revoked API token', $token->name);

        return response()->json(null, 204);
    }
}
