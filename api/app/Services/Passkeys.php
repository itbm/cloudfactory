<?php

namespace App\Services;

use App\Models\Passkey;
use App\Models\User;
use Cose\Algorithm\Manager;
use Cose\Algorithm\Signature\ECDSA\ES256;
use Cose\Algorithm\Signature\EdDSA\EdDSA;
use Cose\Algorithm\Signature\RSA\RS256;
use Cose\Algorithms;
use Illuminate\Support\Facades\Cache;
use Illuminate\Support\Facades\Log;
use Illuminate\Support\Str;
use Illuminate\Validation\ValidationException;
use ParagonIE\ConstantTime\Base64UrlSafe;
use Symfony\Component\Serializer\Normalizer\AbstractObjectNormalizer;
use Symfony\Component\Serializer\SerializerInterface;
use Throwable;
use Webauthn\AttestationStatement\AttestationStatementSupportManager;
use Webauthn\AttestationStatement\NoneAttestationStatementSupport;
use Webauthn\AuthenticatorAssertionResponse;
use Webauthn\AuthenticatorAssertionResponseValidator;
use Webauthn\AuthenticatorAttestationResponse;
use Webauthn\AuthenticatorAttestationResponseValidator;
use Webauthn\AuthenticatorSelectionCriteria;
use Webauthn\CeremonyStep\CeremonyStepManagerFactory;
use Webauthn\CredentialRecord;
use Webauthn\Denormalizer\WebauthnSerializerFactory;
use Webauthn\PublicKeyCredential;
use Webauthn\PublicKeyCredentialCreationOptions;
use Webauthn\PublicKeyCredentialDescriptor;
use Webauthn\PublicKeyCredentialParameters;
use Webauthn\PublicKeyCredentialRequestOptions;
use Webauthn\PublicKeyCredentialRpEntity;
use Webauthn\PublicKeyCredentialUserEntity;

/**
 * FR-103: passkeys (WebAuthn). The library web-auth/webauthn-lib examines the answers of the
 * browser. This class keeps the challenges and the passkeys.
 *
 * - Each passkey must verify the user (a fingerprint, a face or a PIN), so a passkey is a full
 *   sign-in with two factors. The panel does not ask for TOTP after a passkey.
 * - The relying party ID of a passkey is the panel domain of the request: the panel domain, or
 *   the panel domain of a reseller. The browser uses a passkey only on its own domain. The
 *   panel examines each answer against the domain of the passkey, not against the Host header.
 * - The panel asks for no attestation. It does not know the make of the authenticator.
 * - A challenge works one time only, for 5 minutes.
 */
class Passkeys
{
    public const MAX_PER_USER = 20;

    public const CEREMONY_MINUTES = 5;

    private ?SerializerInterface $serializer = null;

    /** The relying party ID for the host of a request, or null when passkeys do not work there. */
    public static function rpIdFor(string $host): ?string
    {
        $host = strtolower(rtrim($host, '.'));

        return in_array($host, self::rpIds(), true) ? $host : null;
    }

    /**
     * The panel domains on which a user can make and use a passkey. In development, also
     * "localhost".
     *
     * @return list<string>
     */
    public static function rpIds(): array
    {
        $ids = [strtolower((string) config('cloudfactory.panel_domain')), ...Branding::panelDomains()];
        if (app()->environment(['local', 'testing', 'demo', 'development'])) {
            $ids[] = 'localhost';
        }

        return array_values(array_unique(array_filter($ids)));
    }

    /**
     * The origins that a passkey of a relying party ID accepts: HTTPS on the default port. In
     * development, "localhost" also has the HTTP origins of config cloudfactory.passkeys.
     *
     * @return list<string>
     */
    public static function origins(string $rpId): array
    {
        $origins = ["https://{$rpId}"];
        if ($rpId === 'localhost') {
            array_push($origins, ...config('cloudfactory.passkeys.local_origins', []));
        }

        return $origins;
    }

