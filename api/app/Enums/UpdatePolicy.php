<?php

namespace App\Enums;

/** FR-1106: the update policy of an image. */
enum UpdatePolicy: string
{
    case Automatic = 'automatic';
    case SecurityAutomatic = 'security_automatic';
    case Manual = 'manual';
}
