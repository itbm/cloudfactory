<?php

namespace Tests\Feature;

use App\Enums\Role;
use App\Http\Controllers\Api\AuthController;
use App\Models\IdentityProvider;
use App\Models\User;
use App\Models\UserIdentity;
use App\Services\Sso\SingleSignOn;
use App\Services\Totp;
use Firebase\JWT\JWT;
use Illuminate\Support\Facades\Http;
use Illuminate\Testing\TestResponse;
use OneLogin\Saml2\Constants;
use OneLogin\Saml2\Utils;
use Tests\TestCase;

/** Single sign-on with OpenID Connect and SAML identity providers. */
class SingleSignOnTest extends TestCase
{
    private const HOST = 'panel.example.com';

    private const BASE = 'https://panel.example.com';

    private const ISSUER = 'https://login.example.org';

    private const IDP_ENTITY = 'https://idp.example.org/saml';

    /** @var array{key: string, public: array<string, string>, cert: string}|null */
    private static ?array $rsa = null;

    /** The claims of the next ID token of the fake provider. The test changes them. */
    private array $claims = [];

    /** The key that signs the next ID token. */
    private ?string $signingKey = null;

    /** Makes the next ID token. Null: a token that the key of the provider signs. */
    private ?\Closure $idToken = null;

    protected function setUp(): void
    {
        parent::setUp();
        config(['cloudfactory.panel_domain' => self::HOST, 'cloudfactory.require_totp' => false]);
        self::$rsa ??= self::rsa();
        $this->fakeOidcProvider();
    }

    /** An RSA key with a certificate for the fake providers. */
    private static function rsa(): array
    {
        $key = openssl_pkey_new(['private_key_bits' => 2048, 'private_key_type' => OPENSSL_KEYTYPE_RSA]);
        openssl_pkey_export($key, $pem);
        $csr = openssl_csr_new(['commonName' => 'idp.example.org'], $key, ['digest_alg' => 'sha256']);
        openssl_x509_export(openssl_csr_sign($csr, null, $key, 365, ['digest_alg' => 'sha256']), $cert);
        $details = openssl_pkey_get_details($key)['rsa'];
        $b64 = fn (string $v) => rtrim(strtr(base64_encode($v), '+/', '-_'), '=');

        return ['key' => $pem, 'public' => ['kty' => 'RSA', 'kid' => 'k1', 'use' => 'sig', 'n' => $b64($details['n']), 'e' => $b64($details['e'])], 'cert' => $cert];
    }

    /** The discovery document, the keys and the token endpoint of an OpenID Connect provider. */
    private function fakeOidcProvider(): void
    {
        Http::fake([
            self::ISSUER.'/.well-known/openid-configuration' => fn () => Http::response([
                'issuer' => self::ISSUER,
                'authorization_endpoint' => self::ISSUER.'/authorize',
                'token_endpoint' => self::ISSUER.'/token',
                'jwks_uri' => self::ISSUER.'/keys',
            ]),
            self::ISSUER.'/keys' => fn () => Http::response(['keys' => [self::$rsa['public']]]),
            self::ISSUER.'/token' => fn () => Http::response(['access_token' => 'at', 'token_type' => 'Bearer',
                'id_token' => $this->idToken ? ($this->idToken)() : JWT::encode($this->claims, $this->signingKey ?? self::$rsa['key'], 'RS256', 'k1')]),
        ]);
    }

    private function url(string $path): string
    {
        return self::BASE.'/api/v1/'.$path;
    }

    private function sessionOf(User $user): string
    {
        return $user->createToken(AuthController::SESSION, ['*'], now()->addHours(12))->plainTextToken;
    }

    private function oidcProvider(array $attributes = []): IdentityProvider
    {
        return IdentityProvider::create($attributes + [
            'name' => 'Example SSO', 'protocol' => IdentityProvider::OIDC, 'issuer' => self::ISSUER, 'client_id' => 'panel', 'client_secret' => 'secret',
            'link_by_email' => false, 'email_domains' => [],
        ]);
    }

