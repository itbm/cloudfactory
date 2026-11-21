<?php

namespace Tests\Feature;

use App\Enums\AgentAction;
use App\Enums\JobState;
use App\Enums\Role;
use App\Jobs\SendJobToAgent;
use App\Models\AgentJob;
use App\Models\Image;
use App\Models\Pack;
use App\Models\Server;
use App\Services\AgentClient;
use App\Services\JobOutcome;
use App\Services\Packs\PackManifest;
use Illuminate\Http\Client\Request as HttpRequest;
use Illuminate\Support\Collection;
use Illuminate\Support\Facades\Http;
use Illuminate\Validation\ValidationException;
use Tests\TestCase;

/**
 * Packs (docs/packs.md): a signed manifest from a publisher adds a website runtime. The panel
 * installs a pack only when a trusted key signed it, keeps the exact bytes of the manifest,
 * and sends all its packs to each web server. Each version of the runtime is an image.
 */
class PackTest extends TestCase
{
    /** @var array{private: \OpenSSLAsymmetricKey, public: string} */
    private array $key;

    protected function setUp(): void
    {
        parent::setUp();
        $this->key = self::newKey();
    }

    /** @return array{private: \OpenSSLAsymmetricKey, public: string} */
    private static function newKey(): array
    {
        $private = openssl_pkey_new(['private_key_type' => OPENSSL_KEYTYPE_EC, 'curve_name' => 'prime256v1']);

        return ['private' => $private, 'public' => openssl_pkey_get_details($private)['key']];
    }

    /** The example manifest of the repository, with changes. */
    private function manifest(array $changes = [], array $images = []): string
    {
        $m = json_decode((string) file_get_contents(base_path('../plugins/packs/golang/pack.json')), true);
        foreach ($images as $i => $image) {
            $m['images'][$i] = $image === null ? null : array_merge($m['images'][$i] ?? [], $image);
        }
        $m['images'] = array_values(array_filter($m['images']));

        return json_encode(array_merge($m, $changes), JSON_PRETTY_PRINT | JSON_UNESCAPED_SLASHES)."\n";
    }

    private function sign(string $manifest, ?array $key = null): string
    {
        openssl_sign($manifest, $signature, ($key ?? $this->key)['private'], OPENSSL_ALGO_SHA256);

        return base64_encode($signature);
    }

    private function admin(): void
    {
        $this->actingWith($this->user(Role::Administrator));
    }

    private function trustKey(): string
    {
        return $this->postJson('/api/v1/trusted-keys', ['name' => 'Example Packs Ltd', 'publicKey' => $this->key['public'], 'password' => 'password'])
            ->assertCreated()->json('id');
    }

    /** @return array<string, mixed> */
    private function install(?string $manifest = null): array
    {
        $manifest ??= $this->manifest();

        return $this->postJson('/api/v1/packs', ['manifest' => $manifest, 'signature' => $this->sign($manifest), 'password' => 'password'])
            ->assertCreated()->json();
    }

    /** The agent of the server sends a report with its packs. */
    private function report(Server $server, array $packs): void
    {
        $server->update(['agent_token_hash' => hash('sha256', "cfa_{$server->id}"), 'local' => true]);
        config(['cloudfactory.agent.direct_addresses' => ['127.0.0.1']]);
        $this->withToken("cfa_{$server->id}")->postJson('/api/agent/v1/report', ['agentVersion' => '0.1.0', 'packs' => $packs])->assertOk();
    }

    /** @return Collection<int, AgentJob> */
    private function packJobs(): Collection
    {
        return AgentJob::where('action', AgentAction::SetPacks->value)->orderBy('created_at')->get();
    }

