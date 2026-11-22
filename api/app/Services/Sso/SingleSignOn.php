<?php

namespace App\Services\Sso;

use App\Models\IdentityProvider;
use App\Models\User;
use App\Models\UserIdentity;
use App\Services\Audit;
use Illuminate\Http\Request;
use Illuminate\Support\Facades\Cache;
use Illuminate\Support\Facades\Log;
use Illuminate\Support\Str;
use Symfony\Component\HttpFoundation\Cookie;
use Throwable;

/**
 * Single sign-on with OpenID Connect and SAML identity providers.
 *
 * - The browser goes to the provider and comes back to the panel API. The panel keeps the
 *   state of each sign-in for 10 minutes, and uses it one time only. A cookie binds the state
 *   to the browser that started the sign-in, so that a person cannot sign a different browser
 *   in to an account of that person.
 * - The panel signs in only a user who connected the account of the provider in Settings, or
 *   a user with the same email when the administrator lets the provider connect users by
 *   email.
 * - After the sign-in, the browser gets a ticket in the part of the address after "#". The web
 *   panel changes the ticket into a session token, and gives a TOTP code when TOTP is on for
 *   the user.
 * - Single sign-on works only on the panel domain, because the provider sends the browser back
 *   only to the addresses that the administrator registered.
 */
class SingleSignOn
{
    public const COOKIE = 'cf_sso';

    public const STATE_MINUTES = 10;

    public const TICKET_MINUTES = 5;

    public const TICKET_ATTEMPTS = 5;

    public function __construct(private Oidc $oidc, private Saml $saml) {}

    /** The address of the panel for single sign-on on the host of the request, or null. */
    public static function baseUrl(Request $request): ?string
    {
        $host = strtolower(rtrim($request->getHost(), '.'));
        $panel = strtolower((string) config('cloudfactory.panel_domain'));
        if ($panel !== '' && $host === $panel) {
            return "https://{$host}";
        }
        // In development, the web panel runs on localhost with a port.
        if ($host === 'localhost' && app()->environment(['local', 'testing', 'demo', 'development'])) {
            return $request->getSchemeAndHttpHost();
        }

        return null;
    }

    /** The address of the panel for the settings of a provider: the panel domain. */
    public static function panelUrl(Request $request): string
    {
        return self::baseUrl($request) ?? 'https://'.strtolower((string) config('cloudfactory.panel_domain'));
    }

    /**
     * The address where an OpenID Connect provider sends the browser back. It is the same for
     * all the providers: the state of the sign-in identifies the provider.
     */
    public static function redirectUri(string $base): string
    {
        return "{$base}/api/v1/auth/sso/oidc/callback";
    }

    /**
     * The values that the administrator gives to a provider. They are the same for all the
     * providers.
     *
     * @return array{redirectUri: string, entityId: string, acsUrl: string}
     */
    public static function serviceProvider(string $base): array
    {
        return ['redirectUri' => self::redirectUri($base), 'entityId' => Saml::entityId($base), 'acsUrl' => Saml::acsUrl($base)];
    }

    /**
     * Starts a sign-in. With a user, the sign-in connects the account of the provider to that
     * user. It gives the address of the provider and the cookie for the browser.
     *
     * @return array{url: string, cookie: Cookie}
     */
    public function start(IdentityProvider $provider, string $base, ?User $connect = null): array
    {
        $state = Str::random(43);
        $browser = Str::random(43);
        $data = ['provider' => $provider->id, 'base' => $base, 'browser' => hash('sha256', $browser), 'connect' => $connect?->id];
        try {
            if ($provider->protocol === IdentityProvider::OIDC) {
                $data += ['nonce' => Str::random(43), 'verifier' => Str::random(64)];
                $url = $this->oidc->authorizationUrl($provider, self::redirectUri($base), $state, $data['nonce'], $data['verifier']);
            } else {
                [$url, $data['requestId']] = $this->saml->loginUrl($provider, $base, $state);
            }
        } catch (SsoException $e) {
            throw $e;
        } catch (Throwable $e) {
            throw new SsoException('failed', $e->getMessage());
        }
        Cache::put(self::key('state', $state), $data, now()->addMinutes(self::STATE_MINUTES));
        $secure = str_starts_with($base, 'https://');
        // The SAML provider posts the answer from a different site: the browser sends the
        // cookie only with SameSite=None, which needs HTTPS.
        $cookie = Cookie::create(self::COOKIE, $browser, now()->addMinutes(self::STATE_MINUTES), '/api/v1/auth/sso', null, $secure, true, false, $secure ? Cookie::SAMESITE_NONE : Cookie::SAMESITE_LAX);

        return ['url' => $url, 'cookie' => $cookie];
    }

    /**
     * Ends a sign-in: examines the answer of the provider and finds the user. It gives the
     * address for the browser: the web panel with a ticket, or Settings after a connection.
     *
     * @param  string  $protocol  the protocol of the address that the provider sent the browser to
     * @param  array<string, mixed>  $answer  the query (OpenID Connect) or the form (SAML) of the provider
     */
    public function finish(string $protocol, Request $request, string $state, array $answer): string
    {
        $data = $state !== '' ? Cache::pull(self::key('state', $state)) : null;
        $connecting = is_array($data) && $data['connect'] !== null;
        $provider = is_array($data) ? IdentityProvider::find($data['provider']) : null;
        try {
            if (! $provider || $provider->protocol !== $protocol || ! hash_equals($data['browser'], hash('sha256', (string) $request->cookie(self::COOKIE)))) {
                throw new SsoException('expired', 'The state of the sign-in is not known, too old or for a different browser.');
            }
            if (! $provider->enabled) {
                throw new SsoException('disabled');
            }
            $account = $this->account($provider, $data, $answer);
            if ($connecting) {
                $this->connect($provider, User::findOrFail($data['connect']), $account, $request);

                return "{$data['base']}/settings#sso-connected={$provider->id}";
            }
            $identity = $this->identity($provider, $account, $request);
            $identity->forceFill(['email' => $account->email, 'last_used_at' => now()])->save();
            $ticket = Str::random(43);
            Cache::put(self::key('ticket', $ticket), ['user' => $identity->user_id, 'provider' => $provider->id, 'attempts' => 0], now()->addMinutes(self::TICKET_MINUTES));

            return "{$data['base']}/sso#ticket={$ticket}";
        } catch (SsoException $e) {
            Log::info('A single sign-on with provider '.($provider->id ?? 'unknown')." did not work ({$e->reason}): {$e->getMessage()}");
            $base = is_array($data) ? $data['base'] : (self::baseUrl($request) ?? '');

            return $base.($connecting ? '/settings' : '/login')."#sso-error={$e->reason}";
        }
    }

