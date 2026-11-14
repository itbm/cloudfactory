<?php

namespace App\Services;

use App\Models\Website;
use App\Validation\Input;
use App\Validation\InputRules;
use Illuminate\Validation\ValidationException;

/**
 * CF-01: the names of databases and database users. Each name starts with the Unix user of the
 * website and an underscore, for example "acme_x7k2_shop". Thus a tenant cannot use the name of
 * a system database, such as "mysql", or a name of a different tenant.
 */
class DatabaseNames
{
    /** Names that the database servers use. The agent refuses them too. */
    public const RESERVED = InputRules::RESERVED_DATABASE_NAMES;

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
        // The part that the user types has its rule in schema/input-rules.json, as the full name.
        if (! Input::valid('databaseNameSuffix', $suffix) || ! Input::valid('databaseName', $prefix.$suffix)) {
            throw ValidationException::withMessages([$field => InputRules::MESSAGES['databaseNameSuffix']]);
        }
        $name = $prefix.$suffix;
        if (self::reserved($name)) {
            throw ValidationException::withMessages([$field => 'This name is reserved.']);
        }

        return $name;
    }

    public static function reserved(string $name): bool
    {
        return in_array($name, self::RESERVED, true)
            || array_any(InputRules::RESERVED_DATABASE_PREFIXES, fn (string $prefix) => str_starts_with($name, $prefix));
    }
}
