<?php

namespace Tests\Feature;

use App\Models\AgentJob;
use App\Models\AuditEntry;
use App\Models\Image;
use App\Models\Rollout;
use App\Services\Registry\RegistryClient;
use Illuminate\Http\Client\Request;
use Illuminate\Support\Facades\Http;
use Tests\TestCase;

/** FR-1110: the panel finds new signed versions of each image in its registry. */
class ImageCheckTest extends TestCase
{
    private const REPO = 'cloudfactory/frankenphp-php8.4';

    private \OpenSSLAsymmetricKey $key;

    /** @var array<string, string> Digest of each tag in the fake registry. */
    private array $tags = [];

    /** @var array<string, string> Signed payload of each digest. */
    private array $signed = [];

    protected function setUp(): void
    {
        parent::setUp();
        $this->key = openssl_pkey_new(['private_key_type' => OPENSSL_KEYTYPE_EC, 'curve_name' => 'prime256v1']);
        $path = sys_get_temp_dir().'/cf-test-cosign-'.getmypid().'.pub';
        file_put_contents($path, openssl_pkey_get_details($this->key)['key']);
        config(['cloudfactory.cosign_public_key' => $path]);
        $this->actingWith($this->user());
        $this->fakeRegistry();
    }

    /** Adds a tag. $signer is the key that signs it, or null for no signature. */
    private function push(string $tag, ?\OpenSSLAsymmetricKey $signer = null, string $repo = self::REPO): string
    {
        $digest = 'sha256:'.hash('sha256', $repo.$tag);
        $this->tags[$tag] = $digest;
        if ($signer) {
            $payload = json_encode(['critical' => [
                'identity' => ['docker-reference' => "ghcr.io/{$repo}"],
                'image' => ['docker-manifest-digest' => $digest],
                'type' => 'cosign container image signature',
            ], 'optional' => null]);
            openssl_sign($payload, $signature, $signer, OPENSSL_ALGO_SHA256);
            $this->signed[$digest] = [$payload, base64_encode($signature)];
        }

        return $digest;
    }

    private function fakeRegistry(): void
    {
        Http::fake(function (Request $request) {
            $url = $request->url();
            if (str_starts_with($url, 'https://ghcr.io/token')) {
                return Http::response(['token' => 'pull-token']);
            }
            if (! $request->hasHeader('Authorization', 'Bearer pull-token')) {
                return Http::response('', 401, ['WWW-Authenticate' => 'Bearer realm="https://ghcr.io/token",service="ghcr.io",scope="repository:x:pull"']);
            }
            $base = 'https://ghcr.io/v2/'.self::REPO;
            if (str_starts_with($url, "{$base}/tags/list")) {
                return Http::response(['name' => self::REPO, 'tags' => [...array_keys($this->tags), 'latest', 'sha256-abc.sig']]);
            }
            if (preg_match('#/manifests/(sha256-([a-f0-9]{64})\.sig)$#', $url, $m)) {
                $signed = $this->signed['sha256:'.$m[2]] ?? null;
                if (! $signed) {
                    return Http::response('', 404);
                }

                return Http::response(['schemaVersion' => 2, 'layers' => [[
                    'mediaType' => 'application/vnd.dev.cosign.simplesigning.v1+json',
                    'digest' => 'sha256:'.hash('sha256', $signed[0]),
                    'annotations' => ['dev.cosignproject.cosign/signature' => $signed[1]],
                ]]]);
            }
            if (preg_match('#/manifests/([^/]+)$#', $url, $m)) {
                $digest = $this->tags[$m[1]] ?? null;

                return $digest ? Http::response('', 200, ['Docker-Content-Digest' => $digest]) : Http::response('', 404);
            }
            if (preg_match('#/blobs/sha256:([a-f0-9]{64})$#', $url, $m)) {
                foreach ($this->signed as [$payload]) {
                    if (hash('sha256', $payload) === $m[1]) {
                        return Http::response($payload);
                    }
                }
            }

            return Http::response('', 404);
        });
    }

    private function official(array $attributes = []): Image
    {
        return $this->image($attributes + ['reference' => 'ghcr.io/'.self::REPO, 'tag' => '0.1.0', 'policy' => 'manual']);
    }

    public function test_it_offers_the_newest_signed_tag(): void
    {
        $image = $this->official();
        $this->push('0.1.0', $this->key);
        $this->push('0.1.0-r7', $this->key);
        $digest = $this->push('0.2.0', $this->key);
        $this->push('0.0.9', $this->key);

        $this->artisan('cloudfactory:check-images')->assertSuccessful();

        $image->refresh();
        $this->assertSame('0.2.0', $image->available_tag);
        $this->assertSame($digest, $image->available_digest);
        $this->assertNotNull($image->checked_at);
        $this->assertNull($image->check_error);
        $this->assertSame(0, Rollout::count(), 'a manual image must not start a rollout');
    }

