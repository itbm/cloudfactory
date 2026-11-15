<?php

namespace Tests\Feature;

use App\Enums\AgentAction;
use App\Enums\JobState;
use App\Enums\Role;
use App\Http\Controllers\Api\DeployController;
use App\Jobs\SendJobToAgent;
use App\Models\AgentJob;
use App\Models\Image;
use App\Models\Website;
use App\Services\AgentClient;
use App\Services\JobOutcome;
use Illuminate\Support\Facades\DB;
use Illuminate\Support\Facades\Http;
use Tests\TestCase;

/**
 * Node.js and Python websites: the version, the start command, the install command and the
 * environment variables. The variables are for each website, of each runtime.
 */
class AppRuntimeTest extends TestCase
{
    private const AGENT = 'https://web-1.internal:7443/cloudfactory.agent.v1.AgentService/';

    private function node(string $version = '24'): Image
    {
        return Image::create(['name' => "Node.js {$version}", 'family' => 'node', 'runtime_version' => $version, 'reference' => "ghcr.io/cloudfactory/node{$version}", 'tag' => '0.2.0', 'signature_verified' => true]);
    }

    private function python(): Image
    {
        return Image::create(['name' => 'Python 3.14', 'family' => 'python', 'runtime_version' => '3.14', 'reference' => 'ghcr.io/cloudfactory/python3.14', 'tag' => '0.2.0', 'signature_verified' => true]);
    }

    private function app(Image $image, array $attributes = []): Website
    {
        config(['cloudfactory.agent.certificates' => sys_get_temp_dir().'/cloudfactory-test-pki']);
        $server = $this->server(['certificate_pem' => 'test', 'certificate_fingerprint' => str_repeat('ab', 32)]);

        return $this->website($this->customer(), $server, $image, 'shop.acme.example', $attributes + [
            'extensions' => [], 'command' => 'npm start', 'install_command' => 'npm ci --omit=dev', 'document_root' => '.',
        ]);
    }

    private function lastJob(AgentAction $action): AgentJob
    {
        return AgentJob::where('action', $action->value)->latest('id')->firstOrFail();
    }

    public function test_a_new_node_website_gets_the_usual_commands_and_runs_its_start_command(): void
    {
        $node = $this->node();
        $customer = $this->customer();
        $this->server();
        $this->actingWith($this->user(Role::Owner, $customer));

        $response = $this->postJson('/api/v1/websites', ['name' => 'shop.acme.example', 'application' => 'Node.js app', 'imageId' => $node->id])
            ->assertStatus(202)
            ->assertJsonPath('website.command', 'npm start')
            ->assertJsonPath('website.installCommand', 'npm install --omit=dev')
            ->assertJsonPath('website.environment', [])
            ->assertJsonPath('website.runtime.family', 'node')
            ->assertJsonPath('website.runtime.version', '24')
            ->assertJsonPath('website.runtime.documentRoot', '.');
        $website = Website::findOrFail($response->json('website.id'));
        $this->assertSame([], $website->extensions);
        $this->assertSame([], $website->php_ini);

        $p = $this->lastJob(AgentAction::CreateWebsite)->payload['website'];
        $this->assertSame(['node', ['sh', '-c', 'npm start'], '.', false], [$p['runtime'], $p['command'], $p['documentRoot'], $p['worker']]);

        // Python, with the commands of the user. An empty install command: no install step.
        $python = $this->python();
        $this->postJson('/api/v1/websites', ['name' => 'api.acme.example', 'imageId' => $python->id, 'command' => 'gunicorn --bind 0.0.0.0:$PORT app:app', 'installCommand' => ''])
            ->assertStatus(202)->assertJsonPath('website.command', 'gunicorn --bind 0.0.0.0:$PORT app:app')->assertJsonPath('website.installCommand', null);

        // A PHP website has no start command and no install command.
        $php = $this->image();
        $this->postJson('/api/v1/websites', ['name' => 'blog.acme.example', 'imageId' => $php->id, 'command' => 'php -S 0.0.0.0:8080'])->assertJsonValidationErrors('command');
        $this->postJson('/api/v1/websites', ['name' => 'blog.acme.example', 'imageId' => $php->id, 'installCommand' => 'composer install'])->assertJsonValidationErrors('installCommand');
        $this->postJson('/api/v1/websites', ['name' => 'two.acme.example', 'imageId' => $node->id, 'command' => "npm start\nrm -rf /"])->assertJsonValidationErrors('command');

        // The list of images for a new website has the version and the usual commands.
        $images = collect($this->getJson('/api/v1/runtime-images')->assertOk()->json('data'))->keyBy('name');
        $this->assertSame(['24', ['command' => 'npm start', 'install' => 'npm install --omit=dev']], [$images['Node.js 24']['version'], $images['Node.js 24']['defaults']]);
        $this->assertNull($images['FrankenPHP · PHP 8.4']['defaults']);
    }