    private function samlProvider(array $attributes = []): IdentityProvider
    {
        return IdentityProvider::create($attributes + [
            'name' => 'Example SAML', 'protocol' => IdentityProvider::SAML, 'entity_id' => self::IDP_ENTITY, 'sso_url' => 'https://idp.example.org/sso',
            'certificate' => self::$rsa['cert'], 'link_by_email' => false, 'email_domains' => [],
        ]);
    }

    /**
     * Starts a sign-in, as the browser does, and gives the query of the address of the provider
     * and the cookie.
     *
     * @return array{0: array<string, string>, 1: string}
     */
    private function startAt(IdentityProvider $provider, string $connect = ''): array
    {
        $start = $this->get($this->url("auth/sso/{$provider->id}").($connect !== '' ? "?connect={$connect}" : ''))->assertRedirect();
        parse_str((string) parse_url($start->headers->get('Location'), PHP_URL_QUERY), $query);
        $this->assertNotEmpty($query, $start->headers->get('Location'));

        return [$query, $start->getCookie(SingleSignOn::COOKIE, false)->getValue()];
    }

    /** The OpenID Connect provider signs the person in with the claims, and sends the browser back. */
    private function oidcSignIn(IdentityProvider $provider, array $claims, string $connect = ''): TestResponse
    {
        [$query, $cookie] = $this->startAt($provider, $connect);
        $this->claims = $claims + ['iss' => self::ISSUER, 'aud' => 'panel', 'exp' => time() + 300, 'iat' => time(), 'nonce' => $query['nonce']];

        return $this->withUnencryptedCookie(SingleSignOn::COOKIE, $cookie)->get($this->url("auth/sso/oidc/callback?code=abc&state={$query['state']}"));
    }

    /** The fragment of the address of a redirect. */
    private static function fragment(TestResponse $response): string
    {
        return (string) parse_url($response->headers->get('Location'), PHP_URL_FRAGMENT);
    }

    /** Changes the ticket of a redirect into a session token. */
    private function complete(TestResponse $redirect, array $more = []): TestResponse
    {
        $this->assertStringStartsWith(self::BASE.'/sso#ticket=', $redirect->headers->get('Location'));
        $this->app['auth']->forgetGuards();

        return $this->postJson($this->url('auth/sso/complete'), ['ticket' => substr(self::fragment($redirect), 7)] + $more);
    }