    public function test_it_refuses_a_tag_without_a_good_signature(): void
    {
        $image = $this->official();
        $this->push('0.1.0', $this->key);
        $good = $this->push('0.1.0-r3', $this->key);
        $this->push('0.1.0-r4');
        $wrongKey = openssl_pkey_new(['private_key_type' => OPENSSL_KEYTYPE_EC, 'curve_name' => 'prime256v1']);
        $this->push('0.1.0-r5', $wrongKey);

        $this->artisan('cloudfactory:check-images')->assertSuccessful();

        $image->refresh();
        $this->assertSame('0.1.0-r3', $image->available_tag);
        $this->assertSame($good, $image->available_digest);
        $this->assertSame(2, AuditEntry::where('action', 'Refused image tag without a good signature')->count());
    }

    public function test_security_automatic_starts_a_rollout_for_a_rebuild_only(): void
    {
        $rebuild = $this->official(['policy' => 'security_automatic']);
        $this->website($this->customer(), $this->server(), $rebuild);
        $this->push('0.1.0-r2', $this->key);

        $this->artisan('cloudfactory:check-images')->assertSuccessful();
        $this->assertSame(1, Rollout::where('image_id', $rebuild->id)->count());
        $this->assertSame('0.1.0-r2', Rollout::first()->to_tag);

        // A new release waits for an administrator.
        Rollout::query()->update(['state' => 'done']);
        $rebuild->update(['tag' => '0.1.0-r2', 'available_tag' => null, 'available_digest' => null]);
        $this->push('0.2.0', $this->key);
        $this->artisan('cloudfactory:check-images')->assertSuccessful();
        $this->assertSame('0.2.0', $rebuild->fresh()->available_tag);
        $this->assertSame(1, Rollout::count());
    }

    public function test_a_custom_image_gets_a_new_digest_of_the_same_tag(): void
    {
        $image = $this->image(['reference' => 'ghcr.io/'.self::REPO, 'tag' => 'stable', 'family' => 'custom', 'custom' => true, 'policy' => 'automatic', 'signature_verified' => false]);
        $website = $this->website($this->customer(), $this->server(), $image);
        $first = $this->push('stable');

        // The first check only records the digest.
        $this->artisan('cloudfactory:check-images')->assertSuccessful();
        $this->assertSame($first, $image->fresh()->digest);
        $this->assertNull($image->fresh()->available_tag);

        $this->tags['stable'] = $second = 'sha256:'.str_repeat('b', 64);
        $this->artisan('cloudfactory:check-images')->assertSuccessful();

        $image->refresh();
        $this->assertSame('stable', $image->available_tag);
        $this->assertSame($second, $image->available_digest);
        $rollout = Rollout::sole();
        $this->assertSame('stable', $rollout->to_tag);
        $this->assertTrue(AgentJob::where('rollout_id', $rollout->id)->get()->contains(
            fn ($job) => $job->payload['website']['imageDigest'] === $second && $job->website_id === $website->id
        ));
    }

    public function test_a_custom_image_shows_an_update_on_the_website(): void
    {
        $image = $this->image(['reference' => 'ghcr.io/'.self::REPO, 'tag' => 'stable', 'family' => 'custom', 'custom' => true, 'policy' => 'manual', 'digest' => 'sha256:'.str_repeat('a', 64)]);
        $website = $this->website($this->customer(), $this->server(), $image);
        $this->push('stable');

        $this->artisan('cloudfactory:check-images')->assertSuccessful();

        $this->assertTrue($website->fresh()->updateReady());
    }

    public function test_it_records_an_error_and_continues(): void
    {
        $missing = $this->image(['reference' => 'ghcr.io/'.self::REPO, 'tag' => 'gone', 'family' => 'custom', 'custom' => true, 'name' => 'Gone']);
        $official = $this->official(['name' => 'Official']);
        $this->push('0.1.1', $this->key);

        $this->artisan('cloudfactory:check-images')->assertFailed();

        $this->assertStringContainsString('gone', $missing->fresh()->check_error);
        $this->assertSame('0.1.1', $official->fresh()->available_tag);
    }

    public function test_without_the_release_key_it_accepts_no_official_tag(): void
    {
        config(['cloudfactory.cosign_public_key' => '/nonexistent/cosign.pub']);
        $image = $this->official();
        $this->push('0.2.0', $this->key);

        $this->artisan('cloudfactory:check-images')->assertFailed();

        $this->assertNull($image->fresh()->available_tag);
        $this->assertStringContainsString('public key', $image->fresh()->check_error);
    }

    public function test_an_administrator_can_check_one_image_now(): void
    {
        $image = $this->official();
        $this->push('0.1.2', $this->key);

        $this->postJson("/api/v1/images/{$image->id}/check")
            ->assertOk()
            ->assertJsonPath('availableTag', '0.1.2')
            ->assertJsonPath('checkError', null);
    }

    public function test_the_registry_reference_is_parsed(): void
    {
        $parse = [RegistryClient::class, 'parse'];
        $this->assertSame(['ghcr.io', 'itbm/edge'], $parse('ghcr.io/itbm/edge'));
        $this->assertSame(['registry-1.docker.io', 'library/nginx'], $parse('nginx'));
        $this->assertSame(['registry-1.docker.io', 'bitnami/nginx'], $parse('docker.io/bitnami/nginx'));
        $this->assertSame(['localhost:5000', 'app'], $parse('localhost:5000/app'));
    }
}
