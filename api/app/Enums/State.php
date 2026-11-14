<?php

namespace App\Enums;

/**
 * A state of a record that changes in steps: a website, a job or a rollout. Each enum has one
 * table with all changes that are permitted. The model checks each change against the table
 * (App\Models\Concerns\GuardsState), thus a change that is not in the table cannot be saved.
 */
interface State extends \BackedEnum
{
    /**
     * The table of changes. Each key is the value of a state. Each value is the list of states
     * that the state can change to. A state with an empty list is final.
     *
     * @return array<string, list<static>>
     */
    public static function changes(): array;

    /** The name of the record in a message, for example "website". */
    public static function what(): string;

    public function canChangeTo(self $to): bool;

    /** The state in a message, for example "rolled back". */
    public function label(): string;
}