    public function test_an_administrator_installs_a_signed_pack_and_each_web_server_gets_it(): void
    {
        $web = $this->server(['name' => 'web-1']);
        $second = $this->server(['name' => 'web-2']);
        $this->server(['name' => 'db-1', 'roles' => ['database']]);
        $this->admin();
        $this->trustKey();
        $manifest = $this->manifest();

        // The review before the install: what the pack adds and who signed it.
        $this->postJson('/api/v1/packs/preview', ['manifest' => $manifest, 'signature' => $this->sign($manifest)])->assertOk()
            ->assertJsonPath('manifest.slug', 'golang')
            ->assertJsonPath('manifest.images.0.repository', 'ghcr.io/example/cf-golang')
            ->assertJsonPath('manifest.images.0.tag', '1.27')
            ->assertJsonPath('signedBy.name', 'Example Packs Ltd')
            ->assertJsonPath('installed', null);

        $pack = $this->install($manifest);
        $this->assertSame(['golang', '1.0.0', true, 'Example Packs Ltd'], [$pack['slug'], $pack['version'], $pack['signed'], $pack['signedBy']['name']]);
        $this->assertSame(['1.27', '1.26'], array_column($pack['images'], 'version'));
        $this->assertSame(['pending', 'pending'], array_column($pack['servers'], 'state'));

        // The panel keeps the exact bytes that the publisher signed.
        $this->assertSame($manifest, Pack::firstOrFail()->manifest);

        // Each version is an image that runs the start command of the website.
        $image = Image::where('runtime_version', '1.27')->firstOrFail();
        $this->assertSame(['golang', 'ghcr.io/example/cf-golang', '1.27', 'sha256:'.str_repeat('0', 60).'0127', false, true],
            [$image->family, $image->reference, $image->tag, $image->digest, $image->custom, $image->signature_verified]);
        $this->assertTrue($image->isApp());
        $this->assertSame(['command' => './server', 'install' => 'go build -o server .'], $image->appDefaults());

        // One job for each web server. The job has no packs: the panel adds them when it sends it.
        $jobs = $this->packJobs();
        $this->assertEqualsCanonicalizing([$web->id, $second->id], $jobs->pluck('server_id')->all());
        $this->assertSame([], $jobs->first()->payload);
        $this->assertDatabaseHas('audit_entries', ['action' => 'Installed pack', 'target' => 'Go 1.0.0 (signed by Example Packs Ltd)']);
    }

    public function test_the_agent_gets_the_manifest_the_signature_and_the_key(): void
    {
        config(['cloudfactory.agent.certificates' => sys_get_temp_dir().'/cf-test-agents-'.uniqid()]);
        $server = $this->server(['agent_url' => 'https://web2:7443', 'certificate_pem' => 'x', 'certificate_fingerprint' => str_repeat('a', 64)]);
        $this->admin();
        $this->trustKey();
        $manifest = $this->manifest();
        $this->install($manifest);
        $job = $this->packJobs()->firstOrFail();

        $pack = Pack::firstOrFail();

        Http::fake(['*' => Http::response(['jobId' => $job->id])]);
        (new SendJobToAgent($job->id))->handle(app(AgentClient::class));
        Http::assertSent(function (HttpRequest $request) use ($job, $manifest, $pack) {
            $packs = $request->data()['packs'] ?? null;

            return str_ends_with($request->url(), '/cloudfactory.agent.v1.AgentService/SetPacks') && $request->data()['jobId'] === $job->id
                && is_array($packs) && count($packs) === 1 && $packs[0]['manifest'] === $manifest && $packs[0]['signature'] === $pack->signature
                && $packs[0]['publicKey'] === trim($this->key['public'])."\n";
        });
        $this->assertSame($server->id, $job->server_id);
    }

    public function test_the_panel_refuses_a_pack_with_no_trusted_signature(): void
    {
        $this->server();
        $this->admin();
        $manifest = $this->manifest();

        // No signature, and no trusted key.
        $this->postJson('/api/v1/packs', ['manifest' => $manifest, 'password' => 'password'])->assertJsonValidationErrors('signature');
        $this->postJson('/api/v1/packs', ['manifest' => $manifest, 'signature' => $this->sign($manifest), 'password' => 'password'])
            ->assertJsonValidationErrors(['signature' => 'No trusted key signed this manifest. Add the public key of the publisher in Packs > Trusted keys, or check that the manifest has no change.']);
        $this->trustKey();
        // A change of one byte after the signature, and a signature of a different key.
        $this->postJson('/api/v1/packs', ['manifest' => str_replace('"Go"', '"Go!"', $manifest), 'signature' => $this->sign($manifest), 'password' => 'password'])
            ->assertJsonValidationErrors('signature');
        $this->postJson('/api/v1/packs', ['manifest' => $manifest, 'signature' => $this->sign($manifest, self::newKey()), 'password' => 'password'])
            ->assertJsonValidationErrors('signature');
        // The password confirms the install.
        $this->postJson('/api/v1/packs', ['manifest' => $manifest, 'signature' => $this->sign($manifest)])->assertJsonValidationErrors('password');
        $this->assertSame(0, Pack::count());
        $this->assertSame(0, $this->packJobs()->count());
    }

