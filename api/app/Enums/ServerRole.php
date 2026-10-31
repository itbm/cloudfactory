<?php

namespace App\Enums;

/** FR-1202: the roles of a server. */
enum ServerRole: string
{
    case Web = 'web';
    case Database = 'database';
    case Email = 'email';
    case Dns = 'dns';
    case Backup = 'backup';
}
