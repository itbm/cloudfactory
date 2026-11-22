<?php

namespace App\Http\Controllers\Api;

use App\Http\Controllers\Controller;
use App\Models\User;
use App\Services\Audit;
use App\Services\LoginThrottle;
use App\Services\Notifications\Notifier;
use App\Services\Notifications\Senders;
use Illuminate\Auth\Passwords\TokenRepositoryInterface;
use Illuminate\Http\JsonResponse;
use Illuminate\Http\Request;
use Illuminate\Support\Facades\Hash;
use Illuminate\Support\Facades\Password as Passwords;
use Illuminate\Validation\Rules\Password;
use Illuminate\Validation\ValidationException;

/**
 * A user who does not know their password asks for an email with a link. The link sets a new
 * password. It does not turn off TOTP, so the user still needs the code to sign in.
 *
 * The answer is the same for each email, so it does not show which emails have a user. The
 * panel keeps only a hash of the code of the link, for 60 minutes (config auth.passwords).
 */
class PasswordResetController extends Controller
{
    public function request(Request $request): JsonResponse
    {
        $data = $request->validate(['email' => ['required', 'string', 'email', 'max:255']]);
        $email = User::normalEmail($data['email']);
        // Nobody signs in as the user of a plugin.
        $user = User::where('email', $email)->whereNull('plugin_id')->first();
        $tokens = self::tokens();
        if (! $user) {
            // CF-14: the same time as for a real user. A new code is a bcrypt hash.
            Hash::check($email, AuthController::dummyHash());

            return response()->json(['message' => self::message()], 202);
        }
        // One email in each minute for one user (config auth.passwords.users.throttle). A
        // person who knows the email cannot fill the mailbox of the user.
        if ($tokens->recentlyCreatedToken($user)) {
            return response()->json(['message' => self::message()], 202);
        }
        // A new code replaces the code of an earlier email.
        $token = $tokens->create($user);
        // The address of the brand of the user, not of the request: a request with a
        // different Host header cannot send the link to a different site. The code is after
        // "#", so the browser does not send it to the server, and no access log has it.
        $url = Senders::brand(Senders::brandOwner($user))['url'].'/reset-password#'.http_build_query(['email' => $user->email, 'token' => $token], '', '&', PHP_QUERY_RFC3986);
        app(Notifier::class)->passwordReset($user, $url, self::minutes(), (string) $request->ip());
        Audit::record($request, 'Sent a password reset email', $user->email, $user->customer_id);

        return response()->json(['message' => self::message()], 202);
    }

    /** Sets a new password with the code from the email. All sessions and API tokens stop. */
    public function reset(Request $request): JsonResponse
    {
        $data = $request->validate([
            'email' => ['required', 'string', 'email', 'max:255'],
            'token' => ['required', 'string', 'max:200'],
            'password' => ['required', 'string', 'confirmed', Password::min(12)],
        ]);
        $email = User::normalEmail($data['email']);
        $user = User::where('email', $email)->whereNull('plugin_id')->first();
        $tokens = self::tokens();
        if (! $user || ! $tokens->exists($user, $data['token'])) {
            if (! $user) {
                Hash::check($data['token'], AuthController::dummyHash());
            }
            throw ValidationException::withMessages(['token' => 'This link does not work. It works one time only, for '.self::minutes().' minutes. Ask for a new link.']);
        }
        $user->forceFill(['password' => $data['password'], 'must_change_password' => false, 'temporary_password_at' => null])->save();
        $tokens->delete($user);
        // The same as a password change: a person who knew the old password could have a
        // session or make a token.
        $user->tokens()->delete();
        // The user can sign in at once with the new password from this network.
        LoginThrottle::unlock($email, (string) $request->ip());
        $request->setUserResolver(fn () => $user);
        Audit::record($request, 'Reset password with an email link', $user->email, $user->customer_id);

        return response()->json(null, 204);
    }

    private static function message(): string
    {
        return 'If a user has this email, the panel sends a link to it. The link works for '.self::minutes().' minutes. Look in your spam folder also.';
    }

    /** The minutes for which a link works. */
    public static function minutes(): int
    {
        return (int) config('auth.passwords.users.expire', 60);
    }

    private static function tokens(): TokenRepositoryInterface
    {
        return Passwords::broker()->getRepository();
    }
}
