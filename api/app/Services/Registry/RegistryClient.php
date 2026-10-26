<?php

namespace App\Services\Registry;

use Illuminate\Http\Client\PendingRequest;
use Illuminate\Http\Client\Response;
use Illuminate\Support\Facades\Http;
use RuntimeException;

/**
 * A small client for the OCI distribution API. It reads tags, manifest digests and cosign
 * signatures from a public registry, for example ghcr.io or Docker Hub (FR-1110).
 */
class RegistryClient
{
    private const MANIFEST_TYPES = [
        'application/vnd.oci.image.index.v1+json',
        'application/vnd.docker.distribution.manifest.list.v2+json',
        'application/vnd.oci.image.manifest.v1+json',
        'application/vnd.docker.distribution.manifest.v2+json',
    ];

    /** @var array<string, string> Bearer tokens by repository. */
    private array $tokens = [];

    /**
     * Splits an image reference into the API host and the repository.
     *
     * @return array{0: string, 1: string}
     */
    public static function parse(string $reference): array
    {
        $parts = explode('/', $reference, 2);
        if (count($parts) === 2 && (str_contains($parts[0], '.') || str_contains($parts[0], ':') || $parts[0] === 'localhost')) {
            [$host, $repository] = $parts;
        } else {
            [$host, $repository] = ['docker.io', $reference];
        }
        if ($host === 'docker.io') {
            $host = 'registry-1.docker.io';
            if (! str_contains($repository, '/')) {
                $repository = 'library/'.$repository;
            }
        }

        return [$host, $repository];
    }

    /** @return list<string> All tags of the image. */
    public function tags(string $reference): array
    {
        [$host, $repository] = self::parse($reference);
        $tags = [];
        $path = "/v2/{$repository}/tags/list?n=1000";
        // The registry gives the next page in a Link header. Stop after 20 pages.
        for ($page = 0; $path !== null && $page < 20; $page++) {
            $response = $this->get($host, $repository, $path);
            $tags = [...$tags, ...($response->json('tags') ?? [])];
            $path = self::nextPage($response->header('Link'));
        }

        return array_values(array_filter($tags, 'is_string'));
    }

    /** Gives the digest of a tag, or null when the tag does not exist. */
    public function digest(string $reference, string $tag): ?string
    {
        [$host, $repository] = self::parse($reference);
        $response = $this->request($host, $repository, 'head', "/v2/{$repository}/manifests/{$tag}");
        if ($response->status() === 404) {
            return null;
        }
        $this->check($response, "{$reference}:{$tag}");
        $digest = $response->header('Docker-Content-Digest');
        if (! preg_match('/^sha256:[a-f0-9]{64}$/', $digest)) {
            throw new RuntimeException("The registry did not give a digest for {$reference}:{$tag}.");
        }

        return $digest;
    }

    /**
     * Gives the cosign signatures of a digest: the signed payload and the signature of each
     * layer of the "sha256-<hex>.sig" tag.
     *
     * @return list<array{payload: string, signature: string}>
     */
    public function signatures(string $reference, string $digest): array
    {
        [$host, $repository] = self::parse($reference);
        $tag = str_replace(':', '-', $digest).'.sig';
        $response = $this->request($host, $repository, 'get', "/v2/{$repository}/manifests/{$tag}", [
            'Accept' => 'application/vnd.oci.image.manifest.v1+json, application/vnd.docker.distribution.manifest.v2+json',
        ]);
        if ($response->status() === 404) {
            return [];
        }
        $this->check($response, "{$reference}:{$tag}");

        $signatures = [];
        foreach (array_slice($response->json('layers') ?? [], 0, 10) as $layer) {
            $signature = $layer['annotations']['dev.cosignproject.cosign/signature'] ?? null;
            $blob = $layer['digest'] ?? '';
            if (! is_string($signature) || ! preg_match('/^sha256:[a-f0-9]{64}$/', $blob)) {
                continue;
            }
            $payload = $this->get($host, $repository, "/v2/{$repository}/blobs/{$blob}")->body();
            if (hash('sha256', $payload) !== substr($blob, 7)) {
                continue;
            }
            $signatures[] = ['payload' => $payload, 'signature' => $signature];
        }

        return $signatures;
    }

    private function get(string $host, string $repository, string $path): Response
    {
        $response = $this->request($host, $repository, 'get', $path);
        $this->check($response, $repository);

        return $response;
    }

    private function request(string $host, string $repository, string $method, string $path, array $headers = []): Response
    {
        $headers += ['Accept' => implode(', ', self::MANIFEST_TYPES)];
        $response = $this->client($repository)->withHeaders($headers)->{$method}("https://{$host}{$path}");
        if ($response->status() === 401 && ! isset($this->tokens[$repository])) {
            $this->tokens[$repository] = $this->token($response->header('WWW-Authenticate'), $repository);
            $response = $this->client($repository)->withHeaders($headers)->{$method}("https://{$host}{$path}");
        }

        return $response;
    }

    private function client(string $repository): PendingRequest
    {
        $client = Http::timeout(15)->withOptions(['allow_redirects' => ['max' => 3, 'protocols' => ['https']]]);

        return isset($this->tokens[$repository]) ? $client->withToken($this->tokens[$repository]) : $client;
    }

    /** Gets an anonymous pull token from the realm in the challenge of the registry. */
    private function token(string $challenge, string $repository): string
    {
        if (! preg_match('/^Bearer\s+(.+)$/i', $challenge, $m)) {
            throw new RuntimeException('The registry needs a sign-in method that the panel does not support.');
        }
        preg_match_all('/(\w+)="([^"]*)"/', $m[1], $pairs, PREG_SET_ORDER);
        $params = [];
        foreach ($pairs as [, $key, $value]) {
            $params[$key] = $value;
        }
        if (! isset($params['realm']) || ! str_starts_with($params['realm'], 'https://')) {
            throw new RuntimeException('The registry gave a sign-in address that is not HTTPS.');
        }
        $response = Http::timeout(15)->get($params['realm'], array_filter([
            'service' => $params['service'] ?? null,
            'scope' => "repository:{$repository}:pull",
        ]));
        $this->check($response, $repository);
        $token = $response->json('token') ?? $response->json('access_token');
        if (! is_string($token) || $token === '') {
            throw new RuntimeException('The registry did not give a pull token.');
        }

        return $token;
    }

    private function check(Response $response, string $what): void
    {
        if (! $response->successful()) {
            throw new RuntimeException("The registry gave status {$response->status()} for {$what}.");
        }
    }

    private static function nextPage(?string $link): ?string
    {
        if ($link && preg_match('/<([^>]+)>;\s*rel="next"/', $link, $m) && str_starts_with($m[1], '/v2/')) {
            return $m[1];
        }

        return null;
    }
}