    public function test_the_panel_and_the_agent_accept_the_same_manifests(): void
    {
        $base = json_decode((string) file_get_contents(base_path('../plugins/packs/golang/pack.json')), true);
        $file = json_decode((string) file_get_contents(base_path('../schema/pack-vectors.json')), true);
        $this->assertGreaterThanOrEqual(40, count($file['cases']));
        foreach ($file['cases'] as $case) {
            if (isset($case['raw'])) {
                $raw = $case['raw'];
            } elseif (! isset($case['set']) && ! isset($case['delete'])) {
                $raw = (string) file_get_contents(base_path('../plugins/packs/golang/pack.json'));
            } else {
                $m = $base;
                foreach ($case['set'] ?? [] as $path => $value) {
                    $ref = &$m;
                    foreach (explode('.', $path) as $key) {
                        $ref = &$ref[$key];
                    }
                    $ref = $value;
                    unset($ref);
                }
                foreach ($case['delete'] ?? [] as $path) {
                    $keys = explode('.', $path);
                    $last = array_pop($keys);
                    $ref = &$m;
                    foreach ($keys as $key) {
                        $ref = &$ref[$key];
                    }
                    unset($ref[$last]);
                    unset($ref);
                }
                $raw = json_encode($m, JSON_UNESCAPED_SLASHES | JSON_UNESCAPED_UNICODE);
            }
            try {
                PackManifest::parse($raw);
                $this->assertTrue($case['valid'], "{$case['name']}: the panel accepts the manifest.");
            } catch (ValidationException $e) {
                $this->assertFalse($case['valid'], "{$case['name']}: ".json_encode($e->errors()));
            }
        }
    }

    public function test_the_developer_mode_accepts_a_pack_with_no_signature(): void
    {
        $this->server();
        $this->admin();
        // The developer mode needs the password.
        $this->putJson('/api/v1/pack-settings', ['developerMode' => true])->assertJsonValidationErrors('password');
        $this->putJson('/api/v1/pack-settings', ['developerMode' => true, 'password' => 'password'])->assertOk()->assertJsonPath('developerMode', true);

        $this->postJson('/api/v1/packs', ['manifest' => $this->manifest(), 'password' => 'password'])->assertCreated()
            ->assertJsonPath('signed', false)->assertJsonPath('signedBy', null);
        $pack = Pack::firstOrFail();
        $this->assertSame(['manifest' => $pack->manifest, 'signature' => '', 'publicKey' => ''], $pack->forAgent());
        $this->assertFalse(Image::where('pack_id', $pack->id)->firstOrFail()->signature_verified);
        $this->assertDatabaseHas('audit_entries', ['action' => 'Installed pack', 'target' => 'Go 1.0.0 (no signature)']);
    }

    public function test_the_release_key_of_cloudfactory_signs_official_packs(): void
    {
        $release = self::newKey();
        $path = sys_get_temp_dir().'/cf-release-'.uniqid().'.pub';
        file_put_contents($path, $release['public']);
        config(['cloudfactory.cosign_public_key' => $path]);
        $this->server();
        $this->admin();
        $manifest = $this->manifest(['slug' => 'zig', 'name' => 'Zig'], [['ref' => 'ghcr.io/itbm/cf-zig:1.27'], ['ref' => 'ghcr.io/itbm/cf-zig:1.26']]);

        $this->postJson('/api/v1/packs', ['manifest' => $manifest, 'signature' => $this->sign($manifest, $release), 'password' => 'password'])->assertCreated()
            ->assertJsonPath('signedBy', ['name' => Pack::RELEASE_KEY, 'fingerprint' => null, 'release' => true]);
        $this->assertSame(trim($release['public'])."\n", Pack::firstOrFail()->public_key);
        unlink($path);
    }

