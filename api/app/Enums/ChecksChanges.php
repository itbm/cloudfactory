<?php

namespace App\Enums;

/** The functions of App\Enums\State that are the same for each enum. */
trait ChecksChanges
{
    public function canChangeTo(State $to): bool
    {
        return in_array($to, static::changes()[$this->value], true);
    }

    public function label(): string
    {
        return str_replace('_', ' ', $this->value);
    }

    /** True when no change from this state is permitted. */
    public function isFinal(): bool
    {
        return static::changes()[$this->value] === [];
    }
}
