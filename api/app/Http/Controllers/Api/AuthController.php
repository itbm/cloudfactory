<?php

namespace App\Http\Controllers\Api;

use App\Http\Controllers\Controller;
use App\Models\User;
use App\Services\Audit;
use App\Services\Totp;
use Illuminate\Http\JsonResponse;
use Illuminate\Http\Request;
use Illuminate\Support\Facades\Hash;
use Illuminate\Validation\Rule;
use Illuminate\Validation\ValidationException;

/** FR-103 and FR-104: sign in, and API tokens with scopes and an expiry date. */
class AuthController extends Controller
{
    /** Sign in with an email, a password and a TOTP code. The answer holds a session token. */
    public function login(Request $request): JsonResponse
    {
        $data = $request->validate([
            'email' => ['required', 'email'],
            'password' => ['required', 'string'],
            'totp' => ['nullable', 'digits:6'],
        ]);
        $user = User::where('email', $data['email'])->first();
        if (! $user || ! Hash::check($data['password'], $user->password)) {
            throw ValidationException::withMessages(['email' => 'The email or the password is not correct.']);
        }
        if ($user->totp_secret && ! Totp::verify($user->totp_secret, $data['totp'] ?? '')) {
            throw ValidationException::withMessages(['totp' => 'The code is not correct.']);
        }

        $token = $user->createToken('Web session', ['*'], now()->addHours(12));
        $request->setUserResolver(fn () => $user);
        Audit::record($request, 'Signed in with password and TOTP', $user->email);

        return response()->json(['token' => $token->plainTextToken, 'expiresAt' => $token->accessToken->expires_at?->toIso8601String()]);
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

    public function createToken(Request $request): JsonResponse
    {
        $data = $request->validate([
            'name' => ['required', 'string', 'max:100'],
            'scopes' => ['required', 'array', 'min:1'],
            'scopes.*' => [Rule::in(config('cloudfactory.scopes'))],
            // FR-104: each token has an expiry date.
            'expiresInDays' => ['required', 'integer', 'min:1', 'max:365'],
        ]);
        $token = $request->user()->createToken($data['name'], $data['scopes'], now()->addDays($data['expiresInDays']));
        Audit::record($request, 'Created API token', $data['name']);

        return response()->json([
            'id' => $token->accessToken->id,
            'token' => $token->plainTextToken,
            'scopes' => $data['scopes'],
            'expiresAt' => $token->accessToken->expires_at?->toIso8601String(),
        ], 201);
    }

    public function revokeToken(Request $request, int $id): JsonResponse
    {
        $token = $request->user()->tokens()->findOrFail($id);
        $token->delete();
        Audit::record($request, 'Revoked API token', $token->name);

        return response()->json(null, 204);
    }
}