    public function test_an_update_changes_the_images_and_keeps_the_versions_that_websites_use(): void
    {
        $server = $this->server(['agent_runtimes' => ['frankenphp', 'node', 'golang']]);
        $this->admin();
        $this->trustKey();
        $this->install();
        $pack = Pack::firstOrFail();
        $go127 = Image::where('runtime_version', '1.27')->firstOrFail();
        $go126 = Image::where('runtime_version', '1.26')->firstOrFail();
        $this->website($this->customer(), $server, $go127, 'api.acme.example');

        // The same version is installed.
        $same = $this->manifest();
        $this->putJson("/api/v1/packs/{$pack->id}", ['manifest' => $same, 'signature' => $this->sign($same), 'password' => 'password'])
            ->assertJsonValidationErrors(['manifest' => 'The version 1.0.0 is installed. Give a manifest with a new version.']);
        // A manifest of a different pack.
        $other = $this->manifest(['slug' => 'zig', 'version' => '2.0.0']);
        $this->putJson("/api/v1/packs/{$pack->id}", ['manifest' => $other, 'signature' => $this->sign($other), 'password' => 'password'])
            ->assertJsonValidationErrors('manifest');
        // The new version removes Go 1.27, and a website uses it.
        $gone = $this->manifest(['version' => '1.1.0'], [null]);
        $this->putJson("/api/v1/packs/{$pack->id}", ['manifest' => $gone, 'signature' => $this->sign($gone), 'password' => 'password'])
            ->assertJsonValidationErrors(['manifest' => 'The new version has no image for Go 1.27, and websites use it. Change the runtime of these websites first.']);

        // New digests for both versions, and a new version 1.28.
        $digest = fn (string $n) => 'sha256:'.str_repeat('1', 60).$n;
        $new = $this->manifest(['version' => '1.1.0'], [
            ['ref' => 'ghcr.io/example/cf-golang:1.27.1', 'digest' => $digest('0127')],
            ['digest' => $digest('0126')],
            ['version' => '1.28', 'ref' => 'ghcr.io/example/cf-golang:1.28', 'digest' => $digest('0128'), 'supportEndsAt' => '2028-02-28'],
        ]);
        AgentJob::query()->delete();
        $this->putJson("/api/v1/packs/{$pack->id}", ['manifest' => $new, 'signature' => $this->sign($new), 'password' => 'password'])->assertOk()
            ->assertJsonPath('version', '1.1.0')
            ->assertJsonPath('images.0.version', '1.28');

        // The version with a website waits for a rollout. The other one changes at once.
        $go127->refresh();
        $this->assertSame(['1.27', 'sha256:'.str_repeat('0', 60).'0127', '1.27.1', $digest('0127')], [$go127->tag, $go127->digest, $go127->available_tag, $go127->available_digest]);
        $go126->refresh();
        $this->assertSame(['1.26', $digest('0126'), null], [$go126->tag, $go126->digest, $go126->available_digest]);
        $this->assertSame(3, Image::where('pack_id', $pack->id)->count());
        $this->assertSame(1, $this->packJobs()->count());
        $this->assertDatabaseHas('audit_entries', ['action' => 'Updated pack', 'target' => 'Go 1.0.0 to 1.1.0']);

        // The image checker does not look for new digests of a pack image: the pack gives them.
        $this->postJson("/api/v1/images/{$go126->id}/check")->assertOk()->assertJsonPath('availableDigest', null);
    }

    public function test_a_pack_goes_only_when_no_website_uses_it(): void
    {
        $server = $this->server(['agent_runtimes' => ['frankenphp', 'golang']]);
        $this->admin();
        $keyId = $this->trustKey();
        $this->install();
        $pack = Pack::firstOrFail();
        $website = $this->website($this->customer(), $server, Image::where('runtime_version', '1.26')->firstOrFail(), 'api.acme.example');

        $this->deleteJson("/api/v1/packs/{$pack->id}")->assertJsonValidationErrors(['pack' => '1 website uses the runtime Go. Change the runtime of these websites or remove them first.']);
        // The key of the pack stays while the pack is there: each server checks the pack with it.
        $this->deleteJson("/api/v1/trusted-keys/{$keyId}")->assertJsonValidationErrors('key');

        $website->domains()->delete();
        $website->delete();
        AgentJob::query()->delete();
        $this->deleteJson("/api/v1/packs/{$pack->id}")->assertNoContent();
        $this->assertSame(0, Pack::count());
        $this->assertSame(0, Image::whereNotNull('pack_id')->count());
        $this->assertSame(1, $this->packJobs()->count());
        $this->deleteJson("/api/v1/trusted-keys/{$keyId}")->assertNoContent();
    }

