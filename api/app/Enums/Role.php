<?php

namespace App\Enums;

/** FR-101: the three roles. */
enum Role: string
{
    case Administrator = 'administrator';
    case Reseller = 'reseller';
    case Owner = 'owner';
}
