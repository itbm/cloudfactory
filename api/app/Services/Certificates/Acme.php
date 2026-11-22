<?php

namespace App\Services\Certificates;

use App\Models\Setting;
use Illuminate\Http\Client\ConnectionException;
use Illuminate\Http\Client\Response;
use Illuminate\Support\Facades\Crypt;
use Illuminate\Support\Facades\Http;

/**
 * A small ACME client (RFC 8555) for the certificates with DNS validation. The panel has one
 * account for each directory. The account key is an ECDSA P-256 key, and each request is a
 * JWS with the algorithm ES256.
 *
 * The client does one request at a time and keeps no state between requests, except the
 * account in the settings. AcmeOrders keeps the state of each order in the certificate.
 */
class Acme
{
    /** The setting with the accounts: [directory URL => [url, key]]. The key is encrypted. */
    private const ACCOUNTS = 'acme_accounts';

    private ?array $directory = null;

    private ?string $nonce = null;

    private ?array $account = null;

    public function __construct(private string $directoryUrl, private ?string $email = null) {}

    public static function fromConfig(): self
    {
        $email = trim((string) config('cloudfactory.acme.email'));

        return new self((string) config('cloudfactory.acme.directory'), $email !== '' ? $email : null);
    }

    public function directoryUrl(): string
    {
        return $this->directoryUrl;
    }

    /**
     * Makes a new order for the names.
     *
     * @param  list<string>  $names
     * @return array{url: string, status: string, authorizations: list<string>, finalize: string}
     */
    public function newOrder(array $names): array
    {
        $response = $this->post($this->endpoint('newOrder'), [
            'identifiers' => array_map(fn (string $n) => ['type' => 'dns', 'value' => $n], $names),
        ]);
        $body = (array) $response->json();
        $url = $response->header('Location');
        if ($url === '' || ! is_array($body['authorizations'] ?? null) || ! is_string($body['finalize'] ?? null)) {
            throw new AcmeException('The certificate authority gave an order with no authorizations.');
        }

        return ['url' => $url, 'status' => (string) ($body['status'] ?? ''), 'authorizations' => array_values(array_map('strval', $body['authorizations'])), 'finalize' => $body['finalize']];
    }

    /**
     * Reads an object of the CA, for example an order or an authorization (POST-as-GET).
     *
     * @return array<string, mixed>
     */
    public function read(string $url): array
    {
        return (array) $this->post($url, null)->json();
    }

    /** Tells the CA that the record of a challenge is ready. */
    public function respond(string $challengeUrl): void
    {
        $this->post($challengeUrl, (object) []);
    }

    /**
     * Sends the certificate signing request of an order.
     *
     * @return array<string, mixed> The order.
     */
    public function finalize(string $finalizeUrl, string $csrDer): array
    {
        return (array) $this->post($finalizeUrl, ['csr' => self::base64url($csrDer)])->json();
    }

    /** Downloads the certificate chain of an order in PEM format. */
    public function download(string $certificateUrl): string
    {
        return $this->post($certificateUrl, null, accept: 'application/pem-certificate-chain')->body();
    }

    /**
     * True when browsers trust the chain from the CA. The certificate of a staging CA, for
     * example, has a chain that browsers do not trust.
     *
     * @param  list<string>  $chain
     */
    public function trusted(array $chain): bool
    {
        return Pem::trusted($chain);
    }

    /** The value of the DNS TXT record of a dns-01 challenge (RFC 8555, section 8.4). */
    public function dnsValue(string $token): string
    {
        return self::base64url(hash('sha256', $token.'.'.$this->thumbprint(), true));
    }

    /** The JWK thumbprint of the account key (RFC 7638). */
    public function thumbprint(): string
    {
        $jwk = self::jwk($this->account()['key']);

        return self::base64url(hash('sha256', json_encode(['crv' => $jwk['crv'], 'kty' => $jwk['kty'], 'x' => $jwk['x'], 'y' => $jwk['y']], JSON_UNESCAPED_SLASHES), true));
    }

