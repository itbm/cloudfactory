<?php

namespace App\Enums;

enum JobState: string implements State
{
    use ChecksChanges;

    case Queued = 'queued';
    case Running = 'running';
    case Succeeded = 'succeeded';
    case Failed = 'failed';

    /**
     * A job in the queue can end before the panel records that it runs, because a fast agent
     * reports first (audit finding R1). The panel puts a running job with no news back in the
     * queue, to send it again. A job that has ended does not change.
     */
    public static function changes(): array
    {
        return [
            'queued' => [self::Running, self::Succeeded, self::Failed],
            'running' => [self::Queued, self::Succeeded, self::Failed],
            'succeeded' => [],
            'failed' => [],
        ];
    }

    public static function what(): string
    {
        return 'job';
    }

    public function finished(): bool
    {
        return $this->isFinal();
    }
}