    /**
     * Starts the creation of a passkey for a user. The answer is for the browser
     * (PublicKeyCredential.parseCreationOptionsFromJSON).
     *
     * @return array{ceremony: string, publicKey: array<string, mixed>}
     */
    public function startCreation(User $user, string $rpId, string $rpName): array
    {
        if ($user->passkeys()->count() >= self::MAX_PER_USER) {
            throw ValidationException::withMessages(['passkey' => 'A user can have '.self::MAX_PER_USER.' passkeys. Remove a passkey first.']);
        }
        // All passkeys of a user have one user handle, so that a device replaces the passkey of
        // the user on this domain and does not keep two. The handle is random: it does not
        // show the email or the ID of the user.
        $handle = $user->passkeys()->value('user_handle') ?? Base64UrlSafe::encodeUnpadded(random_bytes(32));
        $options = PublicKeyCredentialCreationOptions::create(
            rp: PublicKeyCredentialRpEntity::create($rpName, $rpId),
            user: PublicKeyCredentialUserEntity::create($user->email, Base64UrlSafe::decodeNoPadding($handle), $user->name),
            challenge: random_bytes(32),
            pubKeyCredParams: array_map(fn (int $alg) => PublicKeyCredentialParameters::createPk($alg), [Algorithms::COSE_ALGORITHM_ES256, Algorithms::COSE_ALGORITHM_EDDSA, Algorithms::COSE_ALGORITHM_RS256]),
            authenticatorSelection: AuthenticatorSelectionCriteria::create(
                userVerification: AuthenticatorSelectionCriteria::USER_VERIFICATION_REQUIREMENT_REQUIRED,
                residentKey: AuthenticatorSelectionCriteria::RESIDENT_KEY_REQUIREMENT_REQUIRED,
            ),
            attestation: PublicKeyCredentialCreationOptions::ATTESTATION_CONVEYANCE_PREFERENCE_NONE,
            // The device does not make a second passkey for this user and domain.
            excludeCredentials: $user->passkeys()->where('rp_id', $rpId)->get()
                ->map(fn (Passkey $p) => PublicKeyCredentialDescriptor::create(PublicKeyCredentialDescriptor::CREDENTIAL_TYPE_PUBLIC_KEY, $this->record($p)->publicKeyCredentialId))
                ->all(),
            timeout: self::CEREMONY_MINUTES * 60 * 1000,
        );

        return $this->remember($options, ['purpose' => 'create', 'userId' => $user->id, 'rpId' => $rpId, 'handle' => $handle]);
    }

    /**
     * Ends the creation of a passkey: examines the answer of the browser and keeps the passkey.
     *
     * @param  array<string, mixed>  $credential  the JSON of the browser (PublicKeyCredential.toJSON())
     */
    public function finishCreation(User $user, string $ceremony, array $credential, string $name): Passkey
    {
        $state = $this->take($ceremony, 'create');
        if ($state['userId'] !== $user->id) {
            throw self::invalid();
        }
        /** @var PublicKeyCredentialCreationOptions $options */
        $options = $this->serializer()->deserialize($state['options'], PublicKeyCredentialCreationOptions::class, 'json');
        $response = $this->credential($credential)->response;
        if (! $response instanceof AuthenticatorAttestationResponse) {
            throw self::invalid();
        }
        try {
            $record = AuthenticatorAttestationResponseValidator::create($this->ceremonies($state['rpId'])->creationCeremony())
                ->check($response, $options, $state['rpId']);
        } catch (Throwable $e) {
            Log::info("A new passkey of user {$user->id} is not valid: {$e->getMessage()}");
            throw self::invalid();
        }
        if (Passkey::where('credential_hash', hash('sha256', $record->publicKeyCredentialId))->exists()) {
            throw ValidationException::withMessages(['credential' => 'This passkey is already in the panel.']);
        }

        return $this->keep($user, $record, $name, $state['rpId'], $state['handle']);
    }

    /** Keeps a verified passkey of a user. The demonstration data also uses it. */
    public function keep(User $user, CredentialRecord $record, string $name, string $rpId, string $handle): Passkey
    {
        return $user->passkeys()->create([
            'name' => $name,
            'rp_id' => $rpId,
            'credential_hash' => hash('sha256', $record->publicKeyCredentialId),
            'user_handle' => $handle,
            'record' => $this->serializer()->serialize($record, 'json'),
            'backup_eligible' => (bool) $record->backupEligible,
            'backed_up' => (bool) $record->backupStatus,
            'aaguid' => $record->aaguid->toRfc4122(),
        ]);
    }

    /**
     * Starts a sign-in with a passkey. The browser shows the passkeys of the domain: the panel
     * does not ask for an email, so the answer shows no data of a user.
     *
     * @return array{ceremony: string, publicKey: array<string, mixed>}
     */
    public function startSignIn(string $rpId): array
    {
        $options = PublicKeyCredentialRequestOptions::create(
            challenge: random_bytes(32),
            rpId: $rpId,
            userVerification: AuthenticatorSelectionCriteria::USER_VERIFICATION_REQUIREMENT_REQUIRED,
            timeout: self::CEREMONY_MINUTES * 60 * 1000,
        );

        return $this->remember($options, ['purpose' => 'get', 'rpId' => $rpId]);
    }