    /**
     * The ticket of a sign-in, or null when it is not known or too old.
     *
     * @return array{user: int, provider: string, attempts: int}|null
     */
    public function ticket(string $ticket): ?array
    {
        $data = Cache::get(self::key('ticket', $ticket));

        return is_array($data) ? $data : null;
    }

    /** Counts a wrong TOTP code. After 5 wrong codes, the ticket stops. */
    public function failedCode(string $ticket): void
    {
        $data = $this->ticket($ticket);
        if ($data && ++$data['attempts'] < self::TICKET_ATTEMPTS) {
            Cache::put(self::key('ticket', $ticket), $data, now()->addMinutes(self::TICKET_MINUTES));
        } else {
            $this->forgetTicket($ticket);
        }
    }

    public function forgetTicket(string $ticket): void
    {
        Cache::forget(self::key('ticket', $ticket));
    }

    /**
     * A ticket of the user to connect an account in a browser navigation, which has no
     * session token. It works one time only, for 5 minutes.
     */
    public function connectTicket(User $user, IdentityProvider $provider): string
    {
        $ticket = Str::random(43);
        Cache::put(self::key('connect', $ticket), ['user' => $user->id, 'provider' => $provider->id], now()->addMinutes(self::TICKET_MINUTES));

        return $ticket;
    }

    /** The user of a ticket to connect an account, or null. */
    public function takeConnectTicket(string $ticket, IdentityProvider $provider): ?User
    {
        $data = Cache::pull(self::key('connect', $ticket));

        return is_array($data) && $data['provider'] === $provider->id ? User::find($data['user']) : null;
    }

    /** @param array<string, mixed> $data @param array<string, mixed> $answer */
    private function account(IdentityProvider $provider, array $data, array $answer): Account
    {
        if ($provider->protocol === IdentityProvider::OIDC) {
            if (isset($answer['error'])) {
                throw new SsoException('cancelled', 'The provider gave the error '.substr((string) $answer['error'], 0, 100).'.');
            }
            if (! is_string($answer['code'] ?? null) || $answer['code'] === '') {
                throw new SsoException('failed', 'The provider gave no code.');
            }

            return $this->oidc->exchange($provider, $answer['code'], self::redirectUri($data['base']), $data['verifier'], $data['nonce']);
        }
        if (! is_string($answer['SAMLResponse'] ?? null)) {
            throw new SsoException('failed', 'The provider gave no SAMLResponse.');
        }

        return $this->saml->verify($provider, $data['base'], $answer['SAMLResponse'], $data['requestId']);
    }

    /** The connection of the account to a user, from Settings. */
    private function connect(IdentityProvider $provider, User $user, Account $account, Request $request): void
    {
        $other = UserIdentity::where('identity_provider_id', $provider->id)->where('subject', $account->subject)->first();
        if ($other && $other->user_id !== $user->id) {
            throw new SsoException('taken', "The account {$account->subject} is connected to user {$other->user_id}.");
        }
        UserIdentity::updateOrCreate(
            ['user_id' => $user->id, 'identity_provider_id' => $provider->id],
            ['subject' => $account->subject, 'email' => $account->email],
        );
        $request->setUserResolver(fn () => $user);
        Audit::record($request, 'Connected single sign-on account', "{$provider->name} ({$user->email})");
    }

    /**
     * The connection of the account. When there is none, the provider can connect the user
     * with the same email: the provider must say that the email is verified, and the domain
     * of the email must be in the list of the provider.
     */
    private function identity(IdentityProvider $provider, Account $account, Request $request): UserIdentity
    {
        $identity = UserIdentity::with('user')->where('identity_provider_id', $provider->id)->where('subject', $account->subject)->first();
        if ($identity?->user) {
            return $identity;
        }
        if (! $provider->link_by_email || ! $account->emailVerified || ! $provider->acceptsEmail($account->email)) {
            throw new SsoException('no-account', "No user is connected to the account {$account->subject}.");
        }
        $user = User::where('email', User::normalEmail((string) $account->email))->whereNull('plugin_id')->first();
        // A user can connect one account of each provider.
        if (! $user || $user->identities()->where('identity_provider_id', $provider->id)->exists()) {
            throw new SsoException('no-account', "No user can connect the account {$account->subject} by email.");
        }
        $identity = $user->identities()->create(['identity_provider_id' => $provider->id, 'subject' => $account->subject, 'email' => $account->email]);
        $request->setUserResolver(fn () => $user);
        Audit::record($request, 'Connected single sign-on account by email', "{$provider->name} ({$user->email})");

        return $identity->setRelation('user', $user);
    }

    private static function key(string $kind, string $value): string
    {
        return "sso-{$kind}:".hash('sha256', $value);
    }
}
