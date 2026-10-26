<?php

namespace App\Http\Controllers\Api;

use App\Http\Controllers\Controller;
use App\Models\User;
use App\Services\Audit;
use App\Services\LoginThrottle;
use App\Services\Totp;
use Illuminate\Http\JsonResponse;
use Illuminate\Http\Request;
use Illuminate\Support\Facades\Hash;
use Illuminate\Support\Str;
use Illuminate\Validation\Rule;
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
     */
    public function login(Request $request): JsonResponse
    {
        $data = $request->validate([
            'email' => ['required', 'email'],
            'password' => ['required', 'string'],
            'totp' => ['nullable', 'digits:6'],
        ]);
        if ($seconds = LoginThrottle::lockedFor($data['email'])) {
            return response()->json(['message' => "Too many failed attempts. Try again in {$seconds} seconds."], 429)
                ->header('Retry-After', (string) $seconds);
        }
        $user = User::where('email', $data['email'])->first();
        if (! Hash::check($data['password'], $user?->password ?? self::dummyHash()) || ! $user) {
            LoginThrottle::fail($data['email']);
            throw ValidationException::withMessages(['email' => 'The email or the password is not correct.']);
        }
        if (! Totp::consume($user, $data['totp'] ?? '')) {
            LoginThrottle::fail($data['email']);
            throw ValidationException::withMessages(['totp' => 'The code is not correct.']);
        }
        if ($user->isSuspended()) {
            return response()->json(['message' => 'This account is suspended. Contact your provider.'], 403);
        }
        LoginThrottle::clear($data['email']);

        $token = $user->createToken('Web session', ['*'], now()->addHours(12));
        $request->setUserResolver(fn () => $user);
        Audit::record($request, 'Signed in with password and TOTP', $user->email);

        return response()->json(['token' => $token->plainTextToken, 'expiresAt' => $token->accessToken->expires_at?->toIso8601String()]);
    }

    /** A hash of a random password, with the same cost as the real hashes (CF-14). */
    private static function dummyHash(): string
    {
        static $hash = null;

        return $hash ??= Hash::make(Str::random(40));
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