    public function test_an_administrator_adds_a_provider_and_a_user_connects_an_account_and_signs_in_with_it(): void
    {
        $admin = $this->user(Role::Administrator);
        $session = $this->sessionOf($admin);
        $add = ['name' => 'Example SSO', 'protocol' => 'oidc', 'issuer' => self::ISSUER, 'clientId' => 'panel', 'clientSecret' => 'secret'];
        $this->withToken($session)->postJson($this->url('identity-providers'), $add)->assertUnprocessable()->assertJsonValidationErrors('password');
        $provider = $this->withToken($session)->postJson($this->url('identity-providers'), $add + ['password' => 'password'])->assertCreated()
            ->assertJson(['name' => 'Example SSO', 'enabled' => true, 'hasClientSecret' => true])
            ->assertJsonMissingPath('clientSecret')->json();
        $this->assertSame('secret', IdentityProvider::sole()->client_secret);
        $this->assertNotSame('secret', IdentityProvider::query()->toBase()->value('client_secret'));
        $this->assertDatabaseHas('audit_entries', ['action' => 'Added single sign-on provider', 'target' => 'Example SSO']);

        // The sign-in page shows the provider on the panel domain only.
        $this->app['auth']->forgetGuards();
        $this->getJson($this->url('auth/methods'))->assertJsonPath('providers', [['id' => $provider['id'], 'name' => 'Example SSO']]);
        $this->getJson('https://other.example/api/v1/auth/methods')->assertJsonPath('providers', []);
        $this->app['auth']->forgetGuards();

        // The user connects the account in Settings, with the password.
        $user = $this->user(Role::Owner, $this->customer());
        $userSession = $this->sessionOf($user);
        $this->withToken($userSession)->postJson($this->url("me/identities/{$provider['id']}"))->assertUnprocessable()->assertJsonValidationErrors('password');
        $url = $this->withToken($userSession)->postJson($this->url("me/identities/{$provider['id']}"), ['password' => 'password'])->assertOk()->json('url');
        parse_str((string) parse_url($url, PHP_URL_QUERY), $query);
        $this->app['auth']->forgetGuards();
        $connected = $this->oidcSignIn(IdentityProvider::sole(), ['sub' => 'person-1', 'email' => 'person@example.org', 'email_verified' => true], $query['connect']);
        $connected->assertRedirect(self::BASE."/settings#sso-connected={$provider['id']}");
        $this->assertDatabaseHas('user_identities', ['user_id' => $user->id, 'subject' => 'person-1', 'email' => 'person@example.org']);
        $this->assertDatabaseHas('audit_entries', ['action' => 'Connected single sign-on account', 'target' => "Example SSO ({$user->email})"]);
        // A ticket to connect works one time only.
        $this->get($this->url("auth/sso/{$provider['id']}?connect={$query['connect']}"))->assertRedirect(self::BASE.'/settings#sso-error=expired');
        $this->withToken($userSession)->getJson($this->url('me/identities'))->assertOk()
            ->assertJsonPath('data.0.connected', true)->assertJsonPath('data.0.email', 'person@example.org');
        $this->app['auth']->forgetGuards();

        // The user signs in with the provider, with no password.
        $token = $this->complete($this->oidcSignIn(IdentityProvider::sole(), ['sub' => 'person-1']), ['deviceId' => 'device-1234567890'])
            ->assertOk()->assertJsonStructure(['token', 'expiresAt'])->json('token');
        $this->assertDatabaseHas('audit_entries', ['action' => 'Signed in with single sign-on', 'user_id' => $user->id]);
        $this->assertNotNull(UserIdentity::sole()->last_used_at);
        $this->app['auth']->forgetGuards();
        $this->withToken($token)->getJson($this->url('me'))->assertOk()->assertJsonPath('email', $user->email);

        // The token request has the code and the PKCE verifier, and the secret in HTTP Basic.
        Http::assertSent(fn ($request) => $request->url() === self::ISSUER.'/token' && $request['code'] === 'abc'
            && strlen((string) $request['code_verifier']) === 64 && $request->hasHeader('Authorization', 'Basic '.base64_encode('panel:secret')));
    }

    public function test_the_authorization_request_has_pkce_a_nonce_and_the_exact_redirect_address(): void
    {
        $provider = $this->oidcProvider();
        [$query] = $this->startAt($provider);
        $this->assertSame('code', $query['response_type']);
        $this->assertSame('panel', $query['client_id']);
        $this->assertSame('openid email profile', $query['scope']);
        $this->assertSame(self::BASE.'/api/v1/auth/sso/oidc/callback', $query['redirect_uri']);
        $this->assertSame('S256', $query['code_challenge_method']);
        $this->assertNotEmpty($query['nonce']);

        // Single sign-on does not work on a different domain, and a turned-off provider does not start.
        $this->get("https://other.example/api/v1/auth/sso/{$provider->id}")->assertStatus(409);
        $provider->update(['enabled' => false]);
        $this->get($this->url("auth/sso/{$provider->id}"))->assertRedirect(self::BASE.'/login#sso-error=disabled');
        $this->getJson($this->url('auth/methods'))->assertJsonPath('providers', []);
    }

