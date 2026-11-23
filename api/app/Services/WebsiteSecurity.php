<?php

namespace App\Services;

use App\Models\Website;
use App\Validation\Input;
use App\Validation\InputRules;
use Illuminate\Validation\ValidationException;

/**
 * The protection of a website in the edge proxy (docs/architecture.md#protection-of-a-website):
 *
 * - the firewall: Coraza with the OWASP Core Rule Set, in the mode "off", "detect" or "block",
 *   with the rules and the paths that it does not apply to;
 * - the IP address rules: an allow list and a block list;
 * - the password-protected paths: HTTP basic authentication, with the bcrypt hash of each
 *   password;
 * - the sign-in limit: the largest number of POST requests in one minute from one IP address
 *   to the sign-in paths.
 *
 * The panel keeps the value in the column "security" of the website, and adds it to each job
 * with the website (AgentClient). The API never gives the password hashes.
 */
final class WebsiteSecurity
{
    public const MODES = ['off', 'detect', 'block'];

    /** The sign-in paths of WordPress. The web panel offers them when the limit is new. */
    public const WORDPRESS_SIGN_IN_PATHS = ['/wp-login.php', '/xmlrpc.php'];

    /** The bcrypt cost of the passwords of the protected paths. */
    private const BCRYPT_COST = 10;

    /**
     * The stored value of a website, with all keys.
     *
     * @return array{firewall: string, firewallExclusions: list<int>, firewallSkipPaths: list<string>, allow: list<string>, block: list<string>, protectedPaths: list<array{path: string, users: list<array{name: string, passwordHash: string}>}>, signInLimit: int, signInPaths: list<string>}
     */
    public static function of(Website $website): array
    {
        $s = is_array($website->security) ? $website->security : [];

        return [
            'firewall' => in_array($s['firewall'] ?? null, self::MODES, true) ? $s['firewall'] : 'off',
            'firewallExclusions' => array_values(array_map('intval', $s['firewallExclusions'] ?? [])),
            'firewallSkipPaths' => array_values($s['firewallSkipPaths'] ?? []),
            'allow' => array_values($s['allow'] ?? []),
            'block' => array_values($s['block'] ?? []),
            'protectedPaths' => array_values($s['protectedPaths'] ?? []),
            'signInLimit' => (int) ($s['signInLimit'] ?? 0),
            'signInPaths' => array_values($s['signInPaths'] ?? []),
        ];
    }

    /**
     * The value for the API: the same parts, with the names of the users of each protected
     * path and no password hashes.
     *
     * @return array<string, mixed>
     */
    public static function forApi(Website $website): array
    {
        $s = self::of($website);

        return [
            'firewall' => ['mode' => $s['firewall'], 'excludedRules' => $s['firewallExclusions'], 'skippedPaths' => $s['firewallSkipPaths']],
            'ipRules' => ['allow' => $s['allow'], 'block' => $s['block']],
            'protectedPaths' => array_map(fn (array $p) => [
                'path' => $p['path'],
                'users' => array_map(fn (array $u) => ['name' => $u['name']], $p['users']),
            ], $s['protectedPaths']),
            'signInLimit' => ['requestsPerMinute' => $s['signInLimit'], 'paths' => $s['signInPaths']],
        ];
    }

    /**
     * The value for the agent (WebsiteSecurity in agent.proto), or null for a website with no
     * protection.
     *
     * @return array<string, mixed>|null
     */
    public static function forAgent(Website $website): ?array
    {
        if (! self::active($website)) {
            return null;
        }
        $s = self::of($website);

        return [
            'firewall' => $s['firewall'],
            'firewallExclusions' => $s['firewallExclusions'],
            'firewallSkipPaths' => $s['firewallSkipPaths'],
            'allow' => $s['allow'],
            'block' => $s['block'],
            'protectedPaths' => $s['protectedPaths'],
            'signInLimit' => $s['signInLimit'],
            'signInPaths' => $s['signInPaths'],
        ];
    }

    /** True when a part of the protection is on. */
    public static function active(Website $website): bool
    {
        $s = self::of($website);

        return $s['firewall'] !== 'off' || $s['allow'] || $s['block'] || $s['protectedPaths'] || ($s['signInLimit'] > 0 && $s['signInPaths']);
    }