    /**
     * The account of the panel for this directory. The panel makes the key and the account at
     * the first order. The panel agrees to the terms of service of the CA.
     *
     * @return array{url: string, key: \OpenSSLAsymmetricKey}
     */
    private function account(): array
    {
        if ($this->account !== null) {
            return $this->account;
        }
        $accounts = (array) Setting::get(self::ACCOUNTS, []);
        $saved = $accounts[$this->directoryUrl] ?? null;
        if (is_array($saved) && is_string($saved['url'] ?? null) && is_string($saved['key'] ?? null)) {
            $key = openssl_pkey_get_private(Crypt::decryptString($saved['key']));
            if ($key !== false) {
                return $this->account = ['url' => $saved['url'], 'key' => $key];
            }
        }
        $key = openssl_pkey_new(['private_key_type' => OPENSSL_KEYTYPE_EC, 'curve_name' => 'prime256v1']);
        if ($key === false) {
            throw new AcmeException('The panel could not make an account key.');
        }
        $this->account = ['url' => '', 'key' => $key];
        $response = $this->post($this->endpoint('newAccount'), [
            'termsOfServiceAgreed' => true,
            ...($this->email !== null ? ['contact' => ["mailto:{$this->email}"]] : []),
        ], jwk: true);
        $url = $response->header('Location');
        if ($url === '') {
            throw new AcmeException('The certificate authority did not give an account.');
        }
        openssl_pkey_export($key, $pem);
        $accounts[$this->directoryUrl] = ['url' => $url, 'key' => Crypt::encryptString((string) $pem)];
        Setting::put(self::ACCOUNTS, $accounts);

        return $this->account = ['url' => $url, 'key' => $key];
    }

    private function forgetAccount(): void
    {
        $accounts = (array) Setting::get(self::ACCOUNTS, []);
        unset($accounts[$this->directoryUrl]);
        Setting::put(self::ACCOUNTS, $accounts);
        $this->account = null;
    }

    /** @return array<string, mixed> */
    private function directory(): array
    {
        if ($this->directory === null) {
            try {
                $response = Http::timeout(20)->acceptJson()->get($this->directoryUrl);
            } catch (ConnectionException $e) {
                throw new AcmeException("The panel cannot connect to the certificate authority at {$this->directoryUrl}.");
            }
            if (! $response->successful() || ! is_string($response->json('newOrder'))) {
                throw new AcmeException("The certificate authority at {$this->directoryUrl} did not give its directory (status {$response->status()}).");
            }
            $this->directory = (array) $response->json();
        }

        return $this->directory;
    }

    private function endpoint(string $name): string
    {
        $url = $this->directory()[$name] ?? null;
        if (! is_string($url)) {
            throw new AcmeException("The certificate authority has no {$name} address.");
        }

        return $url;
    }

    private function nonce(): string
    {
        if ($this->nonce !== null) {
            $nonce = $this->nonce;
            $this->nonce = null;

            return $nonce;
        }
        try {
            $response = Http::timeout(20)->head($this->endpoint('newNonce'));
        } catch (ConnectionException) {
            throw new AcmeException('The panel cannot connect to the certificate authority.');
        }
        $nonce = $response->header('Replay-Nonce');
        if ($nonce === '') {
            throw new AcmeException('The certificate authority gave no nonce.');
        }

        return $nonce;
    }

    /**
     * Sends a signed request. A null payload is a POST-as-GET. The client tries again one time
     * when the CA refuses the nonce.
     */
    private function post(string $url, array|object|null $payload, bool $jwk = false, string $accept = 'application/json'): Response
    {
        for ($try = 0; ; $try++) {
            $account = $jwk ? $this->account : $this->account();
            $protected = ['alg' => 'ES256', 'nonce' => $this->nonce(), 'url' => $url];
            if ($jwk) {
                $protected['jwk'] = self::jwk($account['key']);
            } else {
                $protected['kid'] = $account['url'];
            }
            $body = self::sign($account['key'], $protected, $payload);
            try {
                $response = Http::timeout(30)->withHeaders(['Accept' => $accept])
                    ->withBody(json_encode($body, JSON_UNESCAPED_SLASHES), 'application/jose+json')->post($url);
            } catch (ConnectionException) {
                throw new AcmeException('The panel cannot connect to the certificate authority.');
            }
            if ($response->header('Replay-Nonce') !== '') {
                $this->nonce = $response->header('Replay-Nonce');
            }
            if ($response->successful()) {
                return $response;
            }
            $type = (string) $response->json('type');
            if ($try === 0 && $type === 'urn:ietf:params:acme:error:badNonce') {
                continue;
            }
            // The CA does not know the saved account, for example after a reset of a test CA.
            // The panel makes a new account and tries again one time.
            if ($try === 0 && ! $jwk && $type === 'urn:ietf:params:acme:error:accountDoesNotExist') {
                $this->forgetAccount();

                continue;
            }
            throw new AcmeException(self::problem($response));
        }
    }