    public function test_the_state_works_one_time_only_and_only_in_the_browser_that_started_the_sign_in(): void
    {
        $provider = $this->oidcProvider();
        UserIdentity::create(['user_id' => $this->user(Role::Owner, $this->customer())->id, 'identity_provider_id' => $provider->id, 'subject' => 'person-1']);

        // A different browser (no cookie) cannot end the sign-in.
        [$query] = $this->startAt($provider);
        $this->claims = ['sub' => 'person-1', 'iss' => self::ISSUER, 'aud' => 'panel', 'exp' => time() + 300, 'nonce' => $query['nonce']];
        $this->get($this->url("auth/sso/oidc/callback?code=abc&state={$query['state']}"))->assertRedirect(self::BASE.'/login#sso-error=expired');

        // The same answer a second time.
        [$query, $cookie] = $this->startAt($provider);
        $this->claims['nonce'] = $query['nonce'];
        $callback = $this->url("auth/sso/oidc/callback?code=abc&state={$query['state']}");
        $this->assertStringStartsWith(self::BASE.'/sso#ticket=', $this->withUnencryptedCookie(SingleSignOn::COOKIE, $cookie)->get($callback)->headers->get('Location'));
        $this->withUnencryptedCookie(SingleSignOn::COOKIE, $cookie)->get($callback)->assertRedirect(self::BASE.'/login#sso-error=expired');

        // The provider did not sign the person in.
        [$query, $cookie] = $this->startAt($provider);
        $this->withUnencryptedCookie(SingleSignOn::COOKIE, $cookie)->get($this->url("auth/sso/oidc/callback?error=access_denied&state={$query['state']}"))
            ->assertRedirect(self::BASE.'/login#sso-error=cancelled');

        // A ticket works one time only.
        $redirect = $this->oidcSignIn($provider, ['sub' => 'person-1']);
        $this->complete($redirect)->assertOk();
        $this->complete($redirect)->assertUnprocessable()->assertJsonValidationErrors('ticket');
    }

    public function test_the_panel_refuses_an_id_token_that_is_not_valid(): void
    {
        $provider = $this->oidcProvider();
        UserIdentity::create(['user_id' => $this->user(Role::Owner, $this->customer())->id, 'identity_provider_id' => $provider->id, 'subject' => 'person-1']);
        $failed = self::BASE.'/login#sso-error=failed';

        $this->oidcSignIn($provider, ['sub' => 'person-1', 'nonce' => 'other'])->assertRedirect($failed);
        $this->oidcSignIn($provider, ['sub' => 'person-1', 'aud' => 'other-client'])->assertRedirect($failed);
        $this->oidcSignIn($provider, ['sub' => 'person-1', 'aud' => ['panel', 'other-client']])->assertRedirect($failed);
        $this->oidcSignIn($provider, ['sub' => 'person-1', 'iss' => 'https://evil.example'])->assertRedirect($failed);
        $this->oidcSignIn($provider, ['sub' => 'person-1', 'exp' => time() - 3600])->assertRedirect($failed);
        // A key that is not a key of the provider.
        $other = openssl_pkey_new(['private_key_bits' => 2048, 'private_key_type' => OPENSSL_KEYTYPE_RSA]);
        openssl_pkey_export($other, $this->signingKey);
        $this->oidcSignIn($provider, ['sub' => 'person-1'])->assertRedirect($failed);
        $this->signingKey = null;
        // HMAC with the client secret, and no signature.
        $this->idToken = fn () => JWT::encode($this->claims, str_repeat('s', 32), 'HS256', 'k1');
        $this->oidcSignIn($provider, ['sub' => 'person-1'])->assertRedirect($failed);
        $this->idToken = fn () => rtrim(strtr(base64_encode('{"alg":"none","typ":"JWT"}'), '+/', '-_'), '=').'.'.rtrim(strtr(base64_encode(json_encode($this->claims)), '+/', '-_'), '=').'.';
        $this->oidcSignIn($provider, ['sub' => 'person-1'])->assertRedirect($failed);
        $this->assertNull(UserIdentity::sole()->last_used_at);
    }

