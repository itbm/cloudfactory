<?php

namespace App\Http\Controllers\Api;

use App\Http\Controllers\Controller;
use App\Models\IdentityProvider;
use App\Models\User;
use App\Models\UserIdentity;
use App\Services\Audit;
use App\Services\LoginThrottle;
use App\Services\Sso\Saml;
use App\Services\Sso\SingleSignOn;
use App\Services\Sso\SsoException;
use App\Services\Totp;
use Illuminate\Http\JsonResponse;
use Illuminate\Http\RedirectResponse;
use Illuminate\Http\Request;
use Illuminate\Http\Response;
use Illuminate\Support\Facades\Log;
use Illuminate\Validation\ValidationException;

/**
 * Single sign-on with the identity providers that an administrator added
 * (IdentityProviderController). The browser goes to these routes: they answer with a redirect,
 * not with JSON. Refer to SingleSignOn for the rules.
 */
class SsoController extends Controller
{
    public function __construct(private SingleSignOn $sso) {}

    /**
     * Sends the browser to the provider. With "connect", the sign-in connects the account of
     * the provider to the user of that ticket (Settings).
     */
    public function start(Request $request, string $id): RedirectResponse
    {
        $base = SingleSignOn::baseUrl($request);
        abort_if($base === null, 409, 'Single sign-on works only on the domain of the panel. Open the panel at its own domain.');
        $provider = IdentityProvider::findOrFail($id);
        $connect = null;
        if ($request->filled('connect')) {
            $connect = $this->sso->takeConnectTicket((string) $request->query('connect'), $provider);
            if (! $connect) {
                return redirect()->away("{$base}/settings#sso-error=expired");
            }
        }
        $page = $connect ? 'settings' : 'login';
        if (! $provider->enabled) {
            return redirect()->away("{$base}/{$page}#sso-error=disabled");
        }
        try {
            $start = $this->sso->start($provider, $base, $connect);
        } catch (SsoException $e) {
            Log::warning("Single sign-on with provider {$provider->id} cannot start: {$e->getMessage()}");

            return redirect()->away("{$base}/{$page}#sso-error=unavailable");
        }

        return redirect()->away($start['url'])->withCookie($start['cookie']);
    }

    /** OpenID Connect: the provider sends the browser back with a code. */
    public function callback(Request $request): RedirectResponse
    {
        return $this->finish($request, IdentityProvider::OIDC, (string) $request->query('state'), $request->query());
    }

    /** SAML: the assertion consumer service. The provider posts the answer. */
    public function acs(Request $request): RedirectResponse
    {
        return $this->finish($request, IdentityProvider::SAML, (string) $request->input('RelayState'), $request->only('SAMLResponse'));
    }

    /** SAML: the metadata of the panel as a service provider, for all the SAML providers. */
    public function metadata(Request $request, Saml $saml): Response
    {
        return response($saml->metadata(SingleSignOn::panelUrl($request)), 200, ['Content-Type' => 'application/samlmetadata+xml']);
    }

    /**
     * Changes the ticket of a sign-in into a session token. When TOTP is on for the user, the
     * request must have a code, as for a sign-in with a password.
     */
    public function complete(Request $request): JsonResponse
    {
        $data = $request->validate([
            'ticket' => ['required', 'string', 'max:100'],
            'totp' => ['nullable', 'digits:6'],
            'deviceId' => ['nullable', 'string', 'max:100'],
        ]);
        $ticket = $this->sso->ticket($data['ticket']);
        $user = $ticket ? User::whereNull('plugin_id')->find($ticket['user']) : null;
        $provider = $ticket ? IdentityProvider::find($ticket['provider']) : null;
        if (! $user || ! $provider?->enabled) {
            throw ValidationException::withMessages(['ticket' => 'The sign-in is too old. Sign in again.']);
        }
        $ip = (string) $request->ip();
        if ($seconds = LoginThrottle::lockedFor($user->email, $ip)) {
            return response()->json(['message' => "Too many failed attempts. Try again in {$seconds} seconds."], 429)->header('Retry-After', (string) $seconds);
        }
        if ($user->totp_secret) {
            if (($data['totp'] ?? '') === '') {
                return response()->json([
                    'message' => 'Type the code from your authenticator app.',
                    'errors' => ['totp' => ['Type the code from your authenticator app.']],
                    'totpRequired' => true,
                ], 422);
            }
            if (! Totp::consume($user, $data['totp'])) {
                $this->sso->failedCode($data['ticket']);
                if (LoginThrottle::fail($user->email, $ip)) {
                    Audit::system('Locked sign-in after failed attempts', "{$user->email} from {$ip}");
                }
                throw ValidationException::withMessages(['totp' => 'The code is not correct. A code works one time only.']);
            }
            LoginThrottle::clear($user->email, $ip);
        }
        $this->sso->forgetTicket($data['ticket']);
        // The provider shows who the person is, so the answer can give the reason.
        if ($user->isSuspended()) {
            return response()->json(['message' => 'This account is suspended. Contact your provider.'], 403);
        }

        return AuthController::startSession($request, $user, $user->totp_secret ? 'Signed in with single sign-on and TOTP' : 'Signed in with single sign-on');
    }

    /** The providers that the user can use, and the accounts that the user connected. */
    public function identities(Request $request): JsonResponse
    {
        $user = $request->user();
        $identities = $user->identities()->get()->keyBy('identity_provider_id');

        return response()->json(['data' => IdentityProvider::where('enabled', true)->orderBy('name')->get()->map(function (IdentityProvider $p) use ($identities) {
            /** @var UserIdentity|null $identity */
            $identity = $identities->get($p->id);

            return [
                'providerId' => $p->id,
                'name' => $p->name,
                'protocol' => $p->protocol,
                'connected' => $identity !== null,
                'email' => $identity?->email,
                'connectedAt' => $identity?->created_at?->toIso8601String(),
                'lastUsedAt' => $identity?->last_used_at?->toIso8601String(),
            ];
        })]);
    }

    /**
     * The first step to connect an account: the user gives the password and, when TOTP is on,
     * a code. The answer has the address for the browser.
     */
    public function connect(Request $request, string $id): JsonResponse
    {
        $user = AuthController::sessionUser($request);
        abort_if(SingleSignOn::baseUrl($request) === null, 409, 'Single sign-on works only on the domain of the panel. Open the panel at its own domain.');
        $provider = IdentityProvider::where('enabled', true)->findOrFail($id);
        AuthController::confirmIdentity($request);

        return response()->json(['url' => "/api/v1/auth/sso/{$provider->id}?connect=".$this->sso->connectTicket($user, $provider)]);
    }

    /** Disconnects the account of a provider. The user gives the password and, when TOTP is on, a code. */
    public function disconnect(Request $request, string $id): JsonResponse
    {
        $user = AuthController::sessionUser($request);
        $identity = $user->identities()->with('provider')->where('identity_provider_id', $id)->firstOrFail();
        AuthController::confirmIdentity($request);
        $identity->delete();
        Audit::record($request, 'Disconnected single sign-on account', "{$identity->provider->name} ({$user->email})");

        return response()->json(null, 204);
    }

    /** @param array<string, mixed> $answer */
    private function finish(Request $request, string $protocol, string $state, array $answer): RedirectResponse
    {
        return redirect()->away($this->sso->finish($protocol, $request, $state, $answer))
            ->withCookie(cookie()->forget(SingleSignOn::COOKIE, '/api/v1/auth/sso'));
    }
}