    /**
     * Ends a sign-in with a passkey. It gives the passkey, with its user, when the answer of the
     * browser is correct.
     *
     * @param  array<string, mixed>  $credential  the JSON of the browser (PublicKeyCredential.toJSON())
     */
    public function finishSignIn(string $ceremony, array $credential): Passkey
    {
        $state = $this->take($ceremony, 'get');
        $publicKey = $this->credential($credential);
        $response = $publicKey->response;
        if (! $response instanceof AuthenticatorAssertionResponse) {
            throw self::invalid();
        }
        $passkey = Passkey::with('user')->where('credential_hash', hash('sha256', $publicKey->rawId))->first();
        // A passkey works only on the domain where the browser made it.
        if (! $passkey || ! $passkey->user || $passkey->rp_id !== $state['rpId']) {
            throw ValidationException::withMessages(['credential' => 'The panel does not know this passkey. Sign in with your password, or use a different passkey.']);
        }
        /** @var PublicKeyCredentialRequestOptions $options */
        $options = $this->serializer()->deserialize($state['options'], PublicKeyCredentialRequestOptions::class, 'json');
        try {
            $record = AuthenticatorAssertionResponseValidator::create($this->ceremonies($passkey->rp_id)->requestCeremony())
                ->check($this->record($passkey), $response, $options, $passkey->rp_id, $response->userHandle);
        } catch (Throwable $e) {
            Log::info("A sign-in with passkey {$passkey->id} is not valid: {$e->getMessage()}");
            throw self::invalid();
        }
        $passkey->forceFill([
            'record' => $this->serializer()->serialize($record, 'json'),
            'backed_up' => (bool) $record->backupStatus,
            'last_used_at' => now(),
        ])->save();

        return $passkey;
    }

    /** The credential record of the library for a passkey. */
    private function record(Passkey $passkey): CredentialRecord
    {
        return $this->serializer()->deserialize($passkey->record, CredentialRecord::class, 'json');
    }

    /** @param array<string, mixed> $credential */
    private function credential(array $credential): PublicKeyCredential
    {
        try {
            $publicKey = $this->serializer()->deserialize((string) json_encode($credential), PublicKeyCredential::class, 'json');
        } catch (Throwable) {
            throw self::invalid();
        }
        if (! $publicKey instanceof PublicKeyCredential || $publicKey->type !== 'public-key') {
            throw self::invalid();
        }

        return $publicKey;
    }

    /**
     * Keeps the options of a ceremony for 5 minutes, and gives them to the browser with the ID
     * of the ceremony.
     *
     * @param  array<string, mixed>  $state
     * @return array{ceremony: string, publicKey: array<string, mixed>}
     */
    private function remember(PublicKeyCredentialCreationOptions|PublicKeyCredentialRequestOptions $options, array $state): array
    {
        $json = $this->serializer()->serialize($options, 'json', [AbstractObjectNormalizer::SKIP_NULL_VALUES => true]);
        $ceremony = Str::random(40);
        Cache::put(self::key($ceremony), $state + ['options' => $json], now()->addMinutes(self::CEREMONY_MINUTES));

        return ['ceremony' => $ceremony, 'publicKey' => json_decode($json, true)];
    }

    /**
     * Takes the state of a ceremony from the cache. A challenge works one time only.
     *
     * @return array<string, mixed>
     */
    private function take(string $ceremony, string $purpose): array
    {
        $state = Cache::pull(self::key($ceremony));
        if (! is_array($state) || ($state['purpose'] ?? null) !== $purpose) {
            throw ValidationException::withMessages(['ceremony' => 'The request for the passkey is too old. Try again.']);
        }

        return $state;
    }

    private static function key(string $ceremony): string
    {
        return 'passkey-ceremony:'.hash('sha256', $ceremony);
    }

    private static function invalid(): ValidationException
    {
        return ValidationException::withMessages(['credential' => 'The panel could not verify the passkey. Try again.']);
    }

    private function ceremonies(string $rpId): CeremonyStepManagerFactory
    {
        $factory = new CeremonyStepManagerFactory;
        // Only the exact origin of the domain, with no subdomain.
        $factory->setAllowedOrigins(self::origins($rpId));
        if ($rpId === 'localhost') {
            $factory->setSecuredRelyingPartyId(['localhost']);
        }
        $factory->setAlgorithmManager(Manager::create()->add(ES256::create(), new EdDSA, RS256::create()));

        return $factory;
    }

    private function serializer(): SerializerInterface
    {
        return $this->serializer ??= (new WebauthnSerializerFactory(AttestationStatementSupportManager::create([new NoneAttestationStatementSupport])))->create();
    }
}