    public function test_a_person_with_no_connected_user_cannot_sign_in_and_an_account_connects_to_one_user_only(): void
    {
        $provider = $this->oidcProvider();
        $this->oidcSignIn($provider, ['sub' => 'person-1', 'email' => 'someone@example.org', 'email_verified' => true])->assertRedirect(self::BASE.'/login#sso-error=no-account');

        $first = $this->user(Role::Owner, $this->customer());
        UserIdentity::create(['user_id' => $first->id, 'identity_provider_id' => $provider->id, 'subject' => 'person-1']);
        $second = $this->user(Role::Owner, $this->customer());
        $url = $this->withToken($this->sessionOf($second))->postJson($this->url("me/identities/{$provider->id}"), ['password' => 'password'])->json('url');
        parse_str((string) parse_url($url, PHP_URL_QUERY), $query);
        $this->app['auth']->forgetGuards();
        $this->oidcSignIn($provider, ['sub' => 'person-1'], $query['connect'])->assertRedirect(self::BASE.'/settings#sso-error=taken');
        $this->assertSame(0, $second->identities()->count());
    }

    public function test_a_provider_can_connect_users_by_a_verified_email_of_its_domains(): void
    {
        $provider = $this->oidcProvider(['link_by_email' => true, 'email_domains' => ['example.org']]);
        $user = User::factory()->create(['email' => 'pat@example.org', 'role' => Role::Owner, 'customer_id' => $this->customer()->id]);
        User::factory()->create(['email' => 'bot@example.org', 'role' => Role::Administrator, 'plugin_id' => 'bot']);
        $noAccount = self::BASE.'/login#sso-error=no-account';

        // The provider does not say that the email is verified.
        $this->oidcSignIn($provider, ['sub' => 'p-1', 'email' => 'pat@example.org'])->assertRedirect($noAccount);
        $this->oidcSignIn($provider, ['sub' => 'p-1', 'email' => 'pat@example.org', 'email_verified' => false])->assertRedirect($noAccount);
        // A domain that is not in the list, and the user of a plugin.
        $this->oidcSignIn($provider, ['sub' => 'p-2', 'email' => 'pat@example.net', 'email_verified' => true])->assertRedirect($noAccount);
        $this->oidcSignIn($provider, ['sub' => 'p-3', 'email' => 'bot@example.org', 'email_verified' => true])->assertRedirect($noAccount);

        $this->complete($this->oidcSignIn($provider, ['sub' => 'p-1', 'email' => 'Pat@Example.org', 'email_verified' => true]))->assertOk();
        $this->assertDatabaseHas('user_identities', ['user_id' => $user->id, 'subject' => 'p-1']);
        $this->assertDatabaseHas('audit_entries', ['action' => 'Connected single sign-on account by email', 'target' => 'Example SSO (pat@example.org)']);
        // A second account of the same provider does not connect to the same user.
        $this->oidcSignIn($provider, ['sub' => 'p-4', 'email' => 'pat@example.org', 'email_verified' => true])->assertRedirect($noAccount);
    }

    public function test_a_user_with_totp_gives_a_code_after_the_provider(): void
    {
        $provider = $this->oidcProvider();
        $user = $this->user(Role::Owner, $this->customer());
        $secret = $this->totp($user);
        UserIdentity::create(['user_id' => $user->id, 'identity_provider_id' => $provider->id, 'subject' => 'person-1']);

        $redirect = $this->oidcSignIn($provider, ['sub' => 'person-1']);
        $this->complete($redirect)->assertUnprocessable()->assertJsonPath('totpRequired', true);
        $this->complete($redirect, ['totp' => '000000'])->assertUnprocessable()->assertJsonValidationErrors('totp');
        $this->complete($redirect, ['totp' => Totp::code($secret)])->assertOk();
        $this->assertDatabaseHas('audit_entries', ['action' => 'Signed in with single sign-on and TOTP', 'user_id' => $user->id]);

        // After 5 wrong codes, the ticket stops.
        $redirect = $this->oidcSignIn($provider, ['sub' => 'person-1']);
        for ($i = 0; $i < 5; $i++) {
            $this->complete($redirect, ['totp' => '000000'])->assertUnprocessable();
        }
        $this->complete($redirect, ['totp' => '000000'])->assertJsonValidationErrors('ticket');

        // A suspended user does not get a session.
        $this->travel(2)->minutes();
        User::whereKey($user->id)->update(['totp_last_step' => null]);
        $user->customer->update(['suspended' => true]);
        $this->complete($this->oidcSignIn($provider, ['sub' => 'person-1']), ['totp' => Totp::code($secret)])->assertForbidden();
    }

