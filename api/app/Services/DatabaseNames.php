<?php

namespace App\Services;

use App\Models\Website;
use Illuminate\Validation\ValidationException;

/**
 * CF-01: the names of databases and database users. Each name starts with the Unix user of the
 * website and an underscore, for example "acme_x7k2_shop". Thus a tenant cannot use the name of
 * a system database, such as "mysql", or a name of a different tenant.
 */
class DatabaseNames
{
    /** The part of the name that the user types. */
    public const SUFFIX = '/^[a-z0-9][a-z0-9_]{0,31}$/';

    /** Names that the shared database servers use. The agent refuses them too. */
    public const RESERVED = ['mysql', 'sys', 'information_schema', 'performance_schema', 'postgres', 'template0', 'template1', 'root', 'mariadb.sys', 'healthcheck'];

    public static function prefix(Website $website): string
    {
        return $website->unix_user.'_';
    }

    /**
     * The full name for the part that the user typed. The user can also type the full name.
     *
     * @throws ValidationException
     */
    public static function full(Website $website, string $field, string $typed): string
    {
        $prefix = self::prefix($website);
        $suffix = str_starts_with($typed, $prefix) ? substr($typed, strlen($prefix)) : $typed;
        if (! preg_match(self::SUFFIX, $suffix)) {
            throw ValidationException::withMessages([$field => 'Use 1 to 32 lower-case letters, digits and underscores. Start with a letter or a digit.']);
        }
        $name = $prefix.$suffix;
        if (self::reserved($name)) {
            throw ValidationException::withMessages([$field => 'This name is reserved.']);
        }

        return $name;
    }

    public static function reserved(string $name): bool
    {
        return in_array($name, self::RESERVED, true) || str_starts_with($name, 'pg_');
    }
}
