<?php

namespace Tests\Unit;

use App\Services\TerminalTickets;
use Tests\TestCase;

/** The tickets of the panel agree with the terminal relay (agent/internal/relay). */
class TerminalTicketsTest extends TestCase
{
    public function test_the_ticket_agrees_with_the_relay(): void
    {
        // The same values as TestTheTicketAgreesWithThePanel in agent/internal/relay/relay_test.go.
        config(['app.key' => 'base64:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=']);
        $this->assertSame('1800000000.e46bb9ed9ff1529010dfd652150346e64ec303c352143aae23cd1f4354223af5',
            TerminalTickets::make('browser', '01m82jppdpt215s2n6f4p03hwd', 1_800_000_000));
    }
}
