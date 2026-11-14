<?php

namespace App\Enums;

enum WebsiteStatus: string implements State
{
    use ChecksChanges;

    case Starting = 'starting';
    case Running = 'running';
    case Stopping = 'stopping';
    case Stopped = 'stopped';
    case Failed = 'failed';

    /**
     * "Starting" is the state of a new website, and of a website with a start job. "Failed"
     * means that the panel could not make the website: it has no container on the server. Thus
     * the only change from "failed" is "starting", when the panel makes the website again. The
     * result of an old power job cannot show a failed website as "running" or "stopped".
     *
     * The results of two power jobs can arrive after the second request changed the state. For
     * example: start, then stop, then the result of the start. Thus the table permits "stopping"
     * to "running" and "running" to "stopped".
     *
     * A website that runs can get a start job: when a suspension ends, the panel starts each
     * website that the suspension stopped, also if its stop job failed. A website that is
     * stopped cannot get a stop job.
     */
    public static function changes(): array
    {
        return [
            'starting' => [self::Running, self::Stopping, self::Stopped, self::Failed],
            'running' => [self::Stopping, self::Stopped, self::Starting, self::Failed],
            'stopping' => [self::Stopped, self::Running, self::Starting, self::Failed],
            'stopped' => [self::Starting, self::Running, self::Failed],
            'failed' => [self::Starting],
        ];
    }

    public static function what(): string
    {
        return 'website';
    }
}
