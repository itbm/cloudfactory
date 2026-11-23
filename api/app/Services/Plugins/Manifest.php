<?php

namespace App\Services\Plugins;

use App\Enums\Role;
use App\Validation\Input;
use App\Validation\InputRules;
use Illuminate\Support\Facades\Validator;
use Illuminate\Validation\Rule;
use Illuminate\Validation\ValidationException;

/**
 * The manifest of a plugin (plugin.json). It gives the name of the plugin, the scopes that its
 * token gets, the events that it receives, its pages, its actions and its before hooks. A
 * manifest with an image can also run in a container that the panel operates (Hosting). A
 * plugin can also offer external DNS providers (App\Services\Dns\Providers). The
 * image has a digest, so the signature of the manifest also covers the image. The paths are relative to the
 * address where the administrator runs the plugin.
 *
 * The panel reads the manifest only as data. It never runs code from a plugin.
 */
class Manifest
{
    /** The version of the manifest format. */
    public const SCHEMA = 1;

    /** A slug: lower-case letters, digits and "-", 1 to 40 characters. */
    public const SLUG = '/^[a-z0-9]([a-z0-9-]{0,38}[a-z0-9])?$/D';

    /** A path of the plugin: it starts with "/" and has no "..", no query and no fragment. */
    public const PATH = '#^/[A-Za-z0-9._~/-]{0,199}$#D';

    /** The ID of a credential of a DNS provider: a letter, then letters and digits, up to 40 characters. */
    public const CREDENTIAL = '/^[A-Za-z][A-Za-z0-9]{0,39}$/D';

    public const VERSION = '/^\d{1,6}\.\d{1,6}\.\d{1,6}([-+][0-9A-Za-z.-]{1,30})?$/D';

    /**
     * The scopes that a plugin cannot have. With "tokens", a plugin can make a token with more
     * time. With "users:write", it can reset the password of a user and sign in as that user.
     * With "plugins", it can change its own scopes or install a different plugin. With
     * "packs", it can install a runtime on each server.
     */
    public const FORBIDDEN_SCOPES = ['tokens:read', 'tokens:write', 'users:write', 'plugins:read', 'plugins:write', 'packs:read', 'packs:write'];

    /**
     * Adversarial review, finding A7-02: the scope that a plugin must have to get an event or
     * a before hook. An event has data of all customers, thus a plugin gets it only when it
     * can read that data with its token.
     */
    public const SCOPE_OF = [
        'audit.recorded' => 'audit:read', 'job.finished' => 'websites:read',
        'website.created' => 'websites:read', 'website.changed' => 'websites:read', 'website.removed' => 'websites:read',
        'domain.added' => 'websites:read', 'domain.verified' => 'websites:read', 'domain.removed' => 'websites:read',
        'database.created' => 'databases:read', 'database.removed' => 'databases:read',
        'backup.started' => 'backups:read', 'restore.started' => 'backups:read', 'deploy.started' => 'websites:read',
        'customer.created' => 'customers:read', 'customer.changed' => 'customers:read', 'user.created' => 'users:read',
        'uptime.down' => 'websites:read', 'uptime.up' => 'websites:read',
        'website.creating' => 'websites:read', 'website.removing' => 'websites:read', 'domain.adding' => 'websites:read',
        'database.creating' => 'databases:read', 'customer.creating' => 'customers:read',
    ];

    /**
     * True when the scopes permit the event or the hook. A scope to change data includes the
     * scope to read it.
     *
     * @param  list<string>  $scopes
     */
    public static function permits(array $scopes, string $event): bool
    {
        $scope = self::SCOPE_OF[$event] ?? null;

        return $scope === null || in_array($scope, $scopes, true) || in_array(str_replace(':read', ':write', $scope), $scopes, true);
    }

    /** @return list<string> */
    public static function allowedScopes(): array
    {
        return array_values(array_diff(config('cloudfactory.scopes'), self::FORBIDDEN_SCOPES));
    }

