<?php

namespace App\Enums;

enum WebsiteStatus: string
{
    case Starting = 'starting';
    case Running = 'running';
    case Stopping = 'stopping';
    case Stopped = 'stopped';
    case Failed = 'failed';
}
