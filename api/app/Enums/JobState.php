<?php

namespace App\Enums;

enum JobState: string
{
    case Queued = 'queued';
    case Running = 'running';
    case Succeeded = 'succeeded';
    case Failed = 'failed';

    public function finished(): bool
    {
        return $this === self::Succeeded || $this === self::Failed;
    }
}