    /**
     * Reads and checks a manifest. The result has only the known keys, so the panel keeps no
     * value that it does not check.
     *
     * @return array<string, mixed>
     *
     * @throws ValidationException
     */
    public static function parse(string $json): array
    {
        if (strlen($json) > 32768) {
            throw ValidationException::withMessages(['manifest' => 'The manifest is too large. The limit is 32 KB.']);
        }
        $data = json_decode($json, true);
        if (! is_array($data) || array_is_list($data)) {
            throw ValidationException::withMessages(['manifest' => 'The manifest is not a JSON object.']);
        }

        $roles = array_map(fn (Role $r) => $r->value, Role::cases());
        $v = Validator::make($data, [
            'schema' => ['required', 'integer', Rule::in([self::SCHEMA])],
            'slug' => ['required', 'string', 'regex:'.self::SLUG],
            'name' => ['required', 'string', 'max:100'],
            'version' => ['required', 'string', 'regex:'.self::VERSION],
            'publisher' => ['required', 'string', 'max:100'],
            'description' => ['nullable', 'string', 'max:500'],
            'scopes' => ['present', 'array', 'max:30'],
            'scopes.*' => ['string', 'distinct', Rule::in(self::allowedScopes())],
            'events' => ['sometimes', 'array', 'max:20'],
            'events.*' => ['string', 'distinct', Rule::in(Events::NAMES)],
            'webhook' => ['required_with:events', 'nullable', 'string', 'regex:'.self::PATH, 'not_regex:#\.\.#'],
            'pages' => ['sometimes', 'array', 'max:10'],
            'pages.*.id' => ['required', 'string', 'regex:'.self::SLUG, 'distinct'],
            'pages.*.title' => ['required', 'string', 'max:40'],
            'pages.*.path' => ['required', 'string', 'regex:'.self::PATH, 'not_regex:#\.\.#'],
            'pages.*.roles' => ['required', 'array', 'min:1'],
            'pages.*.roles.*' => ['string', 'distinct', Rule::in($roles)],
            'actions' => ['sometimes', 'array', 'max:20'],
            'actions.*.id' => ['required', 'string', 'regex:'.self::SLUG, 'distinct'],
            'actions.*.title' => ['required', 'string', 'max:40'],
            'actions.*.target' => ['required', 'string', Rule::in(Actions::TARGETS)],
            'actions.*.path' => ['required', 'string', 'regex:'.self::PATH, 'not_regex:#\.\.#'],
            'actions.*.roles' => ['required', 'array', 'min:1'],
            'actions.*.roles.*' => ['string', 'distinct', Rule::in($roles)],
            'actions.*.confirm' => ['nullable', 'string', 'max:200'],
            'hooks' => ['sometimes', 'array', 'max:20'],
            'hooks.*.event' => ['required', 'string', 'distinct', Rule::in(Hooks::NAMES)],
            'hooks.*.path' => ['required', 'string', 'regex:'.self::PATH, 'not_regex:#\.\.#'],
            'hooks.*.onError' => ['sometimes', 'string', Rule::in(['allow', 'refuse'])],
            'image' => ['sometimes', 'nullable', 'array:ref,digest,port'],
            'image.ref' => ['required_with:image', 'string', Input::rule('image')],
            'image.digest' => ['required_with:image', 'string', 'regex:/^sha256:[0-9a-f]{64}$/D'],
            'image.port' => ['sometimes', 'integer', 'between:1024,65535'],
            'dnsProviders' => ['sometimes', 'array', 'max:10'],
            'dnsProviders.*.id' => ['required', 'string', 'regex:'.self::SLUG, 'distinct'],
            'dnsProviders.*.name' => ['required', 'string', 'max:40'],
            'dnsProviders.*.path' => ['required', 'string', 'regex:'.self::PATH, 'not_regex:#\.\.#'],
            'dnsProviders.*.dnssec' => ['sometimes', 'boolean'],
            'dnsProviders.*.types' => ['sometimes', 'array', 'min:1'],
            'dnsProviders.*.types.*' => ['string', Rule::in(InputRules::DNS_RECORD_TYPES)],
            'dnsProviders.*.credentials' => ['sometimes', 'array', 'max:10'],
            'dnsProviders.*.credentials.*.id' => ['required', 'string', 'regex:'.self::CREDENTIAL],
            'dnsProviders.*.credentials.*.label' => ['required', 'string', 'max:60'],
            'dnsProviders.*.credentials.*.secret' => ['sometimes', 'boolean'],
            'dnsProviders.*.credentials.*.optional' => ['sometimes', 'boolean'],
            'dnsProviders.*.credentials.*.hint' => ['nullable', 'string', 'max:200'],
            'commands' => ['sometimes', 'array', 'max:20'],
            'commands.*.name' => ['required', 'string', 'regex:'.self::SLUG, 'distinct'],
            'commands.*.description' => ['nullable', 'string', 'max:200'],
        ], [
            'scopes.*.in' => 'A plugin cannot have the scope ":input". Plugins cannot have the scopes '.implode(', ', self::FORBIDDEN_SCOPES).'.',
            'events.*.in' => 'The event ":input" is not known. Use one of: '.implode(', ', Events::NAMES).'.',
            'slug.regex' => 'Use lower-case letters, digits and "-", up to 40 characters.',
            'version.regex' => 'Use a version such as 1.2.0.',
            'webhook.required_with' => 'A plugin that receives events must give the path of its webhook.',
            'webhook.regex' => 'Give a path that starts with "/", for example /hooks/cloudfactory.',
            'webhook.not_regex' => 'A path cannot have "..".',
            'pages.*.path.regex' => 'Give a path that starts with "/", for example /panel/status.',
            'pages.*.path.not_regex' => 'A path cannot have "..".',
            'actions.*.target.in' => 'The target of an action is one of: '.implode(', ', Actions::TARGETS).'.',
            'actions.*.path.regex' => 'Give a path that starts with "/", for example /actions/check.',
            'actions.*.path.not_regex' => 'A path cannot have "..".',
            'hooks.*.event.in' => 'The hook ":input" is not known. Use one of: '.implode(', ', Hooks::NAMES).'.',
            'hooks.*.path.regex' => 'Give a path that starts with "/", for example /hooks/before.',
            'hooks.*.path.not_regex' => 'A path cannot have "..".',
            'image.digest.regex' => 'Give the digest of the image, for example sha256: and 64 hexadecimal digits. The signature of the manifest then covers the image.',
            'image.port.between' => 'The port of the image is from 1024 to 65535.',
            'dnsProviders.*.path.regex' => 'Give a path that starts with "/", for example /dns/cloudflare.',
            'dnsProviders.*.path.not_regex' => 'A path cannot have "..".',
            'dnsProviders.*.types.*.in' => 'The record type ":input" is not known. Use some of: '.implode(', ', InputRules::DNS_RECORD_TYPES).'.',
            'dnsProviders.*.credentials.*.id.regex' => 'The ID of a credential has letters and digits, and starts with a letter, for example apiToken.',
        ]);
        $v->validate();
        foreach ($data['dnsProviders'] ?? [] as $provider) {
            $ids = array_column($provider['credentials'] ?? [], 'id');
            $types = $provider['types'] ?? [];
            if (count($ids) !== count(array_unique($ids)) || count($types) !== count(array_unique($types))) {
                throw ValidationException::withMessages(['dnsProviders' => "The DNS provider \"{$provider['id']}\" has a credential or a record type two times."]);
            }
        }
        foreach ([...array_map(fn ($e) => ['events', $e], $data['events'] ?? []), ...array_map(fn ($h) => ['hooks', $h['event']], $data['hooks'] ?? [])] as [$field, $event]) {
            if (! self::permits($data['scopes'], $event)) {
                throw ValidationException::withMessages([$field => "A plugin that gets \"{$event}\" must have the scope ".self::SCOPE_OF[$event].'.']);
            }
        }

        $out = [
            'schema' => self::SCHEMA,
            'slug' => $data['slug'],
            'name' => $data['name'],
            'version' => $data['version'],
            'publisher' => $data['publisher'],
            'description' => $data['description'] ?? '',
            'scopes' => array_values($data['scopes']),
            'events' => array_values($data['events'] ?? []),
            'webhook' => $data['webhook'] ?? null,
            'pages' => array_map(fn (array $p) => [
                'id' => $p['id'], 'title' => $p['title'], 'path' => $p['path'], 'roles' => array_values($p['roles']),
            ], array_values($data['pages'] ?? [])),
            'actions' => array_map(fn (array $a) => [
                'id' => $a['id'], 'title' => $a['title'], 'target' => $a['target'], 'path' => $a['path'],
                'roles' => array_values($a['roles']), 'confirm' => $a['confirm'] ?? '',
            ], array_values($data['actions'] ?? [])),
            'hooks' => array_map(fn (array $h) => [
                'event' => $h['event'], 'path' => $h['path'], 'onError' => $h['onError'] ?? 'allow',
            ], array_values($data['hooks'] ?? [])),
            'image' => isset($data['image']) ? [
                'ref' => $data['image']['ref'], 'digest' => $data['image']['digest'], 'port' => (int) ($data['image']['port'] ?? 8080),
            ] : null,
            'commands' => array_map(fn (array $c) => [
                'name' => $c['name'], 'description' => $c['description'] ?? '',
            ], array_values($data['commands'] ?? [])),
            'dnsProviders' => array_map(fn (array $p) => [
                'id' => $p['id'], 'name' => $p['name'], 'path' => $p['path'], 'dnssec' => (bool) ($p['dnssec'] ?? false),
                // The order of the panel, so that the list does not change with the order of the manifest.
                'types' => array_values(array_intersect(InputRules::DNS_RECORD_TYPES, $p['types'] ?? InputRules::DNS_RECORD_TYPES)),
                'credentials' => array_map(fn (array $c) => [
                    'id' => $c['id'], 'label' => $c['label'], 'secret' => (bool) ($c['secret'] ?? false),
                    'optional' => (bool) ($c['optional'] ?? false), 'hint' => $c['hint'] ?? '',
                ], array_values($p['credentials'] ?? [])),
            ], array_values($data['dnsProviders'] ?? [])),
        ];
        sort($out['scopes']);

        return $out;
    }

