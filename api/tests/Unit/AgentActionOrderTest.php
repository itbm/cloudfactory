<?php

namespace Tests\Unit;

use App\Enums\AgentAction;
use PHPUnit\Framework\TestCase;

/**
 * The cases and the steps of AgentAction are in alphabetical order. Then two pull requests that
 * add actions put them in different places, and do not conflict
 * (docs/development.md#parallel-pull-requests).
 */
class AgentActionOrderTest extends TestCase
{
    public function test_the_cases_are_in_alphabetical_order(): void
    {
        $names = array_map(fn (AgentAction $a) => $a->name, AgentAction::cases());
        $sorted = $names;
        sort($sorted, SORT_STRING);
        $this->assertSame($sorted, $names, 'Put the cases of AgentAction in alphabetical order.');
    }

    public function test_the_steps_are_in_alphabetical_order(): void
    {
        $source = (string) file_get_contents(__DIR__.'/../../app/Enums/AgentAction.php');
        preg_match('/function steps\(\).*?return match \(\$this\) \{(.*?)\n        \};/s', $source, $match);
        preg_match_all('/^            self::(\w+) =>/m', $match[1] ?? '', $arms);
        $sorted = $arms[1];
        sort($sorted, SORT_STRING);
        $this->assertNotEmpty($arms[1]);
        $this->assertSame($sorted, $arms[1], 'Put the steps of AgentAction in alphabetical order.');
    }
}
