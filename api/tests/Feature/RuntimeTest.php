<?php

namespace Tests\Feature;

use Tests\TestCase;

class RuntimeTest extends TestCase
{
    public function test_extensions_must_be_in_the_plan(): void
    {
        $customer = $this->customer();
        $website = $this->website($customer, $this->server(), $this->image());
        $this->actingWith($this->user());

        $this->putJson("/api/v1/websites/{$website->id}/runtime", ['extensions' => ['opcache', 'xdebug']])
            ->assertJsonValidationErrors(['extensions' => 'The Business plan does not include: xdebug.']);
        $this->putJson("/api/v1/websites/{$website->id}/runtime", ['extensions' => ['opcache', 'redis']])
            ->assertAccepted()->assertJsonPath('job.action', 'SetRuntime');
    }

    public function test_memory_limit_cannot_be_more_than_the_website_limit(): void
    {
        $customer = $this->customer();
        $website = $this->website($customer, $this->server(), $this->image());
        $this->actingWith($this->user());

        $this->putJson("/api/v1/websites/{$website->id}/runtime", ['phpIni' => ['memory_limit' => '8G']])->assertJsonValidationErrors('phpIni.memory_limit');
        $this->putJson("/api/v1/websites/{$website->id}/runtime", ['phpIni' => ['disable_functions' => '']])->assertJsonValidationErrors('phpIni.disable_functions');
    }

    public function test_changing_the_image_keeps_the_previous_one_for_revert(): void
    {
        $customer = $this->customer();
        $old = $this->image();
        $new = $this->image(['name' => 'FrankenPHP · PHP 8.5', 'php_version' => '8.5', 'reference' => 'ghcr.io/cloudfactory/frankenphp-php8.5', 'tag' => '8.5.0-1']);
        $website = $this->website($customer, $this->server(), $old);
        $this->actingWith($this->user());

        $this->putJson("/api/v1/websites/{$website->id}/runtime", ['imageId' => $new->id])->assertAccepted();
        $this->assertSame($old->id, $website->fresh()->previous_image_id);

        $this->postJson("/api/v1/websites/{$website->id}/runtime/revert")->assertAccepted();
        $this->assertSame($old->id, $website->fresh()->image_id);
    }

    public function test_worker_mode_is_only_for_frankenphp(): void
    {
        $customer = $this->customer();
        $apache = $this->image(['name' => 'Apache', 'family' => 'apache-php-fpm', 'reference' => 'ghcr.io/cloudfactory/apache-php8.4', 'tag' => '8.4.0-1']);
        $website = $this->website($customer, $this->server(), $apache);
        $this->actingWith($this->user());

        $this->putJson("/api/v1/websites/{$website->id}/runtime", ['workerMode' => true])->assertJsonValidationErrors('workerMode');
    }
}
