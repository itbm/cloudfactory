<?php

namespace App\Services\Sso;

use App\Models\IdentityProvider;
use OneLogin\Saml2\AuthnRequest;
use OneLogin\Saml2\Constants;
use OneLogin\Saml2\Response;
use OneLogin\Saml2\Settings;
use OneLogin\Saml2\Utils;
use Throwable;

/**
 * SAML 2.0 with the library onelogin/php-saml. The panel sends the request with the
 * HTTP-Redirect binding, and the provider posts the answer to the assertion consumer service
 * (ACS). The answer or its assertion must have a valid signature of the certificate of the
 * provider. The panel does not sign its requests.
 */
class Saml
{
    /** The attributes that can hold the email of the user. */
    private const EMAIL_ATTRIBUTES = ['email', 'mail', 'emailaddress', 'http://schemas.xmlsoap.org/ws/2005/05/identity/claims/emailaddress', 'urn:oid:0.9.2342.19200300.100.1.3'];

    private const NAME_ATTRIBUTES = ['displayName', 'name', 'http://schemas.microsoft.com/identity/claims/displayname', 'http://schemas.xmlsoap.org/ws/2005/05/identity/claims/name', 'urn:oid:2.16.840.1.113730.3.1.241'];

    /**
     * The entity ID of the panel, which is also the address of its metadata. The panel is one
     * service provider for all the SAML providers, so that the administrator can give these
     * values to a provider before the provider is in the panel.
     */
    public static function entityId(string $base): string
    {
        return "{$base}/api/v1/auth/sso/saml/metadata";
    }

    /** The assertion consumer service of the panel. The state of the sign-in identifies the provider. */
    public static function acsUrl(string $base): string
    {
        return "{$base}/api/v1/auth/sso/saml/acs";
    }

    /**
     * The address of the provider where the user signs in, and the ID of the request.
     *
     * @return array{0: string, 1: string}
     */
    public function loginUrl(IdentityProvider $provider, string $base, string $relayState): array
    {
        $request = new AuthnRequest(new Settings($this->settings($provider, $base)));
        $query = http_build_query(['SAMLRequest' => $request->getRequest(), 'RelayState' => $relayState], '', '&');
        $url = (string) $provider->sso_url;

        return [$url.(str_contains($url, '?') ? '&' : '?').$query, $request->getId()];
    }

    /**
     * Verifies the answer of the provider: the signature, the destination, the audience, the
     * time and the ID of the request.
     */
    public function verify(IdentityProvider $provider, string $base, string $samlResponse, string $requestId): Account
    {
        // The library compares the destination of the answer with the address of this
        // request. It reads the address from $_SERVER: give it the address of the ACS.
        $uri = $_SERVER['REQUEST_URI'] ?? null;
        $_SERVER['REQUEST_URI'] = (string) parse_url(self::acsUrl($base), PHP_URL_PATH);
        try {
            $response = new Response(new Settings($this->settings($provider, $base)), $samlResponse);
            if (! $response->isValid($requestId)) {
                throw new SsoException('failed', 'The SAML answer is not valid: '.$response->getError());
            }
            $subject = (string) $response->getNameId();
            $format = (string) $response->getNameIdFormat();
            $attributes = $response->getAttributes();
        } catch (SsoException $e) {
            throw $e;
        } catch (Throwable $e) {
            throw new SsoException('failed', "The SAML answer is not valid: {$e->getMessage()}");
        } finally {
            Utils::setBaseURL(null);
            if ($uri === null) {
                unset($_SERVER['REQUEST_URI']);
            } else {
                $_SERVER['REQUEST_URI'] = $uri;
            }
        }
        // A transient NameID changes at each sign-in, so it cannot identify the account.
        if ($format === Constants::NAMEID_TRANSIENT || $subject === '' || strlen($subject) > 255) {
            throw new SsoException('failed', 'The SAML answer has no stable NameID. Set the NameID format of the provider to "persistent" or "emailAddress".');
        }
        $email = self::first($attributes, self::EMAIL_ATTRIBUTES);
        if ($email === null && ($format === Constants::NAMEID_EMAIL_ADDRESS || filter_var($subject, FILTER_VALIDATE_EMAIL))) {
            $email = $subject;
        }
        $email = $email !== null && filter_var($email, FILTER_VALIDATE_EMAIL) ? strtolower($email) : null;

        // A SAML answer has no "email_verified". The administrator decides to trust the
        // emails of the provider (IdentityProvider::link_by_email).
        return new Account($subject, $email, $email !== null, self::first($attributes, self::NAME_ATTRIBUTES));
    }

    /** The metadata of the panel as a service provider, for the providers. */
    public function metadata(string $base): string
    {
        return (new Settings($this->settings(null, $base), true))->getSPMetadata();
    }

    /** @return array<string, mixed> the settings of the library. With no provider, only the part of the panel. */
    private function settings(?IdentityProvider $provider, string $base): array
    {
        return [
            'strict' => true,
            'debug' => false,
            'baseurl' => "{$base}/",
            'sp' => [
                'entityId' => self::entityId($base),
                'assertionConsumerService' => ['url' => self::acsUrl($base), 'binding' => Constants::BINDING_HTTP_POST],
                'NameIDFormat' => Constants::NAMEID_UNSPECIFIED,
            ],
            'idp' => $provider ? [
                'entityId' => (string) $provider->entity_id,
                'singleSignOnService' => ['url' => (string) $provider->sso_url, 'binding' => Constants::BINDING_HTTP_REDIRECT],
                'x509cert' => (string) $provider->certificate,
            ] : [],
            'security' => [
                'authnRequestsSigned' => false,
                // The library refuses an answer with no signature. A signature on the answer
                // or on the assertion is sufficient.
                'wantMessagesSigned' => false,
                'wantAssertionsSigned' => false,
                'wantNameId' => true,
                // The provider can use any method to sign in, for example a passkey or MFA.
                'requestedAuthnContext' => false,
                'destinationStrictlyMatches' => true,
                'rejectUnsolicitedResponsesWithInResponseTo' => true,
                'rejectDeprecatedAlgorithm' => true,
                'wantXMLValidation' => true,
                'signatureAlgorithm' => 'http://www.w3.org/2001/04/xmldsig-more#rsa-sha256',
                'digestAlgorithm' => 'http://www.w3.org/2001/04/xmlenc#sha256',
            ],
        ];
    }

    /** @param array<string, list<string>> $attributes */
    private static function first(array $attributes, array $names): ?string
    {
        foreach ($attributes as $name => $values) {
            if (in_array(strtolower($name), array_map(strtolower(...), $names), true) && is_string($values[0] ?? null) && $values[0] !== '') {
                return $values[0];
            }
        }

        return null;
    }
}
