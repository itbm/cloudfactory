<?php

namespace Tests;

use Illuminate\Foundation\Testing\RefreshDatabase;
use Illuminate\Foundation\Testing\TestCase as BaseTestCase;
use Illuminate\Support\Facades\Queue;
use Tests\Support\Hosting;

abstract class TestCase extends BaseTestCase
{
    use Hosting, RefreshDatabase;

    /** The certificate fingerprint of the agent on the panel server in the tests. */
    protected const LOCAL_FINGERPRINT = 'f00df00df00df00df00df00df00df00df00df00df00df00df00df00df00df00d';

    protected function setUp(): void
    {
        parent::setUp();
        // The tests do not call real agents. They send job events to the panel instead.
        Queue::fake();
    }

    /**
     * A request from the agent on the panel server. The HTTPS port of the API sends the
     * fingerprint of the client certificate in a header (AuthenticateAgent).
     */
    protected function asLocalAgent(string $token, string $fingerprint = self::LOCAL_FINGERPRINT): static
    {
        return $this->withToken($token)->withHeader('X-Cloudfactory-Direct-Cert-Sha256', $fingerprint);
    }
}