    /** The validation rules of a change (SecurityController::update). Each part is optional. */
    public static function rules(): array
    {
        $path = ['string', Input::rule('securityPath')];

        return [
            'firewall' => ['sometimes', 'array:mode,excludedRules,skippedPaths'],
            'firewall.mode' => ['required_with:firewall', 'string', 'in:'.implode(',', self::MODES)],
            'firewall.excludedRules' => ['sometimes', 'array', 'max:'.InputRules::MAX_FIREWALL_EXCLUSIONS],
            'firewall.excludedRules.*' => ['integer', 'between:1,'.InputRules::MAX_FIREWALL_RULE_ID],
            'firewall.skippedPaths' => ['sometimes', 'array', 'max:'.InputRules::MAX_FIREWALL_SKIP_PATHS],
            'firewall.skippedPaths.*' => $path,
            'ipRules' => ['sometimes', 'array:allow,block'],
            'ipRules.allow' => ['sometimes', 'array', 'max:'.InputRules::MAX_IP_RULES],
            'ipRules.allow.*' => ['string', 'max:64'],
            'ipRules.block' => ['sometimes', 'array', 'max:'.InputRules::MAX_IP_RULES],
            'ipRules.block.*' => ['string', 'max:64'],
            'protectedPaths' => ['sometimes', 'array', 'max:'.InputRules::MAX_PROTECTED_PATHS],
            'protectedPaths.*' => ['array:path,users'],
            'protectedPaths.*.path' => ['required', ...$path],
            'protectedPaths.*.users' => ['required', 'array', 'min:1', 'max:'.InputRules::MAX_PATH_USERS],
            'protectedPaths.*.users.*' => ['array:name,password'],
            'protectedPaths.*.users.*.name' => ['required', 'string', Input::rule('pathUser')],
            // bcrypt uses the first 72 bytes of a password only.
            'protectedPaths.*.users.*.password' => ['sometimes', 'nullable', 'string', 'min:8', 'max:72'],
            'signInLimit' => ['sometimes', 'array:requestsPerMinute,paths'],
            'signInLimit.requestsPerMinute' => ['required_with:signInLimit', 'integer', 'between:0,'.InputRules::MAX_SIGN_IN_LIMIT],
            'signInLimit.paths' => ['sometimes', 'array', 'max:'.InputRules::MAX_SIGN_IN_PATHS],
            'signInLimit.paths.*' => $path,
        ];
    }

    /**
     * The new stored value after a change. A part that the change does not have keeps its
     * value. A user of a protected path with no new password keeps the password hash.
     *
     * @param  array<string, mixed>  $data  The checked request (rules()).
     * @return array<string, mixed>
     *
     * @throws ValidationException
     */
    public static function change(Website $website, array $data): array
    {
        $s = self::of($website);
        if (isset($data['firewall'])) {
            $f = $data['firewall'];
            $s['firewall'] = $f['mode'];
            if (array_key_exists('excludedRules', $f)) {
                $ids = array_values(array_unique(array_map('intval', $f['excludedRules'])));
                sort($ids);
                $s['firewallExclusions'] = $ids;
            }
            if (array_key_exists('skippedPaths', $f)) {
                $s['firewallSkipPaths'] = self::paths($f['skippedPaths']);
            }
        }
        if (isset($data['ipRules'])) {
            foreach (['allow', 'block'] as $list) {
                if (array_key_exists($list, $data['ipRules'])) {
                    $s[$list] = self::ranges($data['ipRules'][$list], "ipRules.{$list}");
                }
            }
        }
        if (array_key_exists('protectedPaths', $data)) {
            $s['protectedPaths'] = self::protectedPaths($s['protectedPaths'], $data['protectedPaths']);
        }
        if (isset($data['signInLimit'])) {
            $limit = (int) $data['signInLimit']['requestsPerMinute'];
            if ($limit !== 0 && $limit < InputRules::MIN_LOW_SIGN_IN_LIMIT) {
                throw ValidationException::withMessages(['signInLimit.requestsPerMinute' => 'Enter 0 for no limit, or a number from '.InputRules::MIN_LOW_SIGN_IN_LIMIT.' to '.InputRules::MAX_SIGN_IN_LIMIT.'.']);
            }
            $s['signInLimit'] = $limit;
            if (array_key_exists('paths', $data['signInLimit'])) {
                $s['signInPaths'] = self::paths($data['signInLimit']['paths']);
            }
            if ($limit > 0 && $s['signInPaths'] === []) {
                throw ValidationException::withMessages(['signInLimit.paths' => 'Add a sign-in path, for example /wp-login.php.']);
            }
        }

        return $s;
    }

