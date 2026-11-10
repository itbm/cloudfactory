<?php

namespace Tests\Feature;

use App\Enums\AgentAction;
use App\Models\AuditEntry;
use App\Services\Audit;
use App\Services\JobDispatcher;
use Tests\TestCase;

/**
 * Audit findings B4 and R4: the panel uses PostgreSQL on a server. These tests are for the
 * differences between SQLite (development and the usual test run) and PostgreSQL. The CI runs
 * all tests with the two engines.
 */
class DatabaseEngineTest extends TestCase
{
    /** PostgreSQL compares text with the case of the letters. The search must not. */
    public function test_a_search_ignores_the_case_of_the_letters(): void
    {
        $this->actingWith($this->user());
        $this->website($this->customer(), $this->server(), $this->image(), 'Shop.Example');
        Audit::system('Paused rollout', 'FrankenPHP 8.4');

        $this->getJson('/api/v1/websites?q=shop.EXAMPLE')->assertOk()->assertJsonCount(1, 'data');
        $this->getJson('/api/v1/websites?q=other')->assertOk()->assertJsonCount(0, 'data');
        $this->getJson('/api/v1/audit?q=frankenphp')->assertOk()->assertJsonCount(1, 'data');
        $this->getJson('/api/v1/audit?q=PAUSED')->assertOk()->assertJsonCount(1, 'data');
    }

    /** Audit finding R5: an array in the search text gave a server error. */
    public function test_the_search_text_must_be_text(): void
    {
        $this->actingWith($this->user());

        $this->getJson('/api/v1/audit?q[]=x')->assertUnprocessable()->assertJsonValidationErrors('q');
        $this->getJson('/api/v1/audit?q='.str_repeat('x', 201))->assertUnprocessable();
        $this->getJson('/api/v1/websites?q[]=x')->assertUnprocessable()->assertJsonValidationErrors('q');
    }

    /** PostgreSQL refuses a value that is longer than its column. These columns have no limit. */
    public function test_a_long_value_does_not_stop_a_request(): void
    {
        $long = str_repeat('a', 300);
        Audit::system($long, $long);
        $this->assertSame($long, AuditEntry::sole()->target);

        $server = $this->server();
        $website = $this->website($this->customer(), $server, $this->image());
        $job = app(JobDispatcher::class)->dispatch($server, AgentAction::Reboot, [], $long, $website, $long);
        $this->assertSame($long, $job->fresh()->title);
        $this->assertSame($long, $job->fresh()->ref);

        $website->update(['application' => $long]);
        $this->assertSame($long, $website->fresh()->application);
    }

    /** PostgreSQL refuses an integer that is too large for its column. The API checks it first. */
    public function test_a_number_that_is_too_large_is_a_validation_error(): void
    {
        $this->actingWith($this->user());
        $plan = ['name' => 'Huge', 'websites' => 9_999_999_999, 'mailboxes' => 1, 'databases' => 1, 'phpExtensions' => ['opcache'],
            'limits' => ['vcpus' => 1, 'memoryBytes' => 1024 ** 3, 'diskBytes' => 1024 ** 3, 'diskIoBytesPerSecond' => 1024 ** 2, 'processes' => 100]];

        $this->postJson('/api/v1/plans', $plan)->assertUnprocessable()->assertJsonValidationErrors('websites');
        $this->postJson('/api/v1/plans', ['websites' => 5] + $plan)->assertCreated();
    }
}
