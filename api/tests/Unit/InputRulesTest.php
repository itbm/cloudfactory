<?php

namespace Tests\Unit;

use App\Validation\Input;
use App\Validation\InputRules;
use PHPUnit\Framework\TestCase;

/**
 * Audit findings R5 and F04: the panel and the agent accept the same values. The two have their
 * rules from schema/input-rules.json, and the two run the examples in schema/input-vectors.json
 * (the agent: agent/internal/validate/validate_test.go).
 */
class InputRulesTest extends TestCase
{
    public function test_the_panel_and_the_agent_use_the_same_examples(): void
    {
        $vectors = json_decode((string) file_get_contents(__DIR__.'/../../../schema/input-vectors.json'), true, flags: JSON_THROW_ON_ERROR)['rules'];

        $this->assertSame([], array_diff(array_keys($vectors), array_keys(InputRules::PATTERNS)), 'The examples have a rule that does not exist.');
        foreach (array_keys(InputRules::PATTERNS) as $rule) {
            $this->assertNotEmpty($vectors[$rule]['valid'] ?? [], "The rule {$rule} must have valid examples.");
            $this->assertNotEmpty($vectors[$rule]['invalid'] ?? [], "The rule {$rule} must have invalid examples.");
            foreach ($vectors[$rule]['valid'] as $example) {
                $value = self::value($example);
                $this->assertTrue(Input::valid($rule, $value), "{$rule}: the panel refuses the valid example ".json_encode(substr($value, 0, 80)));
            }
            foreach ($vectors[$rule]['invalid'] as $example) {
                $value = self::value($example);
                $this->assertFalse(Input::valid($rule, $value), "{$rule}: the panel accepts the invalid example ".json_encode(substr($value, 0, 80)));
            }
        }
    }

    public function test_each_pattern_is_correct_for_pcre(): void
    {
        foreach (InputRules::PATTERNS as $rule => $pattern) {
            $this->assertNotFalse(@preg_match($pattern, 'x'), "The pattern of the rule {$rule} has an error: ".preg_last_error_msg());
            $this->assertNotEmpty(InputRules::MESSAGES[$rule]);
        }
    }

    public function test_a_value_that_is_not_text_does_not_agree(): void
    {
        $this->assertFalse(Input::valid('digest', null));
        $this->assertFalse(Input::valid('phpExtension', ['gd']));
        $this->assertFalse(Input::valid('id', 1));
    }

    /**
     * An example is a string, or ['repeat' => [...]] with strings and [string, number] pairs.
     *
     * @param  string|array{repeat: list<string|array{string, int}>}  $example
     */
    private static function value(string|array $example): string
    {
        if (is_string($example)) {
            return $example;
        }

        return implode('', array_map(fn ($part) => is_array($part) ? str_repeat($part[0], $part[1]) : $part, $example['repeat']));
    }
}