    /**
     * A list of IP addresses and ranges in one form: an address with no prefix, or a range
     * with the address of the range, for example "203.0.113.0/24" for "203.0.113.7/24".
     *
     * @param  list<string>  $values
     * @return list<string>
     *
     * @throws ValidationException
     */
    private static function ranges(array $values, string $field): array
    {
        $out = [];
        foreach ($values as $i => $value) {
            $range = self::range((string) $value);
            if ($range === null) {
                throw ValidationException::withMessages(["{$field}.{$i}" => 'Enter an IP address or a range, for example 203.0.113.10, 203.0.113.0/24 or 2001:db8::/32.']);
            }
            $out[] = $range;
        }

        return array_values(array_unique($out));
    }

    /** The form of one IP address or range (ranges()), or null when the value is not one. */
    public static function range(string $value): ?string
    {
        $value = strtolower(trim($value));
        [$address, $bits] = str_contains($value, '/') ? explode('/', $value, 2) : [$value, null];
        if (filter_var($address, FILTER_VALIDATE_IP) === false) {
            return null;
        }
        $packed = (string) inet_pton($address);
        $max = strlen($packed) * 8;
        if ($bits === null) {
            return (string) inet_ntop($packed);
        }
        if (! preg_match('/^(0|[1-9][0-9]{0,2})$/', $bits) || (int) $bits > $max) {
            return null;
        }
        $bits = (int) $bits;
        $masked = '';
        for ($i = 0; $i < strlen($packed); $i++) {
            $keep = max(0, min(8, $bits - $i * 8));
            $masked .= chr(ord($packed[$i]) & ((0xFF << (8 - $keep)) & 0xFF));
        }
        $text = (string) inet_ntop($masked);

        return $bits === $max ? $text : "{$text}/{$bits}";
    }

    /**
     * @param  list<string>  $paths
     * @return list<string>
     */
    private static function paths(array $paths): array
    {
        return array_values(array_unique($paths));
    }

    /**
     * The new protected paths. A user with no new password keeps the hash of the same user of
     * the same path.
     *
     * @param  list<array{path: string, users: list<array{name: string, passwordHash: string}>}>  $old
     * @param  list<array<string, mixed>>  $new
     * @return list<array{path: string, users: list<array{name: string, passwordHash: string}>}>
     *
     * @throws ValidationException
     */
    private static function protectedPaths(array $old, array $new): array
    {
        $hashes = [];
        foreach ($old as $p) {
            foreach ($p['users'] as $u) {
                $hashes[$p['path']][$u['name']] = $u['passwordHash'];
            }
        }
        $out = [];
        $seen = [];
        foreach ($new as $i => $p) {
            if (isset($seen[$p['path']])) {
                throw ValidationException::withMessages(["protectedPaths.{$i}.path" => 'This path is in the list. Add the user to that path.']);
            }
            $seen[$p['path']] = true;
            $users = [];
            foreach ($p['users'] as $j => $u) {
                if (isset($users[$u['name']])) {
                    throw ValidationException::withMessages(["protectedPaths.{$i}.users.{$j}.name" => 'This user is in the list of the path.']);
                }
                $password = $u['password'] ?? null;
                $hash = $password !== null && $password !== ''
                    ? password_hash($password, PASSWORD_BCRYPT, ['cost' => self::BCRYPT_COST])
                    : ($hashes[$p['path']][$u['name']] ?? null);
                if ($hash === null) {
                    throw ValidationException::withMessages(["protectedPaths.{$i}.users.{$j}.password" => 'Enter a password of 8 to 72 characters for the new user.']);
                }
                $users[$u['name']] = ['name' => $u['name'], 'passwordHash' => $hash];
            }
            $out[] = ['path' => $p['path'], 'users' => array_values($users)];
        }

        return $out;
    }
}
