<?php

namespace App\Validation;

use Closure;

/**
 * Applies the input rules that the panel and the agent share. The rules are in the file
 * schema/input-rules.json. The generator agent/cmd/cf-rules writes InputRules.php for the panel
 * and the Go checks for the agent from that file, so that the agent does not refuse a value
 * that the panel accepted (audit findings R5 and F04).
 *
 * Do not write a pattern for such a value in a controller. Add a rule to the rules file.
 */
final class Input
{
    /** True when the value agrees with the rule. A value that is not text does not agree. */
    public static function valid(string $rule, mixed $value): bool
    {
        return is_string($value) && preg_match(InputRules::PATTERNS[$rule], $value) === 1;
    }

    /**
     * A validation rule for a request, with a message that tells the user what to type. For
     * example: 'name' => ['required', Input::rule('domain')].
     *
     * @return Closure(string, mixed, Closure): void
     */
    public static function rule(string $rule): Closure
    {
        return function (string $attribute, mixed $value, Closure $fail) use ($rule): void {
            if (! self::valid($rule, $value)) {
                $fail(InputRules::MESSAGES[$rule]);
            }
        };
    }
}