    public function test_a_user_signs_in_with_saml(): void
    {
        $provider = $this->samlProvider();
        $user = $this->user(Role::Owner, $this->customer());
        UserIdentity::create(['user_id' => $user->id, 'identity_provider_id' => $provider->id, 'subject' => 'pat@example.org']);
        $acs = self::BASE.'/api/v1/auth/sso/saml/acs';
        $entityId = self::BASE.'/api/v1/auth/sso/saml/metadata';

        // The metadata of the panel for the providers.
        $metadata = $this->get($this->url('auth/sso/saml/metadata'))->assertOk()->assertHeader('Content-Type', 'application/samlmetadata+xml')->getContent();
        $this->assertStringContainsString("entityID=\"{$entityId}\"", $metadata);
        $this->assertStringContainsString("Location=\"{$acs}\"", $metadata);

        $post = function (array $change = [], bool $sign = true) use ($provider, $acs, $entityId) {
            [$query, $cookie] = $this->startAt($provider);
            $request = gzinflate(base64_decode($query['SAMLRequest']));
            $this->assertStringContainsString("AssertionConsumerServiceURL=\"{$acs}\"", $request);
            preg_match('/ID="([^"]+)"/', $request, $id);
            $xml = $this->samlResponse($change + ['inResponseTo' => $id[1], 'destination' => $acs, 'audience' => $entityId]);
            if ($sign) {
                $xml = Utils::addSign($xml, self::$rsa['key'], self::$rsa['cert']);
            }

            return $this->withUnencryptedCookie(SingleSignOn::COOKIE, $cookie)->post($acs, ['SAMLResponse' => base64_encode($xml), 'RelayState' => $query['RelayState']]);
        };

        $this->complete($post())->assertOk();
        $this->assertDatabaseHas('user_identities', ['user_id' => $user->id, 'email' => 'pat@example.org']);

        // The state of a SAML sign-in does not work at the address of OpenID Connect.
        [$query, $cookie] = $this->startAt($provider);
        $this->withUnencryptedCookie(SingleSignOn::COOKIE, $cookie)->get($this->url("auth/sso/oidc/callback?code=abc&state={$query['RelayState']}"))
            ->assertRedirect(self::BASE.'/login#sso-error=expired');

        $failed = self::BASE.'/login#sso-error=failed';
        $post([], sign: false)->assertRedirect($failed);
        $post(['inResponseTo' => '_other'])->assertRedirect($failed);
        $post(['destination' => self::BASE.'/api/v1/auth/sso/other/acs'])->assertRedirect($failed);
        // The issuer of the answer must be the provider of the sign-in.
        $post(['issuer' => 'https://other-idp.example.org'])->assertRedirect($failed);
        $post(['audience' => 'https://other.example/metadata'])->assertRedirect($failed);
        $post(['notOnOrAfter' => gmdate('Y-m-d\TH:i:s\Z', time() - 600)])->assertRedirect($failed);
        $post(['format' => Constants::NAMEID_TRANSIENT, 'nameId' => '_t123'])->assertRedirect($failed);
        // A signature of a different key.
        $provider->update(['certificate' => self::rsa()['cert']]);
        $post()->assertRedirect($failed);
    }

