<?php

namespace App\Exceptions;

use App\Enums\State;
use Symfony\Component\HttpKernel\Exception\ConflictHttpException;

/**
 * A change of state that the table of the state does not permit. The API gives HTTP status 409
 * (conflict) with this message: the request is correct, but the record is in a state that does
 * not permit it. For example, a second "roll back" of the same rollout.
 */
class StateChangeNotAllowed extends ConflictHttpException
{
    public function __construct(public readonly State $from, public readonly State $to)
    {
        $what = $from::what();

        parent::__construct($from === $to
            ? "The {$what} is {$from->label()} already."
            : "The {$what} is {$from->label()}. It cannot change to {$to->label()}.");
    }
}