    /** The text of an ACME problem document, for the user. */
    public static function problem(Response $response): string
    {
        $detail = trim((string) $response->json('detail'));
        $type = str_replace('urn:ietf:params:acme:error:', '', (string) $response->json('type'));
        $sub = collect((array) $response->json('subproblems'))->map(fn ($p) => trim(($p['identifier']['value'] ?? '').': '.($p['detail'] ?? '')))->filter()->implode(' ');

        return trim('The certificate authority refused the request'.($type !== '' ? " ({$type})" : '').': '.($detail !== '' ? $detail : "status {$response->status()}").($sub !== '' ? " {$sub}" : ''));
    }

    /**
     * A JWS in the flattened JSON form.
     *
     * @return array{protected: string, payload: string, signature: string}
     */
    public static function sign(\OpenSSLAsymmetricKey $key, array $protected, array|object|null $payload): array
    {
        $header = self::base64url(json_encode($protected, JSON_UNESCAPED_SLASHES));
        $body = $payload === null ? '' : self::base64url(json_encode($payload, JSON_UNESCAPED_SLASHES));
        if (! openssl_sign("{$header}.{$body}", $der, $key, OPENSSL_ALGO_SHA256)) {
            throw new AcmeException('The panel could not sign the request.');
        }

        return ['protected' => $header, 'payload' => $body, 'signature' => self::base64url(self::rawSignature($der))];
    }

    /**
     * The public key as a JWK.
     *
     * @return array{kty: string, crv: string, x: string, y: string}
     */
    public static function jwk(\OpenSSLAsymmetricKey $key): array
    {
        $ec = openssl_pkey_get_details($key)['ec'] ?? [];

        return [
            'kty' => 'EC',
            'crv' => 'P-256',
            'x' => self::base64url(str_pad((string) ($ec['x'] ?? ''), 32, "\0", STR_PAD_LEFT)),
            'y' => self::base64url(str_pad((string) ($ec['y'] ?? ''), 32, "\0", STR_PAD_LEFT)),
        ];
    }

    /** Converts an ECDSA signature in DER format to the 64 bytes R || S of JWS. */
    public static function rawSignature(string $der): string
    {
        $offset = 2;
        if (ord($der[1]) & 0x80) {
            $offset += ord($der[1]) & 0x7F;
        }
        $parts = [];
        for ($i = 0; $i < 2; $i++) {
            if (ord($der[$offset]) !== 0x02) {
                throw new AcmeException('The signature is not in DER format.');
            }
            $length = ord($der[$offset + 1]);
            $value = substr($der, $offset + 2, $length);
            $offset += 2 + $length;
            $parts[] = str_pad(ltrim($value, "\0"), 32, "\0", STR_PAD_LEFT);
        }

        return $parts[0].$parts[1];
    }

    public static function base64url(string $data): string
    {
        return rtrim(strtr(base64_encode($data), '+/', '-_'), '=');
    }

    /**
     * A certificate signing request for the names, in DER format.
     *
     * @param  list<string>  $names
     */
    public static function csr(\OpenSSLAsymmetricKey $key, array $names): string
    {
        $config = tempnam(sys_get_temp_dir(), 'cf-csr');
        try {
            file_put_contents($config, "[req]\ndistinguished_name = dn\nprompt = no\n[dn]\n[san]\nsubjectAltName = ".implode(',', array_map(fn ($n) => "DNS:{$n}", $names))."\n");
            // The common name is the first name that fits (64 characters). The CA uses the
            // subject alternative names.
            $common = collect($names)->first(fn (string $n) => strlen($n) <= 64) ?? substr($names[0], 0, 64);
            $csr = openssl_csr_new(['commonName' => $common], $key, ['config' => $config, 'req_extensions' => 'san', 'digest_alg' => 'sha256']);
            if (! $csr instanceof \OpenSSLCertificateSigningRequest || ! openssl_csr_export($csr, $pem)) {
                throw new AcmeException('The panel could not make the certificate signing request: '.openssl_error_string());
            }
        } finally {
            @unlink($config);
        }

        return base64_decode(preg_replace('/-----[A-Z ]+-----|\s/', '', (string) $pem));
    }
}
