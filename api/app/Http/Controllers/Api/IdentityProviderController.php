<?php

namespace App\Http\Controllers\Api;

use App\Http\Controllers\Controller;
use App\Models\IdentityProvider;
use App\Services\Audit;
use App\Services\Sso\Oidc;
use App\Services\Sso\SingleSignOn;
use App\Services\Sso\SsoException;
use Illuminate\Http\JsonResponse;
use Illuminate\Http\Request;
use Illuminate\Validation\Rule;
use Illuminate\Validation\ValidationException;

/**
 * Single sign-on: the identity providers of the panel. Only an administrator can see them. A
 * provider can sign in users, so only an administrator in a sign-in session can add, change or
 * remove one, with the password and, when TOTP is on, a code.
 */
class IdentityProviderController extends Controller
{
    public function index(Request $request): JsonResponse
    {
        $base = SingleSignOn::panelUrl($request);

        return response()->json([
            'data' => IdentityProvider::withCount('identities')->orderBy('name')->get()->map(fn (IdentityProvider $p) => self::present($p)),
            // The values to give to a provider. They are the same for all the providers.
            'serviceProvider' => SingleSignOn::serviceProvider($base),
        ]);
    }

    public function store(Request $request, Oidc $oidc): JsonResponse
    {
        AuthController::sessionUser($request);
        $data = $this->validated($request, null);
        AuthController::confirmIdentity($request);
        $provider = new IdentityProvider($data);
        $this->check($provider, $oidc);
        $provider->save();
        Audit::record($request, 'Added single sign-on provider', $provider->name);

        return response()->json(self::present($provider->loadCount('identities')), 201);
    }

    public function update(Request $request, Oidc $oidc, string $id): JsonResponse
    {
        AuthController::sessionUser($request);
        $provider = IdentityProvider::findOrFail($id);
        $data = $this->validated($request, $provider);
        AuthController::confirmIdentity($request);
        $provider->fill($data);
        $this->check($provider, $oidc);
        $provider->save();
        Audit::record($request, 'Changed single sign-on provider', $provider->name);

        return response()->json(self::present($provider->loadCount('identities')));
    }

    /** Removes the provider and the connections of the users to it. */
    public function destroy(Request $request, string $id): JsonResponse
    {
        AuthController::sessionUser($request);
        $provider = IdentityProvider::findOrFail($id);
        AuthController::confirmIdentity($request);
        $provider->delete();
        Audit::record($request, 'Removed single sign-on provider', $provider->name);

        return response()->json(null, 204);
    }