    public function test_only_an_administrator_in_a_session_changes_providers_and_the_rules_of_a_provider(): void
    {
        $owner = $this->user(Role::Owner, $this->customer());
        $this->withToken($this->sessionOf($owner))->getJson($this->url('identity-providers'))->assertForbidden();
        $this->app['auth']->forgetGuards();
        $admin = $this->user(Role::Administrator);
        $apiToken = $admin->createToken('CI', ['*'])->plainTextToken;
        // The values for a provider are the same for all the providers, so the administrator can
        // give them to a provider before the provider is in the panel.
        $this->withToken($apiToken)->getJson($this->url('identity-providers'))->assertOk()->assertExactJson(['data' => [], 'serviceProvider' => [
            'redirectUri' => self::BASE.'/api/v1/auth/sso/oidc/callback',
            'entityId' => self::BASE.'/api/v1/auth/sso/saml/metadata',
            'acsUrl' => self::BASE.'/api/v1/auth/sso/saml/acs',
        ]]);
        $this->withToken($apiToken)->postJson($this->url('identity-providers'), ['name' => 'X', 'protocol' => 'oidc', 'password' => 'password'])->assertForbidden();
        $this->app['auth']->forgetGuards();

        $session = $this->sessionOf($admin);
        $post = fn (array $data) => $this->withToken($session)->postJson($this->url('identity-providers'), $data + ['password' => 'password']);
        $oidc = ['name' => 'Example SSO', 'protocol' => 'oidc', 'issuer' => self::ISSUER, 'clientId' => 'panel', 'clientSecret' => 'secret'];
        $post(['issuer' => 'http://login.example.org'] + $oidc)->assertJsonValidationErrors('issuer');
        $post(['issuer' => 'https://login.example.net'] + $oidc)->assertJsonValidationErrors('issuer');
        $post(['linkByEmail' => true] + $oidc)->assertJsonValidationErrors('emailDomains');
        $post(['emailDomains' => ['not a domain']] + $oidc)->assertJsonValidationErrors('emailDomains.0');
        $post(['protocol' => 'saml', 'name' => 'SAML', 'entityId' => self::IDP_ENTITY, 'ssoUrl' => 'https://idp.example.org/sso', 'certificate' => 'not a certificate'])->assertJsonValidationErrors('certificate');
        // The metadata of a provider has the certificate as base64 text with no header.
        $body = preg_replace('/-----[A-Z ]+-----|\s+/', '', self::$rsa['cert']);
        $saml = $post(['protocol' => 'saml', 'name' => 'SAML', 'entityId' => self::IDP_ENTITY, 'ssoUrl' => 'https://idp.example.org/sso', 'certificate' => $body])->assertCreated();
        $this->assertNotNull($saml->json('certificateExpiresAt'));

        $id = $post(['linkByEmail' => true, 'emailDomains' => ['Example.org']] + $oidc)->assertCreated()->assertJsonPath('emailDomains', ['example.org'])->json('id');
        // A change keeps the secret when it does not give a new one. The protocol does not change.
        $this->withToken($session)->putJson($this->url("identity-providers/{$id}"), ['name' => 'Renamed', 'issuer' => self::ISSUER, 'clientId' => 'panel', 'password' => 'password', 'protocol' => 'saml'])->assertJsonValidationErrors('protocol');
        $this->withToken($session)->putJson($this->url("identity-providers/{$id}"), ['name' => 'Renamed', 'issuer' => self::ISSUER, 'clientId' => 'panel', 'enabled' => false, 'password' => 'password'])
            ->assertOk()->assertJsonPath('name', 'Renamed')->assertJsonPath('enabled', false)->assertJsonPath('hasClientSecret', true);
        $this->assertSame('secret', IdentityProvider::find($id)->client_secret);

        UserIdentity::create(['user_id' => $owner->id, 'identity_provider_id' => $id, 'subject' => 'x']);
        $this->withToken($session)->deleteJson($this->url("identity-providers/{$id}"))->assertJsonValidationErrors('password');
        $this->withToken($session)->deleteJson($this->url("identity-providers/{$id}"), ['password' => 'password'])->assertNoContent();
        $this->assertSame(0, UserIdentity::count());
        $this->assertDatabaseHas('audit_entries', ['action' => 'Removed single sign-on provider', 'target' => 'Renamed']);
    }