    /**
     * Checks the address where the administrator runs the plugin. The plugin must not be on the
     * address of the panel: its pages would then have the origin of the panel, and the frame
     * would not separate them from the panel.
     *
     * @throws ValidationException
     */
    public static function checkBaseUrl(string $url, bool $developerMode): string
    {
        $parts = parse_url($url);
        $scheme = strtolower($parts['scheme'] ?? '');
        $host = strtolower($parts['host'] ?? '');
        $fail = fn (string $m) => throw ValidationException::withMessages(['baseUrl' => $m]);
        if (strlen($url) > 255 || $host === '' || isset($parts['user']) || isset($parts['pass']) || isset($parts['query']) || isset($parts['fragment'])) {
            $fail('Give an address such as https://plugin.example.com, with no user name, query or fragment.');
        }
        if ($scheme !== 'https' && ! ($developerMode && $scheme === 'http')) {
            $fail('The address of a plugin must use HTTPS. Only the developer mode accepts HTTP.');
        }
        $panelHost = strtolower((string) parse_url((string) config('app.url'), PHP_URL_HOST));
        if ($panelHost !== '' && $host === $panelHost) {
            $fail('A plugin cannot use the address of the panel. Run it on a different host name, so that its pages stay separate from the panel.');
        }

        // Adversarial review, finding A7-08: the panel compares this address with the origin that
        // a browser gives: lower-case letters, and no port when the port is the default port.
        $port = $parts['port'] ?? null;
        $default = $scheme === 'https' ? 443 : 80;
        $bracket = str_contains($host, ':') && ! str_starts_with($host, '[') ? "[{$host}]" : $host;

        return $scheme.'://'.$bracket.($port !== null && $port !== $default ? ":{$port}" : '').rtrim($parts['path'] ?? '', '/');
    }
}
