<?php

namespace Tests\Feature;

use App\Enums\AgentAction;
use App\Enums\Role;
use App\Models\AgentJob;
use App\Models\Image;
use App\Models\Server;
use Tests\TestCase;

/**
 * Stage 2 of the runtime packs: an image of a runtime from a manifest of the agent, for
 * example Go. An administrator adds the image with the name of the runtime and its usual
 * commands. The panel puts its websites only on the servers whose agent reports the runtime.
 */
class PackRuntimeTest extends TestCase
{
    private const IMAGE = ['name' => 'Go 1.25', 'reference' => 'ghcr.io/example/golang', 'tag' => '1.25.0', 'policy' => 'manual'];

    private function golang(): Image
    {
        $this->actingWith($this->user(Role::Administrator));
        $id = $this->postJson('/api/v1/images', [...self::IMAGE, 'runtime' => 'golang', 'defaultCommand' => './server', 'defaultInstall' => 'go build -o server .'])
            ->assertCreated()
            ->assertJsonPath('family', 'golang')
            ->assertJsonPath('custom', false)
            ->assertJsonPath('appRuntime', true)
            ->assertJsonPath('defaults', ['command' => './server', 'install' => 'go build -o server .'])
            ->json('id');

        return Image::findOrFail($id);
    }

    private function lastJob(AgentAction $action): AgentJob
    {
        return AgentJob::where('action', $action->value)->latest('id')->firstOrFail();
    }

    public function test_an_administrator_adds_an_image_of_a_runtime_from_a_manifest(): void
    {
        $image = $this->golang();
        $this->assertTrue($image->isApp());
        $this->assertSame('golang', $image->kind());

        // The names of the built-in runtimes are not available.
        foreach (['node', 'frankenphp', 'custom', 'edge', 'Go', 'g', 'go lang'] as $name) {
            $this->postJson('/api/v1/images', [...self::IMAGE, 'runtime' => $name, 'defaultCommand' => './server'])->assertJsonValidationErrors('runtime');
        }
        // An image of a runtime has the port and the command of the runtime contract.
        $this->postJson('/api/v1/images', [...self::IMAGE, 'runtime' => 'golang', 'defaultCommand' => './server', 'containerPort' => 9000])->assertJsonValidationErrors('runtime');
        $this->postJson('/api/v1/images', [...self::IMAGE, 'runtime' => 'golang', 'defaultCommand' => './server', 'command' => ['./server']])->assertJsonValidationErrors('runtime');
        // It needs a start command. The usual commands need a runtime.
        $this->postJson('/api/v1/images', [...self::IMAGE, 'runtime' => 'golang'])->assertJsonValidationErrors('defaultCommand');
        $this->postJson('/api/v1/images', [...self::IMAGE, 'defaultCommand' => './server'])->assertJsonValidationErrors('defaultCommand');
        $this->postJson('/api/v1/images', [...self::IMAGE, 'defaultInstall' => 'go build'])->assertJsonValidationErrors('defaultInstall');

        // A custom image does not change.
        $this->postJson('/api/v1/images', self::IMAGE)->assertCreated()
            ->assertJsonPath('family', 'custom')->assertJsonPath('custom', true)->assertJsonPath('appRuntime', false)->assertJsonPath('defaults', null);
    }

