<?php

namespace App\Enums;

enum DomainKind: string
{
    case Primary = 'primary';
    case Redirect = 'redirect';
    case Alias = 'alias';
}
