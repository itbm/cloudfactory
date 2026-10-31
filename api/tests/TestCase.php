<?php

namespace Tests;

use Illuminate\Foundation\Testing\RefreshDatabase;
use Illuminate\Foundation\Testing\TestCase as BaseTestCase;
use Illuminate\Support\Facades\Queue;
use Tests\Support\Hosting;

abstract class TestCase extends BaseTestCase
{
    use Hosting, RefreshDatabase;

    protected function setUp(): void
    {
        parent::setUp();
        // The tests do not call real agents. They send job events to the panel instead.
        Queue::fake();
    }
}
