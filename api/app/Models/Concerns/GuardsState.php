<?php

namespace App\Models\Concerns;

use App\Enums\State;
use App\Exceptions\StateChangeNotAllowed;
use Illuminate\Support\Facades\Log;

/**
 * Checks each change of the state of a model against the table of its enum (App\Enums\State).
 * The model casts its state attribute to that enum.
 *
 * The check uses the state in the database, not the state of the copy in memory, because a
 * different request can change the record between the read and the save. In a transaction, the
 * check also locks the row until the transaction ends.
 *
 * A query that changes many rows (Model::where(...)->update(...)) does not start this check.
 * Do not change a state with such a query.
 */
trait GuardsState
{
    /** The name of the attribute that has the state, for example "status". */
    abstract public static function stateAttribute(): string;

    public static function bootGuardsState(): void
    {
        static::updating(function (self $model): void {
            $attribute = static::stateAttribute();
            if (! $model->isDirty($attribute)) {
                return;
            }
            $from = $model->stateInDatabase();
            $to = $model->getAttribute($attribute);
            if ($from !== null && $from !== $to && ! $from->canChangeTo($to)) {
                throw new StateChangeNotAllowed($from, $to);
            }
        });
    }

    /** The state of the record in the database now. */
    public function stateInDatabase(): ?State
    {
        return static::query()->whereKey($this->getKey())->lockForUpdate()->value(static::stateAttribute());
    }

    /**
     * Changes the state for a request of a user. A change that the table does not permit, or a
     * change to the state that the record has, gives HTTP status 409.
     *
     * @param  array<string, mixed>  $attributes  Other attributes to save with the state.
     *
     * @throws StateChangeNotAllowed
     */
    public function changeTo(State $to, array $attributes = []): void
    {
        $from = $this->stateInDatabase();
        if ($from === null || ! $from->canChangeTo($to)) {
            throw new StateChangeNotAllowed($from ?? $this->getAttribute(static::stateAttribute()), $to);
        }
        $this->update([static::stateAttribute() => $to, ...$attributes]);
    }

    /**
     * Changes the state for a result that arrives from a server, for example the result of a
     * job. A late result can ask for a change that the table does not permit. Such a result
     * must not stop the caller, thus this function changes nothing, writes a warning to the log
     * and gives false.
     *
     * @param  array<string, mixed>  $attributes  Other attributes to save with the state.
     */
    public function moveTo(State $to, array $attributes = []): bool
    {
        try {
            $this->update([static::stateAttribute() => $to, ...$attributes]);

            return true;
        } catch (StateChangeNotAllowed $e) {
            Log::warning('A change of state was ignored.', ['model' => static::class, 'id' => $this->getKey(), 'from' => $e->from->value, 'to' => $e->to->value]);
            $this->refresh();

            return false;
        }
    }
}