    public function test_a_server_with_other_packs_gets_the_packs_again(): void
    {
        $server = $this->server();
        $this->admin();
        $this->trustKey();
        $this->install();
        AgentJob::query()->delete();

        // A report that agrees with the panel makes no job.
        $this->report($server, [['slug' => 'golang', 'version' => '1.0.0']]);
        $this->assertSame(0, $this->packJobs()->count());
        $this->assertSame([['slug' => 'golang', 'version' => '1.0.0']], $server->fresh()->agent_packs);

        // The server lost the pack, for example after a restore of the server.
        $this->report($server, []);
        $this->assertSame(1, $this->packJobs()->count());
        // The job waits in the queue: the next report makes no second job.
        $this->report($server, []);
        $this->assertSame(1, $this->packJobs()->count());

        // The job failed. The next try waits 1 minute, then 2.
        app(JobOutcome::class)->finish($this->packJobs()->first(), JobState::Failed, 'the image policy is not a JSON object');
        $this->report($server, []);
        $this->assertSame(1, $this->packJobs()->count());
        $this->travel(61)->seconds();
        $this->report($server, []);
        $this->assertSame(2, $this->packJobs()->count());

        // The job succeeded: the panel keeps the packs and the runtimes of the server.
        app(JobOutcome::class)->finish($this->packJobs()->last(), JobState::Succeeded, null, [
            'packs' => [['slug' => 'golang', 'version' => '1.0.0']], 'runtimes' => ['frankenphp', 'node', 'golang'],
        ]);
        $server->refresh();
        $this->assertSame([['slug' => 'golang', 'version' => '1.0.0']], $server->agent_packs);
        $this->assertTrue($server->hasRuntime('golang'));
        $this->report($server, [['slug' => 'golang', 'version' => '1.0.0']]);
        $this->assertNull($server->fresh()->packs_resync);

        // A server with no web role does not get the packs.
        $db = $this->server(['roles' => ['database']]);
        $this->report($db, []);
        $this->assertSame(0, AgentJob::where('server_id', $db->id)->count());

        // An administrator sends the packs to each web server again.
        $this->postJson('/api/v1/packs/sync')->assertAccepted()->assertJsonPath('servers', 1);
    }

    public function test_a_website_of_a_pack_goes_to_a_server_that_loaded_the_pack(): void
    {
        $this->admin();
        $this->trustKey();
        $this->install();
        $image = Image::where('runtime_version', '1.27')->firstOrFail();
        // The server without the pack: its agent did not load it yet.
        $old = $this->server(['agent_runtimes' => ['frankenphp', 'apache-php-fpm', 'node', 'python', 'custom']]);
        $go = $this->server(['agent_runtimes' => ['frankenphp', 'apache-php-fpm', 'node', 'python', 'custom', 'golang']]);
        $customer = $this->customer();
        $this->actingWith($this->user(Role::Owner, $customer));

        // The customer sees the image of the pack, with its usual commands.
        $listed = collect($this->getJson('/api/v1/runtime-images')->assertOk()->json('data'))->firstWhere('id', $image->id);
        $this->assertSame(['family' => 'golang', 'custom' => false, 'defaults' => ['command' => './server', 'install' => 'go build -o server .']],
            array_intersect_key($listed, array_flip(['family', 'custom', 'defaults'])));
        $this->assertSame('golang', $listed['pack']['slug']);

        $this->postJson('/api/v1/websites', ['name' => 'api.acme.example', 'imageId' => $image->id])
            ->assertStatus(202)
            ->assertJsonPath('website.command', './server')
            ->assertJsonPath('website.installCommand', 'go build -o server .')
            ->assertJsonPath('website.runtime.family', 'golang');
        $job = AgentJob::where('action', AgentAction::CreateWebsite->value)->latest('id')->firstOrFail();
        $this->assertSame($go->id, $job->server_id);
        $p = $job->payload['website'];
        $this->assertSame(['golang', ['sh', '-c', './server'], '.', 0, $image->digest], [$p['runtime'], $p['command'], $p['documentRoot'], $p['containerPort'], $p['imageDigest']]);

        // An administrator who selects a server without the runtime gets an error.
        $this->actingWith($this->user(Role::Administrator));
        $this->postJson('/api/v1/websites', ['name' => 'old.acme.example', 'imageId' => $image->id, 'serverId' => $old->id, 'customerId' => $customer->id])
            ->assertJsonValidationErrors('serverId');
    }

