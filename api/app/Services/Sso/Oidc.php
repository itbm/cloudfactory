<?php

namespace App\Services\Sso;

use App\Models\IdentityProvider;
use Firebase\JWT\JWK;
use Firebase\JWT\JWT;
use Firebase\JWT\Key;
use Illuminate\Http\Client\ConnectionException;
use Illuminate\Support\Facades\Cache;
use Illuminate\Support\Facades\Http;
use ParagonIE\ConstantTime\Base64UrlSafe;
use Throwable;

/**
 * OpenID Connect: the authorization code flow with PKCE. The panel reads the discovery document
 * of the issuer, and verifies the signature of the ID token with the keys of the issuer.
 */
class Oidc
{
    public const SCOPES = 'openid email profile';

    /** The time in seconds that the panel keeps the discovery document and the keys. */
    private const CACHE_SECONDS = 3600;

    /** @return array<string, mixed> the discovery document of the issuer */
    public function discovery(IdentityProvider $provider, bool $fresh = false): array
    {
        $key = 'sso-discovery:'.hash('sha256', (string) $provider->issuer);
        if ($fresh) {
            Cache::forget($key);
        }

        return Cache::remember($key, self::CACHE_SECONDS, function () use ($provider) {
            $document = $this->getJson(rtrim((string) $provider->issuer, '/').'/.well-known/openid-configuration');
            if (($document['issuer'] ?? null) !== $provider->issuer) {
                throw new SsoException('failed', 'The discovery document has the issuer "'.(is_string($document['issuer'] ?? null) ? $document['issuer'] : '').'", not "'.$provider->issuer.'". Copy the issuer from the document.');
            }
            foreach (['authorization_endpoint', 'token_endpoint', 'jwks_uri'] as $name) {
                if (! is_string($document[$name] ?? null) || ! self::allowedUrl($document[$name])) {
                    throw new SsoException('failed', "The discovery document has no valid {$name}.");
                }
            }

            return $document;
        });
    }

    /** The address of the provider where the user signs in. */
    public function authorizationUrl(IdentityProvider $provider, string $redirectUri, string $state, string $nonce, string $verifier): string
    {
        $endpoint = $this->discovery($provider)['authorization_endpoint'];
        $query = http_build_query([
            'response_type' => 'code',
            'client_id' => $provider->client_id,
            'redirect_uri' => $redirectUri,
            'scope' => self::SCOPES,
            'state' => $state,
            'nonce' => $nonce,
            'code_challenge' => Base64UrlSafe::encodeUnpadded(hash('sha256', $verifier, true)),
            'code_challenge_method' => 'S256',
        ], '', '&', PHP_QUERY_RFC3986);

        return $endpoint.(str_contains($endpoint, '?') ? '&' : '?').$query;
    }

    /** Changes the code of the provider into an ID token, and gives the account of the token. */
    public function exchange(IdentityProvider $provider, string $code, string $redirectUri, string $verifier, string $nonce): Account
    {
        $document = $this->discovery($provider);
        $form = ['grant_type' => 'authorization_code', 'code' => $code, 'redirect_uri' => $redirectUri, 'code_verifier' => $verifier];
        $request = Http::asForm()->acceptJson()->timeout(10);
        // The default method is HTTP Basic (RFC 6749, section 2.3.1). Some providers accept
        // the secret only in the form.
        $methods = $document['token_endpoint_auth_methods_supported'] ?? ['client_secret_basic'];
        if (is_array($methods) && ! in_array('client_secret_basic', $methods, true) && in_array('client_secret_post', $methods, true)) {
            $form += ['client_id' => $provider->client_id, 'client_secret' => $provider->client_secret];
        } else {
            $request = $request->withBasicAuth(urlencode((string) $provider->client_id), urlencode((string) $provider->client_secret));
        }
        try {
            $response = $request->post($document['token_endpoint'], $form);
        } catch (ConnectionException $e) {
            throw new SsoException('failed', "The panel could not connect to the token endpoint: {$e->getMessage()}");
        }
        $idToken = $response->json('id_token');
        if (! $response->successful() || ! is_string($idToken)) {
            throw new SsoException('failed', "The token endpoint gave status {$response->status()} with no ID token: ".substr((string) $response->json('error_description', $response->json('error', '')), 0, 200));
        }

        return $this->verify($provider, $idToken, $nonce);
    }

