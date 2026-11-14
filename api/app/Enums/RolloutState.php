<?php

namespace App\Enums;

enum RolloutState: string implements State
{
    use ChecksChanges;

    case Running = 'running';
    case Paused = 'paused';
    case Done = 'done';
    case RolledBack = 'rolled_back';

    /**
     * Audit finding F06: a rollout that is complete or rolled back does not change. Before, a
     * "resume" of a rolled back rollout started the next stage again, and a "pause" of a
     * complete rollout stopped all new rollouts.
     */
    public static function changes(): array
    {
        return [
            'running' => [self::Paused, self::Done, self::RolledBack],
            'paused' => [self::Running, self::RolledBack],
            'done' => [],
            'rolled_back' => [],
        ];
    }

    public static function what(): string
    {
        return 'rollout';
    }

    /** True while the rollout can continue. Only one such rollout exists at a time. */
    public function active(): bool
    {
        return ! $this->isFinal();
    }

    /**
     * The values of the states of a rollout that can continue, for a query.
     *
     * @return list<string>
     */
    public static function activeValues(): array
    {
        return array_column(array_filter(self::cases(), fn (self $state) => $state->active()), 'value');
    }
}
