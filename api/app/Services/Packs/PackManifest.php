<?php

namespace App\Services\Packs;

use App\Validation\Input;
use App\Validation\InputRules;
use Illuminate\Validation\ValidationException;

/**
 * The manifest of a pack (pack.json, docs/packs.md). The contract "cf-pack/1" is stable: the
 * panel and the agent read the same keys with the same rules (agent/internal/packs). A key
 * that the contract does not have is an error, except a key that starts with "x-", so that
 * the panel never ignores a key of a later contract.
 *
 * The panel reads the manifest only as data. It never runs a program that a manifest names.
 */
class PackManifest
{
    /** The version of the manifest format. */
    public const CONTRACT = 'cf-pack/1';

    /** The version of the contract between the agent and the image of a runtime pack. */
    public const RUNTIME_CONTRACT = 'cf-runtime/1';

    /** The only kind of pack of cf-pack/1. The kind "database" is for a later release. */
    public const KIND_RUNTIME = 'runtime';

    /** The only runtime type of cf-runtime/1: the image runs the start command of the website. */
    public const TYPE_APP = 'app';

    private const TOP = ['contract', 'kind', 'slug', 'name', 'version', 'publisher', 'description', 'runtime', 'images'];

    private const RUNTIME = ['contract', 'type', 'defaultCommand', 'defaultInstall'];

    private const IMAGE = ['version', 'ref', 'digest', 'supportEndsAt'];

    /**
     * Reads and checks a manifest. The result has only the keys of the contract.
     *
     * @return array{contract: string, kind: string, slug: string, name: string, version: string, publisher: string, description: string,
     *     runtime: array{contract: string, type: string, defaultCommand: string, defaultInstall: string},
     *     images: list<array{version: string, ref: string, repository: string, tag: string, digest: string, supportEndsAt: ?string}>}
     *
     * @throws ValidationException
     */
    public static function parse(string $raw): array
    {
        if (strlen($raw) > InputRules::MAX_PACK_MANIFEST_BYTES) {
            self::fail(sprintf('The manifest has %d bytes. The limit is %d.', strlen($raw), InputRules::MAX_PACK_MANIFEST_BYTES));
        }
        $top = self::object(json_decode($raw, false, 16), 'The manifest', self::TOP);
        $errors = [];
        $m = [];
        foreach (['contract', 'kind', 'slug', 'name', 'version', 'publisher', 'description'] as $key) {
            $m[$key] = self::string($top, $key, $key, $errors);
        }
        if ($m['contract'] !== self::CONTRACT) {
            self::fail("The contract \"{$m['contract']}\" is not known. This panel reads \"".self::CONTRACT.'".');
        }
        if ($m['kind'] === 'database') {
            self::fail('Database engine packs are not available yet. This release installs runtime packs only.');
        }
        if ($m['kind'] !== self::KIND_RUNTIME) {
            self::fail("The kind \"{$m['kind']}\" is not known. Use \"".self::KIND_RUNTIME.'".');
        }
        if (! Input::valid('packSlug', $m['slug'])) {
            $errors[] = 'slug: '.InputRules::MESSAGES['packSlug'];
        } elseif (in_array($m['slug'], InputRules::RESERVED_RUNTIME_NAMES, true)) {
            $errors[] = "The pack name \"{$m['slug']}\" is the name of a built-in runtime.";
        }
        if (! Input::valid('packVersion', $m['version'])) {
            $errors[] = 'version: '.InputRules::MESSAGES['packVersion'];
        }
        self::text('name', $m['name'], 1, 60, $errors);
        self::text('publisher', $m['publisher'], 1, 100, $errors);
        self::text('description', $m['description'], 0, 500, $errors);

        $m['runtime'] = ['contract' => '', 'type' => '', 'defaultCommand' => '', 'defaultInstall' => ''];
        if (! property_exists($top, 'runtime')) {
            $errors[] = 'A runtime pack needs the key runtime.';
        } else {
            $runtime = self::object($top->runtime, 'runtime', self::RUNTIME);
            foreach (self::RUNTIME as $key) {
                $m['runtime'][$key] = self::string($runtime, $key, "runtime.{$key}", $errors);
            }
            if ($m['runtime']['contract'] !== self::RUNTIME_CONTRACT) {
                $errors[] = "The runtime contract \"{$m['runtime']['contract']}\" is not known. This panel reads \"".self::RUNTIME_CONTRACT.'".';
            }
            if ($m['runtime']['type'] !== self::TYPE_APP) {
                $errors[] = "The runtime type \"{$m['runtime']['type']}\" is not known. ".self::RUNTIME_CONTRACT.' has only the type "'.self::TYPE_APP.'".';
            }
            self::command('runtime.defaultCommand', $m['runtime']['defaultCommand'], true, $errors);
            self::command('runtime.defaultInstall', $m['runtime']['defaultInstall'], false, $errors);
        }

        $images = $top->images ?? null;
        $m['images'] = [];
        if (! is_array($images)) {
            $errors[] = 'images must be a list.';
            $images = [];
        } elseif ($images === [] || count($images) > InputRules::MAX_PACK_IMAGES) {
            $errors[] = sprintf('A pack has 1 to %d images, not %d.', InputRules::MAX_PACK_IMAGES, count($images));
        }
        $seen = [];
        foreach (array_values($images) as $n => $value) {
            $what = "images[{$n}]";
            if (! is_object($value)) {
                $errors[] = "{$what} is not a JSON object.";

                continue;
            }
            $fields = self::object($value, $what, self::IMAGE);
            $img = [];
            foreach (self::IMAGE as $key) {
                $img[$key] = self::string($fields, $key, "{$what}.{$key}", $errors);
            }
            if (! Input::valid('packImageVersion', $img['version'])) {
                $errors[] = "{$what}.version: ".InputRules::MESSAGES['packImageVersion'];
            }
            if (! Input::valid('image', $img['ref'])) {
                $errors[] = "{$what}.ref: ".InputRules::MESSAGES['image'];
            }
            if ($img['digest'] === '') {
                $errors[] = "{$what} needs a digest.";
            } elseif (! Input::valid('digest', $img['digest'])) {
                $errors[] = "{$what}.digest: ".InputRules::MESSAGES['digest'];
            }
            if ($img['supportEndsAt'] !== '' && ! self::date($img['supportEndsAt'])) {
                $errors[] = "{$what}.supportEndsAt must be a date such as 2026-08-31.";
            }
            if (isset($seen[$img['version']])) {
                $errors[] = "The version \"{$img['version']}\" is in the pack two times.";
            }
            $seen[$img['version']] = true;
            $repository = self::repository($img['ref']);
            $m['images'][] = [
                'version' => $img['version'], 'ref' => $img['ref'], 'repository' => $repository,
                'tag' => (string) substr($img['ref'], strlen($repository) + 1), 'digest' => $img['digest'],
                'supportEndsAt' => $img['supportEndsAt'] === '' ? null : $img['supportEndsAt'],
            ];
        }
        if ($errors !== []) {
            throw ValidationException::withMessages(['manifest' => $errors]);
        }

        return $m;
    }