    public function test_a_node_worker_runs_its_command_with_the_node_runtime(): void
    {
        $node = $this->node();
        $customer = $this->customer();
        $this->server(['mesh_networks' => true, 'mesh_websites' => true, 'mesh_public_key' => base64_encode(hash('sha256', 'web', true))]);
        $this->actingWith($this->user(Role::Owner, $customer));

        $this->postJson('/api/v1/websites', ['kind' => 'worker', 'name' => 'jobs', 'imageId' => $node->id, 'command' => 'node worker.js'])
            ->assertStatus(202)->assertJsonPath('website.command', 'node worker.js')->assertJsonPath('website.installCommand', 'npm install --omit=dev');
        $p = $this->lastJob(AgentAction::CreateWebsite)->payload['website'];
        $this->assertSame([['sh', '-c', 'node worker.js'], true], [$p['command'], $p['worker']]);
    }

    public function test_the_runtime_tab_changes_the_version_and_the_commands(): void
    {
        $website = $this->app($this->node('22'));
        $node24 = $this->node('24');
        $this->actingWith($this->user(Role::Owner, $website->customer));

        // A new start command: a new container, with no stop.
        $this->putJson("/api/v1/websites/{$website->id}/runtime", ['command' => 'node server.js'])->assertAccepted()
            ->assertJsonPath('job.action', 'SetRuntime')->assertJsonPath('job.title', 'Change start command');
        $this->assertSame(['sh', '-c', 'node server.js'], $this->lastJob(AgentAction::SetRuntime)->payload['website']['command']);

        // A new install command only changes the setting.
        $this->putJson("/api/v1/websites/{$website->id}/runtime", ['installCommand' => 'npm ci'])->assertAccepted()->assertJsonPath('job.title', 'Apply runtime settings');
        $this->assertSame('npm ci', $website->fresh()->install_command);

        // A new version installs the dependencies again: a native module is for one version.
        $answer = $this->putJson("/api/v1/websites/{$website->id}/runtime", ['imageId' => $node24->id])->assertAccepted()
            ->assertJsonPath('job.action', 'InstallDependencies')->assertJsonPath('website.runtime.version', '24')->json();
        $job = AgentJob::findOrFail($answer['job']['id']);
        $this->assertSame(['npm ci', 'ghcr.io/cloudfactory/node24:0.2.0'], [$job->payload['command'], $job->payload['website']['image']]);
        $this->assertSame(['Install dependencies', 'Check image signature', 'Start new container on a copy', 'Health check', 'Switch traffic', 'Stop old container'], $job->steps);

        // The install fails: the website goes back to Node.js 22, as for a change of the runtime.
        app(JobOutcome::class)->started($job);
        app(JobOutcome::class)->finish($job, JobState::Failed, 'the install command failed: exit status 1');
        $this->assertSame('Node.js 22', $website->fresh()->image->name);

        // A website changes only to an image of its own kind.
        $this->putJson("/api/v1/websites/{$website->id}/runtime", ['imageId' => $this->python()->id])->assertJsonValidationErrors('imageId');
        $this->putJson("/api/v1/websites/{$website->id}/runtime", ['imageId' => $this->image()->id])->assertJsonValidationErrors('imageId');
        // A PHP website has no install command.
        $php = $this->website($website->customer, $website->server, $this->image(), 'blog.acme.example');
        $this->putJson("/api/v1/websites/{$php->id}/runtime", ['installCommand' => 'npm ci'])->assertJsonValidationErrors('installCommand');
        $this->putJson("/api/v1/websites/{$php->id}/runtime", ['command' => 'php -S 0.0.0.0:8080'])->assertJsonValidationErrors('command');
    }

    public function test_install_dependencies_runs_the_install_command(): void
    {
        $website = $this->app($this->node());
        $this->actingWith($this->user(Role::Owner, $website->customer));

        $this->postJson("/api/v1/websites/{$website->id}/runtime/install")->assertAccepted()
            ->assertJsonPath('job.action', 'InstallDependencies')->assertJsonPath('job.title', 'Install dependencies');
        $this->assertSame('npm ci --omit=dev', $this->lastJob(AgentAction::InstallDependencies)->payload['command']);

        $website->update(['install_command' => null]);
        $this->postJson("/api/v1/websites/{$website->id}/runtime/install")->assertJsonValidationErrors('installCommand');
        $php = $this->website($website->customer, $website->server, $this->image(), 'blog.acme.example');
        $this->postJson("/api/v1/websites/{$php->id}/runtime/install")->assertJsonValidationErrors('installCommand');
        // A different customer cannot see the website.
        $this->actingWith($this->user(Role::Owner, $this->customer()));
        $this->postJson("/api/v1/websites/{$website->id}/runtime/install")->assertNotFound();
    }