    public function test_the_built_in_runtimes_and_custom_images_do_not_change(): void
    {
        // An agent of an earlier release reports no runtimes. It has the built-in runtimes.
        $server = new Server(['agent_runtimes' => null]);
        foreach (['frankenphp', 'apache-php-fpm', 'node', 'python', 'custom'] as $runtime) {
            $this->assertTrue($server->hasRuntime($runtime));
        }
        $this->assertFalse($server->hasRuntime('golang'));
        $node = Image::create(['name' => 'Node.js 24', 'family' => 'node', 'runtime_version' => '24', 'reference' => 'ghcr.io/cloudfactory/node24', 'tag' => '0.2.0', 'signature_verified' => true]);
        $this->assertSame(Image::APP_DEFAULTS['node'], $node->fresh()->appDefaults());
        $this->assertNull($this->image()->refresh()->appDefaults());

        // A custom image is still a custom image. The form has no runtime field now: a new
        // runtime comes from a pack.
        $this->admin();
        $this->postJson('/api/v1/images', ['name' => 'Go 1.25', 'reference' => 'ghcr.io/example/golang', 'tag' => '1.25.0', 'policy' => 'manual'])->assertCreated()
            ->assertJsonPath('family', 'custom')->assertJsonPath('custom', true)->assertJsonPath('pack', null)->assertJsonPath('defaults', null);
    }

    public function test_an_image_repository_is_in_one_pack_only(): void
    {
        $this->server();
        $this->admin();
        $this->trustKey();
        $this->install();
        $zig = $this->manifest(['slug' => 'zig', 'name' => 'Zig']);
        $this->postJson('/api/v1/packs', ['manifest' => $zig, 'signature' => $this->sign($zig), 'password' => 'password'])
            ->assertJsonValidationErrors(['manifest' => 'The image ghcr.io/example/cf-golang is in the pack golang. An image repository can be in one pack only.']);
        $again = $this->manifest(['version' => '1.0.1']);
        $this->postJson('/api/v1/packs', ['manifest' => $again, 'signature' => $this->sign($again), 'password' => 'password'])
            ->assertJsonValidationErrors(['manifest' => 'The pack "golang" is installed. To install a new version, update it.']);
        // A custom image of the panel has the repository.
        $this->postJson('/api/v1/images', ['name' => 'Zig', 'reference' => 'ghcr.io/example/cf-zig', 'tag' => '1.0', 'policy' => 'manual'])->assertCreated();
        $own = $this->manifest(['slug' => 'zig', 'name' => 'Zig'], [['ref' => 'ghcr.io/example/cf-zig:1.27'], ['ref' => 'ghcr.io/example/cf-zig:1.26']]);
        $this->postJson('/api/v1/packs', ['manifest' => $own, 'signature' => $this->sign($own), 'password' => 'password'])
            ->assertJsonValidationErrors(['manifest' => 'The image ghcr.io/example/cf-zig is the image Zig of the panel. A pack needs its own image repository.']);
    }

    public function test_only_an_administrator_with_the_packs_scope_manages_packs(): void
    {
        $this->actingWith($this->user(Role::Owner, $this->customer()));
        $this->getJson('/api/v1/packs')->assertForbidden();
        $this->getJson('/api/v1/trusted-keys')->assertForbidden();
        $this->actingWith($this->user(Role::Reseller));
        $this->getJson('/api/v1/packs')->assertForbidden();

        $admin = $this->user(Role::Administrator);
        $this->actingWith($admin, ['plugins:read']);
        $this->getJson('/api/v1/packs')->assertForbidden();
        $this->getJson('/api/v1/trusted-keys')->assertOk();
        $this->actingWith($admin, ['packs:read']);
        $this->getJson('/api/v1/packs')->assertOk();
        $this->getJson('/api/v1/trusted-keys')->assertOk();
        $this->postJson('/api/v1/packs/sync')->assertForbidden();
    }
}