    /** An image reference with no tag and no digest, for example "ghcr.io/example/cf-golang". */
    public static function repository(string $ref): string
    {
        $ref = explode('@', $ref, 2)[0];
        $colon = strrpos($ref, ':');
        if ($colon !== false && $colon > (int) strrpos($ref, '/')) {
            $ref = substr($ref, 0, $colon);
        }

        return $ref;
    }

    /**
     * Checks a JSON object and its keys.
     *
     * @param  list<string>  $known
     */
    private static function object(mixed $value, string $what, array $known): object
    {
        if (! is_object($value)) {
            self::fail("{$what} is not a JSON object.");
        }
        $unknown = array_values(array_filter(array_keys(get_object_vars($value)), fn ($k) => ! in_array($k, $known, true) && ! str_starts_with((string) $k, 'x-')));
        if ($unknown !== []) {
            sort($unknown);
            self::fail("{$what} has the key \"{$unknown[0]}\", which the contract ".self::CONTRACT.' does not have. The pack may need a later release of Cloudfactory.');
        }

        return $value;
    }

    /** @param  list<string>  $errors */
    private static function string(object $o, string $key, string $what, array &$errors): string
    {
        if (! property_exists($o, $key)) {
            return '';
        }
        if (! is_string($o->{$key})) {
            $errors[] = "{$what}: the value must be a string.";

            return '';
        }

        return $o->{$key};
    }

    /** @param  list<string>  $errors */
    private static function text(string $key, string $s, int $min, int $max, array &$errors): void
    {
        $n = mb_strlen($s);
        if ($n < $min || $n > $max) {
            $errors[] = $min > 0 ? "{$key} needs 1 to {$max} characters." : "{$key} can have up to {$max} characters.";
        } elseif (preg_match('/\p{Cc}/u', $s) === 1) {
            $errors[] = "{$key} has a control character.";
        }
    }

    /** @param  list<string>  $errors */
    private static function command(string $key, string $s, bool $required, array &$errors): void
    {
        if ($s === '') {
            if ($required) {
                $errors[] = "{$key} is necessary.";
            }
        } elseif (! Input::valid('commandLine', $s)) {
            $errors[] = "{$key}: ".InputRules::MESSAGES['commandLine'];
        }
    }

    private static function date(string $s): bool
    {
        return preg_match('/^(\d{4})-(\d{2})-(\d{2})$/D', $s, $m) === 1 && checkdate((int) $m[2], (int) $m[3], (int) $m[1]);
    }

    /** @throws ValidationException */
    private static function fail(string $message): never
    {
        throw ValidationException::withMessages(['manifest' => $message]);
    }
}