    public function test_environment_variables_are_encrypted_and_go_to_the_agent_with_each_job(): void
    {
        $website = $this->app($this->node());
        $this->actingWith($this->user(Role::Owner, $website->customer));

        $answer = $this->patchJson("/api/v1/websites/{$website->id}/environment", ['variables' => ['NODE_ENV' => 'production', 'DATABASE_URL' => 'postgres://app:s3cret@postgres.internal/app']])
            ->assertAccepted()->assertJsonPath('job.action', 'SetRuntime')->assertJsonPath('job.title', 'Apply environment variables')
            ->assertJsonPath('website.environment', ['DATABASE_URL', 'NODE_ENV'])->json();
        // The database, the job and the API have no value.
        $this->assertStringNotContainsString('s3cret', (string) DB::table('websites')->where('id', $website->id)->value('environment'));
        $job = AgentJob::findOrFail($answer['job']['id']);
        $this->assertStringNotContainsString('s3cret', json_encode($job->payload));
        $this->assertStringNotContainsString('s3cret', json_encode($this->getJson("/api/v1/websites/{$website->id}")->json()));
        $this->assertSame(['DATABASE_URL' => 'postgres://app:s3cret@postgres.internal/app', 'NODE_ENV' => 'production'], $website->fresh()->variables());

        // The panel adds the values when it sends the job.
        Http::fake([self::AGENT.'*' => Http::response(['jobId' => $job->id])]);
        (new SendJobToAgent($job->id))->handle(app(AgentClient::class));
        Http::assertSent(fn ($r) => str_ends_with($r->url(), '/SetRuntime') && (array) $r['website']['environment'] === ['DATABASE_URL' => 'postgres://app:s3cret@postgres.internal/app', 'NODE_ENV' => 'production']);

        // null removes a variable. The same values make no job.
        $this->patchJson("/api/v1/websites/{$website->id}/environment", ['variables' => ['DATABASE_URL' => null]])->assertAccepted()->assertJsonPath('website.environment', ['NODE_ENV']);
        $this->patchJson("/api/v1/websites/{$website->id}/environment", ['variables' => ['NODE_ENV' => 'production']])->assertOk()->assertJsonPath('job', null);

        // Names that the agent and the image use, and values on two lines.
        foreach (['PORT', 'HOME', 'CF_APP_DIR', '1A', 'A-B'] as $name) {
            $this->patchJson("/api/v1/websites/{$website->id}/environment", ['variables' => [$name => 'x']])->assertJsonValidationErrors("variables.{$name}");
        }
        $this->patchJson("/api/v1/websites/{$website->id}/environment", ['variables' => ['A' => "x\ny"]])->assertJsonValidationErrors('variables.A');
        $many = [];
        for ($i = 0; $i < 50; $i++) {
            $many["V{$i}"] = 'x';
        }
        $this->patchJson("/api/v1/websites/{$website->id}/environment", ['variables' => $many])->assertJsonValidationErrors('variables');

        // A PHP website has variables too.
        $php = $this->website($website->customer, $website->server, $this->image(), 'blog.acme.example');
        $this->patchJson("/api/v1/websites/{$php->id}/environment", ['variables' => ['APP_ENV' => 'production']])->assertAccepted();
        $this->assertSame(['APP_ENV' => 'production'], $php->fresh()->variables());
    }

    public function test_a_deploy_installs_the_dependencies_in_the_new_release(): void
    {
        $website = $this->app($this->node());
        $this->actingWith($this->user(Role::Owner, $website->customer));
        $this->patchJson("/api/v1/websites/{$website->id}/environment", ['variables' => ['NODE_ENV' => 'production']])->assertAccepted();

        $source = $this->putJson("/api/v1/websites/{$website->id}/deploy", ['repository' => 'git@github.com:acme/shop.git', 'branch' => 'main', 'buildCommands' => ['npm run build']])->assertOk()->json('source');
        // A Node.js application is usually at the top of its repository.
        $this->assertSame('.', $source['webDirectory']);
        $answer = $this->postJson("/api/v1/websites/{$website->id}/deploy/releases")->assertAccepted()->json();
        $job = AgentJob::findOrFail($answer['job']['id']);
        $this->assertSame(['(npm ci --omit=dev)', 'npm run build'], $job->payload['buildCommands']);
        $this->assertSame("releases/{$answer['release']['id']}", $job->payload['website']['documentRoot']);

        // In a subdirectory, the install command runs there.
        $website->gitSource->update(['web_directory' => 'apps/web']);
        $this->assertSame(["(cd 'apps/web' && npm ci --omit=dev)", 'npm run build'], DeployController::buildCommands($website->fresh(), $website->gitSource->fresh()));
    }
}