    /** @return array<string, mixed> the columns of the provider */
    private function validated(Request $request, ?IdentityProvider $provider): array
    {
        $protocol = $provider?->protocol ?? $request->input('protocol');
        $oidc = $protocol === IdentityProvider::OIDC;
        $saml = $protocol === IdentityProvider::SAML;
        $data = $request->validate([
            'name' => ['required', 'string', 'max:100'],
            // The protocol of a provider does not change: add a new provider.
            'protocol' => $provider ? ['prohibited'] : ['required', Rule::in([IdentityProvider::OIDC, IdentityProvider::SAML])],
            'enabled' => ['boolean'],
            'issuer' => [Rule::requiredIf($oidc), Rule::prohibitedIf($saml), 'string', 'max:500', 'url'],
            'clientId' => [Rule::requiredIf($oidc), Rule::prohibitedIf($saml), 'string', 'max:255'],
            // The secret stays when a change does not give it.
            'clientSecret' => [Rule::requiredIf($oidc && ! $provider), Rule::prohibitedIf($saml), 'nullable', 'string', 'max:2000'],
            'entityId' => [Rule::requiredIf($saml), Rule::prohibitedIf($oidc), 'string', 'max:500'],
            'ssoUrl' => [Rule::requiredIf($saml), Rule::prohibitedIf($oidc), 'string', 'max:500', 'url'],
            'certificate' => [Rule::requiredIf($saml), Rule::prohibitedIf($oidc), 'string', 'max:20000'],
            'linkByEmail' => ['boolean'],
            'emailDomains' => ['array', 'max:50'],
            'emailDomains.*' => ['string', 'max:253', 'regex:/^(?=.{1,253}$)([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]{2,63}$/i'],
        ], [
            'emailDomains.*.regex' => 'Type a domain, for example example.com.',
            'clientSecret.required' => 'Type the client secret.',
        ]);
        foreach (['issuer', 'ssoUrl'] as $field) {
            if (isset($data[$field]) && ! Oidc::allowedUrl($data[$field])) {
                throw ValidationException::withMessages([$field => 'The address must use HTTPS.']);
            }
        }
        $domains = array_values(array_unique(array_map(strtolower(...), $data['emailDomains'] ?? $provider?->email_domains ?? [])));
        $linkByEmail = (bool) ($data['linkByEmail'] ?? $provider?->link_by_email ?? false);
        // A provider that connects users by email gives each person with an account at the
        // provider the access of the user with that email. Limit it to the domains of the
        // organization of the provider.
        if ($linkByEmail && $domains === []) {
            throw ValidationException::withMessages(['emailDomains' => 'Type the email domains of the users of the provider, for example example.com.']);
        }
        $columns = ['name' => trim($data['name']), 'link_by_email' => $linkByEmail, 'email_domains' => $domains];
        if (! $provider) {
            $columns['protocol'] = $protocol;
        }
        if (array_key_exists('enabled', $data) || ! $provider) {
            $columns['enabled'] = (bool) ($data['enabled'] ?? true);
        }
        if ($oidc) {
            $columns += ['issuer' => $data['issuer'], 'client_id' => trim($data['clientId'])];
            if (($data['clientSecret'] ?? '') !== '') {
                $columns['client_secret'] = $data['clientSecret'];
            }
        } else {
            $columns += ['entity_id' => trim($data['entityId']), 'sso_url' => $data['ssoUrl'], 'certificate' => self::certificate($data['certificate'])];
        }

        return $columns;
    }

    /** An OpenID Connect provider: the panel reads the discovery document of the issuer. */
    private function check(IdentityProvider $provider, Oidc $oidc): void
    {
        if ($provider->protocol !== IdentityProvider::OIDC || ! $provider->enabled) {
            return;
        }
        try {
            $oidc->discovery($provider, fresh: true);
        } catch (SsoException $e) {
            throw ValidationException::withMessages(['issuer' => $e->getMessage()]);
        }
    }

    /** The certificate of a SAML provider in PEM format. The metadata of a provider has only the base64 text. */
    private static function certificate(string $value): string
    {
        $body = preg_replace('/-----(BEGIN|END) CERTIFICATE-----|\s+/', '', $value);
        $pem = "-----BEGIN CERTIFICATE-----\n".chunk_split((string) $body, 64, "\n").'-----END CERTIFICATE-----';
        if ($body === '' || ! @openssl_x509_read($pem)) {
            throw ValidationException::withMessages(['certificate' => 'Paste the X.509 certificate of the provider, in PEM format or as base64 text.']);
        }

        return $pem;
    }

    /** @return array<string, mixed> */
    public static function present(IdentityProvider $p): array
    {
        $saml = $p->protocol === IdentityProvider::SAML;
        $certificate = $saml && $p->certificate ? openssl_x509_parse($p->certificate) : null;

        return [
            'id' => $p->id,
            'name' => $p->name,
            'protocol' => $p->protocol,
            'enabled' => $p->enabled,
            'issuer' => $p->issuer,
            'clientId' => $p->client_id,
            // The panel does not show the secret again.
            'hasClientSecret' => $p->client_secret !== null && $p->client_secret !== '',
            'entityId' => $p->entity_id,
            'ssoUrl' => $p->sso_url,
            'certificate' => $p->certificate,
            'certificateExpiresAt' => $certificate ? date(DATE_ATOM, $certificate['validTo_time_t']) : null,
            'linkByEmail' => $p->link_by_email,
            'emailDomains' => $p->email_domains,
            'users' => $p->identities_count ?? $p->identities()->count(),
            'createdAt' => $p->created_at?->toIso8601String(),
        ];
    }
}