    /** Verifies the ID token: its signature, the issuer, the audience, the time and the nonce. */
    public function verify(IdentityProvider $provider, string $idToken, string $nonce): Account
    {
        $claims = null;
        foreach ([false, true] as $fresh) {
            try {
                JWT::$leeway = 60;
                $claims = (array) JWT::decode($idToken, $this->keys($provider, $idToken, $fresh));
                break;
            } catch (Throwable $e) {
                // The provider can have a new key: the panel reads the keys again one time.
                if ($fresh) {
                    throw new SsoException('failed', "The ID token is not valid: {$e->getMessage()}");
                }
            }
        }
        $audience = (array) ($claims['aud'] ?? []);
        $checks = [
            'issuer' => ($claims['iss'] ?? null) === $provider->issuer,
            'audience' => in_array($provider->client_id, $audience, true) && (count($audience) === 1 || ($claims['azp'] ?? null) === $provider->client_id),
            'expiry' => is_int($claims['exp'] ?? null),
            'nonce' => is_string($claims['nonce'] ?? null) && hash_equals($nonce, $claims['nonce']),
            'subject' => is_string($claims['sub'] ?? null) && $claims['sub'] !== '' && strlen($claims['sub']) <= 255,
        ];
        if ($failed = array_keys(array_filter($checks, fn (bool $ok) => ! $ok))) {
            throw new SsoException('failed', 'The ID token is not valid. These checks failed: '.implode(', ', $failed).'.');
        }
        $email = is_string($claims['email'] ?? null) ? strtolower($claims['email']) : null;
        $verified = ($claims['email_verified'] ?? false) === true || ($claims['email_verified'] ?? null) === 'true';

        return new Account($claims['sub'], $email, $email !== null && $verified, is_string($claims['name'] ?? null) ? $claims['name'] : null);
    }

    /**
     * The public keys of the provider. A key with no "alg" gets the usual algorithm of its type.
     * The panel does not accept HMAC (a shared secret) or the algorithm "none".
     *
     * @return Key|array<string, Key>
     */
    private function keys(IdentityProvider $provider, string $idToken, bool $fresh): Key|array
    {
        $uri = $this->discovery($provider)['jwks_uri'];
        $key = 'sso-jwks:'.hash('sha256', $uri);
        if ($fresh) {
            Cache::forget($key);
        }
        $set = Cache::remember($key, self::CACHE_SECONDS, fn () => $this->getJson($uri));
        $keys = [];
        foreach (is_array($set['keys'] ?? null) ? $set['keys'] : [] as $jwk) {
            if (! is_array($jwk) || ($jwk['use'] ?? 'sig') !== 'sig') {
                continue;
            }
            $jwk['alg'] ??= match ($jwk['kty'] ?? null) {
                'RSA' => 'RS256',
                'EC' => ['P-256' => 'ES256', 'P-384' => 'ES384'][$jwk['crv'] ?? ''] ?? null,
                'OKP' => 'EdDSA',
                default => null,
            };
            if (! in_array($jwk['alg'], ['RS256', 'RS384', 'RS512', 'PS256', 'PS384', 'PS512', 'ES256', 'ES384', 'EdDSA'], true)) {
                continue;
            }
            $keys[] = $jwk;
        }
        $parsed = JWK::parseKeySet(['keys' => $keys]);
        // A token with no key ID works only when the provider has one key.
        $header = json_decode(Base64UrlSafe::decodeNoPadding(explode('.', $idToken)[0] ?? ''), true);
        if (! isset($header['kid']) && count($parsed) === 1) {
            return array_values($parsed)[0];
        }

        return $parsed;
    }

    /** @return array<string, mixed> */
    private function getJson(string $url): array
    {
        if (! self::allowedUrl($url)) {
            throw new SsoException('failed', "The address {$url} must use HTTPS.");
        }
        try {
            $response = Http::acceptJson()->timeout(10)->get($url);
        } catch (ConnectionException $e) {
            throw new SsoException('failed', "The panel could not connect to {$url}: {$e->getMessage()}");
        }
        if (! $response->successful() || ! is_array($response->json())) {
            throw new SsoException('failed', "The panel could not read {$url}: status {$response->status()}.");
        }

        return $response->json();
    }

    /** HTTPS only. In development, also HTTP, for a provider on the same computer. */
    public static function allowedUrl(string $url): bool
    {
        $scheme = parse_url($url, PHP_URL_SCHEME);

        return filter_var($url, FILTER_VALIDATE_URL) !== false
            && ($scheme === 'https' || ($scheme === 'http' && app()->environment(['local', 'demo', 'development'])));
    }
}