    public function test_a_user_disconnects_an_account_and_a_reset_removes_the_connections(): void
    {
        $provider = $this->oidcProvider();
        $user = $this->user(Role::Owner, $this->customer());
        UserIdentity::create(['user_id' => $user->id, 'identity_provider_id' => $provider->id, 'subject' => 'person-1']);
        $session = $this->sessionOf($user);
        $apiToken = $user->createToken('CI', ['*'])->plainTextToken;
        $this->withToken($apiToken)->getJson($this->url('me/identities'))->assertOk()->assertJsonPath('data.0.connected', true);
        $this->withToken($apiToken)->deleteJson($this->url("me/identities/{$provider->id}"), ['password' => 'password'])->assertForbidden();
        $this->app['auth']->forgetGuards();
        $this->withToken($session)->deleteJson($this->url("me/identities/{$provider->id}"), ['password' => 'password'])->assertNoContent();
        $this->assertDatabaseHas('audit_entries', ['action' => 'Disconnected single sign-on account', 'target' => "Example SSO ({$user->email})"]);

        UserIdentity::create(['user_id' => $user->id, 'identity_provider_id' => $provider->id, 'subject' => 'person-1']);
        $this->app['auth']->forgetGuards();
        $admin = $this->user(Role::Administrator);
        $this->withToken($this->sessionOf($admin))->postJson($this->url("users/{$user->id}/reset-password"))->assertOk();
        $this->assertSame(0, $user->identities()->count());
    }

    /** A SAML answer of the fake provider for the user pat@example.org. */
    private function samlResponse(array $values): string
    {
        $v = $values + ['nameId' => 'pat@example.org', 'format' => Constants::NAMEID_EMAIL_ADDRESS, 'notOnOrAfter' => gmdate('Y-m-d\TH:i:s\Z', time() + 300), 'issuer' => self::IDP_ENTITY];
        $now = gmdate('Y-m-d\TH:i:s\Z');
        $before = gmdate('Y-m-d\TH:i:s\Z', time() - 60);
        $issuer = $v['issuer'];

        return <<<XML
<samlp:Response xmlns:samlp="urn:oasis:names:tc:SAML:2.0:protocol" xmlns:saml="urn:oasis:names:tc:SAML:2.0:assertion" ID="_r{$this->random()}" Version="2.0" IssueInstant="{$now}" Destination="{$v['destination']}" InResponseTo="{$v['inResponseTo']}"><saml:Issuer>{$issuer}</saml:Issuer><samlp:Status><samlp:StatusCode Value="urn:oasis:names:tc:SAML:2.0:status:Success"/></samlp:Status><saml:Assertion ID="_a{$this->random()}" Version="2.0" IssueInstant="{$now}"><saml:Issuer>{$issuer}</saml:Issuer><saml:Subject><saml:NameID Format="{$v['format']}">{$v['nameId']}</saml:NameID><saml:SubjectConfirmation Method="urn:oasis:names:tc:SAML:2.0:cm:bearer"><saml:SubjectConfirmationData NotOnOrAfter="{$v['notOnOrAfter']}" Recipient="{$v['destination']}" InResponseTo="{$v['inResponseTo']}"/></saml:SubjectConfirmation></saml:Subject><saml:Conditions NotBefore="{$before}" NotOnOrAfter="{$v['notOnOrAfter']}"><saml:AudienceRestriction><saml:Audience>{$v['audience']}</saml:Audience></saml:AudienceRestriction></saml:Conditions><saml:AuthnStatement AuthnInstant="{$now}" SessionIndex="_s1"><saml:AuthnContext><saml:AuthnContextClassRef>urn:oasis:names:tc:SAML:2.0:ac:classes:PasswordProtectedTransport</saml:AuthnContextClassRef></saml:AuthnContext></saml:AuthnStatement><saml:AttributeStatement><saml:Attribute Name="displayName"><saml:AttributeValue>Pat Example</saml:AttributeValue></saml:Attribute></saml:AttributeStatement></saml:Assertion></samlp:Response>
XML;
    }

    private function random(): string
    {
        return bin2hex(random_bytes(10));
    }
}