    public function test_a_customer_selects_the_runtime_and_the_website_goes_to_a_server_with_the_runtime(): void
    {
        $image = $this->golang();
        // An agent of an earlier release reports no runtimes, and an agent without the manifest
        // does not report golang. The panel does not select these servers.
        $old = $this->server(['agent_runtimes' => null]);
        $this->server(['agent_runtimes' => ['frankenphp', 'apache-php-fpm', 'node', 'python', 'custom']]);
        $go = $this->server(['agent_runtimes' => ['frankenphp', 'apache-php-fpm', 'node', 'python', 'custom', 'golang']]);
        $customer = $this->customer();
        $this->actingWith($this->user(Role::Owner, $customer));

        // The customer sees the image, with its usual commands.
        $listed = collect($this->getJson('/api/v1/runtime-images')->assertOk()->json('data'))->firstWhere('id', $image->id);
        $this->assertSame(['family' => 'golang', 'custom' => false, 'defaults' => ['command' => './server', 'install' => 'go build -o server .'], 'appRuntime' => true],
            array_intersect_key($listed, array_flip(['family', 'custom', 'appRuntime', 'defaults'])));

        $this->postJson('/api/v1/websites', ['name' => 'api.acme.example', 'imageId' => $image->id])
            ->assertStatus(202)
            ->assertJsonPath('website.command', './server')
            ->assertJsonPath('website.installCommand', 'go build -o server .')
            ->assertJsonPath('website.runtime.family', 'golang')
            ->assertJsonPath('website.runtime.documentRoot', '.');
        $job = $this->lastJob(AgentAction::CreateWebsite);
        $this->assertSame($go->id, $job->server_id);
        $p = $job->payload['website'];
        $this->assertSame(['golang', ['sh', '-c', './server'], '.', 0], [$p['runtime'], $p['command'], $p['documentRoot'], $p['containerPort']]);

        // The commands of the customer.
        $this->postJson('/api/v1/websites', ['name' => 'www.acme.example', 'imageId' => $image->id, 'command' => './web --port $PORT', 'installCommand' => ''])
            ->assertStatus(202)->assertJsonPath('website.command', './web --port $PORT')->assertJsonPath('website.installCommand', null);

        // An administrator who selects a server without the runtime gets an error.
        $this->actingWith($this->user(Role::Administrator));
        $this->postJson('/api/v1/websites', ['name' => 'old.acme.example', 'imageId' => $image->id, 'serverId' => $old->id, 'customerId' => $customer->id])
            ->assertJsonValidationErrors(['serverId' => "The agent on the server {$old->name} does not have the runtime golang."]);
    }

    public function test_no_server_with_the_runtime_gives_an_error(): void
    {
        $image = $this->golang();
        $this->server(['agent_runtimes' => ['frankenphp', 'node']]);
        $customer = $this->customer();
        $this->actingWith($this->user(Role::Owner, $customer));

        $this->postJson('/api/v1/websites', ['name' => 'api.acme.example', 'imageId' => $image->id])->assertJsonValidationErrors('serverId');
        $this->assertSame(0, AgentJob::where('action', AgentAction::CreateWebsite->value)->count());
    }

    public function test_the_built_in_runtimes_do_not_change(): void
    {
        // An agent of an earlier release reports no runtimes. It has the built-in runtimes.
        $server = new Server(['agent_runtimes' => null]);
        foreach (['frankenphp', 'apache-php-fpm', 'node', 'python', 'custom'] as $runtime) {
            $this->assertTrue($server->hasRuntime($runtime));
        }
        $this->assertFalse($server->hasRuntime('golang'));

        // The official images are not runtime images from a manifest.
        $node = Image::create(['name' => 'Node.js 24', 'family' => 'node', 'runtime_version' => '24', 'reference' => 'ghcr.io/cloudfactory/node24', 'tag' => '0.2.0', 'signature_verified' => true]);
        $node->refresh();
        $this->assertFalse($node->app_runtime);
        $this->assertSame(Image::APP_DEFAULTS['node'], $node->appDefaults());
        $php = $this->image()->refresh();
        $this->assertFalse($php->isApp());
        $this->assertNull($php->appDefaults());

        // A Node.js website on a server that reports only some runtimes: the panel does not check
        // the built-in runtimes, as before.
        $this->server(['agent_runtimes' => ['frankenphp']]);
        $customer = $this->customer();
        $this->actingWith($this->user(Role::Owner, $customer));
        $this->postJson('/api/v1/websites', ['name' => 'shop.acme.example', 'imageId' => $node->id])->assertStatus(202)->assertJsonPath('website.command', 'npm start');
    }
}
