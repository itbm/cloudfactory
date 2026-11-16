<?php

namespace App\Http\Controllers\Api;

use App\Enums\Role;
use App\Http\Controllers\Controller;
use App\Http\Middleware\AllowPluginOrigins;
use App\Models\Customer;
use App\Models\Plugin;
use App\Models\PluginDelivery;
use App\Models\PluginKey;
use App\Models\Server;
use App\Models\Setting;
use App\Models\User;
use App\Services\Audit;
use App\Services\Plugins\Actions;
use App\Services\Plugins\Caller;
use App\Services\Plugins\Events;
use App\Services\Plugins\Hosting;
use App\Services\Plugins\Manifest;
use App\Services\Plugins\Signatures;
use Illuminate\Http\JsonResponse;
use Illuminate\Http\Request;
use Illuminate\Support\Facades\DB;
use Illuminate\Support\Facades\Log;
use Illuminate\Support\Str;
use Illuminate\Validation\ValidationException;

/**
 * Plugins. A plugin is a program that the administrator runs outside the panel. The panel
 * never runs the code of a plugin, and a plugin never connects to an agent. A plugin can do
 * only what its token permits:
 *
 * - The token belongs to a user of the plugin, and has only the scopes of the manifest. The
 *   manifest cannot ask for the scopes in Manifest::FORBIDDEN_SCOPES.
 * - The panel sends the events of the manifest to the webhook of the plugin, with a signature.
 * - A page of the plugin opens in a frame with the origin of the plugin. The page gets a token
 *   of the user who opens it, with the scopes of the plugin and for 15 minutes.
 * - An action of the plugin is a button on a website, a server or a customer. The plugin gets a
 *   token of the user who selects it, with the scopes of the plugin and for 15 minutes.
 * - A before hook lets the plugin refuse a change (App\Services\Plugins\Hooks).
 *
 * Only an administrator installs a plugin, with the password and the TOTP code. The panel
 * installs only a manifest that a trusted key signed, unless the developer mode is on.
 */
class PluginController extends Controller
{
    use Concerns;

    public function __construct(private Hosting $hosting) {}

    /** A plugin token is valid for one year (FR-104: each token has an expiry date). */
    private const TOKEN_DAYS = 365;

    /** The token of a page is valid for 15 minutes. The page asks for a new one. */
    private const PAGE_TOKEN_MINUTES = 15;

    /** The name of the token of a plugin, and the start of the name of a page token. */
    public const TOKEN_NAME = 'Plugin';

    public const PAGE_TOKEN_PREFIX = 'Plugin page ';

    public function index(): JsonResponse
    {
        return response()->json(['data' => Plugin::with(['key', 'user', 'server'])->orderBy('name')->get()->map(fn (Plugin $p) => self::present($p))]);
    }

    public function show(string $id): JsonResponse
    {
        return response()->json(self::present(Plugin::with(['key', 'user', 'server'])->findOrFail($id)));
    }

    /** Reads a manifest and gives what it asks for, before the administrator installs it. */
    public function preview(Request $request): JsonResponse
    {
        $data = $request->validate([
            'manifest' => ['required', 'string'],
            'signature' => ['nullable', 'string', 'max:512'],
        ]);
        $spec = Manifest::parse($data['manifest']);
        $key = $data['signature'] ?? null ? Signatures::signer($data['manifest'], $data['signature']) : null;

        return response()->json([
            'manifest' => $spec,
            'signedBy' => $key ? self::presentKey($key) : null,
            'installed' => Plugin::where('slug', $spec['slug'])->value('id'),
            'developerMode' => self::developerMode(),
        ]);
    }

    public function store(Request $request): JsonResponse
    {
        $data = $request->validate([
            'manifest' => ['required', 'string'],
            'signature' => ['nullable', 'string', 'max:512'],
            // A plugin outside the panel has an address. A plugin that the panel runs has a server
            // and a host name instead (Hosting).
            'hosted' => ['sometimes', 'boolean'],
            'baseUrl' => ['exclude_if:hosted,true', 'required', 'string', 'max:255'],
            'serverId' => ['exclude_unless:hosted,true', 'nullable', 'string', 'max:64'],
            'host' => ['exclude_unless:hosted,true', 'nullable', 'string', 'max:253'],
        ]);
        AuthController::confirmIdentity($request);
        $spec = Manifest::parse($data['manifest']);
        $key = self::checkSignature($data['manifest'], $data['signature'] ?? null);
        $hosted = (bool) ($data['hosted'] ?? false);
        [$server, $host] = $hosted ? Hosting::place($spec, $data['serverId'] ?? null, $data['host'] ?? null) : [null, null];
        $baseUrl = Manifest::checkBaseUrl($hosted ? "https://{$host}" : $data['baseUrl'], self::developerMode());
        if (Plugin::where('slug', $spec['slug'])->exists()) {
            throw ValidationException::withMessages(['manifest' => "The plugin \"{$spec['slug']}\" is installed. To install a new version, update it."]);
        }

        $secret = 'whsec_'.Str::random(40);
        [$plugin, $token] = DB::transaction(function () use ($spec, $data, $key, $baseUrl, $secret, $server, $host) {
            $plugin = Plugin::create([
                'slug' => $spec['slug'], 'name' => $spec['name'], 'version' => $spec['version'], 'publisher' => $spec['publisher'],
                'manifest' => json_encode($spec, JSON_UNESCAPED_SLASHES), 'signature' => $data['signature'] ?? null,
                'plugin_key_id' => $key?->id, 'enabled' => true, 'base_url' => $baseUrl, 'webhook_secret' => $secret,
                'server_id' => $server?->id, 'host' => $host, 'unix_user' => $server ? Hosting::unixUser() : null,
                'container_status' => $server ? Hosting::STARTING : null,
            ]);
            // The user of the plugin. It has the role "administrator", so the scopes of its token
            // are the only limit. Its password is random and nobody knows it, and the sign-in
            // refuses a plugin user also with the correct password.
            $user = User::create([
                'name' => "Plugin: {$spec['name']}", 'email' => "plugin-{$spec['slug']}@plugins.invalid",
                'password' => Str::random(64), 'role' => Role::Administrator, 'plugin_id' => $plugin->id,
            ]);
            $plugin->update(['user_id' => $user->id]);
            $token = self::newToken($plugin, $user);
            if ($plugin->hosted()) {
                $plugin->update(['hosted_token' => $token]);
            }

            return [$plugin, $token];
        });
        if ($plugin->hosted()) {
            $this->hosting->start($plugin, false);
        }
        AllowPluginOrigins::forget();
        Audit::record($request, 'Installed plugin', "{$spec['name']} {$spec['version']}".($plugin->hosted() ? " on {$server->name}" : ''));

        return response()->json([
            'plugin' => self::present($plugin->fresh(['key', 'user', 'server'])),
            // The panel gives the token and the secret to the container of a plugin that it
            // runs, so nobody needs to see them.
            'token' => $plugin->hosted() ? null : $token,
            'webhookSecret' => $plugin->hosted() ? null : $secret,
        ], 201);
    }

    /** A new version of a plugin, with a new signed manifest. */
    public function upgrade(Request $request, string $id): JsonResponse
    {
        $plugin = Plugin::findOrFail($id);
        $data = $request->validate([
            'manifest' => ['required', 'string'],
            'signature' => ['nullable', 'string', 'max:512'],
        ]);
        AuthController::confirmIdentity($request);
        $spec = Manifest::parse($data['manifest']);
        if ($spec['slug'] !== $plugin->slug) {
            throw ValidationException::withMessages(['manifest' => "This manifest is for the plugin \"{$spec['slug']}\", not \"{$plugin->slug}\"."]);
        }
        $key = self::checkSignature($data['manifest'], $data['signature'] ?? null);
        if ($plugin->hosted() && empty($spec['image'])) {
            throw ValidationException::withMessages(['manifest' => 'The panel runs this plugin, so its manifest must have an image.']);
        }
        $old = $plugin->version;
        $oldImage = $plugin->image();
        DB::transaction(function () use ($plugin, $spec, $data, $key) {
            $plugin->update([
                'name' => $spec['name'], 'version' => $spec['version'], 'publisher' => $spec['publisher'],
                'manifest' => json_encode($spec, JSON_UNESCAPED_SLASHES), 'signature' => $data['signature'] ?? null,
                'plugin_key_id' => $key?->id,
            ]);
            // The token keeps its value and gets the scopes of the new manifest.
            $plugin->user?->tokens()->where('name', self::TOKEN_NAME)->update(['abilities' => json_encode($spec['scopes'])]);
            $plugin->user?->update(['name' => "Plugin: {$spec['name']}"]);
        });
        // A new image, or a new port: the agent starts the new container, then stops the old one.
        if ($plugin->hosted() && $oldImage !== $spec['image'] && self::canStart($plugin)) {
            $this->hosting->start($plugin->fresh(), $plugin->container_status !== Hosting::FAILED);
        }
        // The tokens of pages and actions had the scopes of the old manifest.
        self::revokePageTokens($plugin);
        AllowPluginOrigins::forget();
        Audit::record($request, 'Updated plugin', "{$spec['name']} {$old} to {$spec['version']}");

        return response()->json(self::present($plugin->fresh(['key', 'user'])));
    }

    public function update(Request $request, string $id): JsonResponse
    {
        $plugin = Plugin::findOrFail($id);
        $data = $request->validate([
            'enabled' => ['sometimes', 'boolean'],
            'baseUrl' => ['sometimes', 'string', 'max:255'],
        ]);
        if (array_key_exists('baseUrl', $data)) {
            if ($plugin->hosted()) {
                throw ValidationException::withMessages(['baseUrl' => 'The panel runs this plugin on its own host name. Remove the plugin and install it again to change it.']);
            }
            // A new address sends the events and the page tokens to a different host.
            AuthController::confirmIdentity($request);
            $newUrl = Manifest::checkBaseUrl($data['baseUrl'], self::developerMode());
            // Adversarial review, finding A7-10: the old host must not keep tokens of users,
            // or the secret that signs the requests to the new host.
            if ($newUrl !== $plugin->base_url) {
                $plugin->webhook_secret = $newSecret = 'whsec_'.Str::random(40);
                self::revokePageTokens($plugin);
            }
            $plugin->base_url = $newUrl;
        }
        $power = array_key_exists('enabled', $data) && $plugin->hosted() && $plugin->enabled !== $data['enabled'];
        if (array_key_exists('enabled', $data)) {
            $plugin->enabled = $data['enabled'];
        }
        $plugin->save();
        // A plugin that the panel runs stops when it is off, and starts again when it is on.
        if ($power) {
            $this->hosting->power($plugin, $plugin->enabled);
        }
        if (! $plugin->enabled) {
            self::revokePageTokens($plugin);
        }
        AllowPluginOrigins::forget();
        Audit::record($request, match (true) {
            array_key_exists('baseUrl', $data) => 'Changed the address of plugin',
            $plugin->enabled => 'Turned on plugin',
            default => 'Turned off plugin',
        }, $plugin->name);

        return response()->json(self::present($plugin->fresh(['key', 'user'])) + (isset($newSecret) ? ['webhookSecret' => $newSecret] : []));
    }

    public function destroy(Request $request, string $id): JsonResponse
    {
        $plugin = Plugin::with(['user', 'server'])->findOrFail($id);
        if ($plugin->hosted() && $plugin->server) {
            // The agent removes the container, its Unix user and its route.
            $this->hosting->remove($plugin);
        }
        DB::transaction(function () use ($plugin) {
            self::revokePageTokens($plugin);
            $plugin->user?->tokens()->delete();
            $plugin->user?->delete();
            $plugin->delete();
        });
        AllowPluginOrigins::forget();
        Audit::record($request, 'Removed plugin', $plugin->name);

        return response()->json(null, 204);
    }

    /** A new token and a new webhook secret. The old ones stop at once. */
    public function rotate(Request $request, string $id): JsonResponse
    {
        $plugin = Plugin::with('user')->findOrFail($id);
        AuthController::confirmIdentity($request);
        $secret = 'whsec_'.Str::random(40);
        $token = DB::transaction(function () use ($plugin, $secret) {
            $plugin->update(['webhook_secret' => $secret]);
            $token = self::newToken($plugin, $plugin->user);
            if ($plugin->hosted()) {
                $plugin->update(['hosted_token' => $token]);
            }
            self::revokePageTokens($plugin);

            return $token;
        });
        // The container gets the new values: the agent starts a new container, then stops the old one.
        if ($plugin->hosted() && self::canStart($plugin)) {
            $this->hosting->start($plugin, $plugin->container_status !== Hosting::FAILED);
        }
        Audit::record($request, 'Made new credentials for plugin', $plugin->name);

        return response()->json([
            'plugin' => self::present($plugin->fresh(['key', 'user', 'server'])),
            'token' => $plugin->hosted() ? null : $token,
            'webhookSecret' => $plugin->hosted() ? null : $secret,
        ]);
    }

    /** Makes the container of a plugin that the panel runs again, for example after a failure. */
    public function restart(Request $request, string $id): JsonResponse
    {
        $plugin = Plugin::with('server')->findOrFail($id);
        if (! $plugin->hosted() || ! $plugin->server) {
            throw ValidationException::withMessages(['plugin' => 'The panel does not run this plugin.']);
        }
        if (! $plugin->enabled) {
            throw ValidationException::withMessages(['plugin' => 'Turn on the plugin first.']);
        }
        $this->hosting->start($plugin, $plugin->container_status !== Hosting::FAILED);
        Audit::record($request, 'Started plugin container again', $plugin->name);

        return response()->json(self::present($plugin->fresh(['key', 'user', 'server'])), 202);
    }

    public function test(Request $request, string $id): JsonResponse
    {
        $plugin = Plugin::findOrFail($id);
        if ($plugin->webhookUrl() === null) {
            throw ValidationException::withMessages(['plugin' => 'The manifest of this plugin has no webhook.']);
        }
        $delivery = Events::deliver($plugin, Events::TEST, ['message' => 'This is a test event from the panel.', 'by' => $request->user()->name]);

        return response()->json(self::presentDelivery($delivery), 202);
    }

    public function deliveries(Request $request, string $id): JsonResponse
    {
        $plugin = Plugin::findOrFail($id);

        return response()->json(['data' => $plugin->deliveries()->latest('created_at')->latest('id')->limit(50)->get()->map(fn (PluginDelivery $d) => self::presentDelivery($d))]);
    }

    public function retry(string $id, string $deliveryId): JsonResponse
    {
        $delivery = PluginDelivery::where('plugin_id', $id)->findOrFail($deliveryId);
        $copy = Events::deliver($delivery->plugin, $delivery->event, $delivery->payload['data'] ?? []);

        return response()->json(self::presentDelivery($copy), 202);
    }

    public function keys(): JsonResponse
    {
        return response()->json(['data' => PluginKey::orderBy('name')->get()->map(fn (PluginKey $k) => self::presentKey($k))]);
    }

    public function storeKey(Request $request): JsonResponse
    {
        $data = $request->validate([
            'name' => ['required', 'string', 'max:100'],
            'publicKey' => ['required', 'string', 'max:4096'],
        ]);
        AuthController::confirmIdentity($request);
        $fingerprint = Signatures::fingerprint($data['publicKey']);
        if (PluginKey::where('fingerprint', $fingerprint)->exists()) {
            throw ValidationException::withMessages(['publicKey' => 'The panel trusts this key already.']);
        }
        $key = PluginKey::create(['name' => $data['name'], 'public_key' => trim($data['publicKey'])."\n", 'fingerprint' => $fingerprint]);
        Audit::record($request, 'Trusted plugin key', "{$key->name} (".substr($fingerprint, 0, 16).')');

        return response()->json(self::presentKey($key), 201);
    }

    public function destroyKey(Request $request, string $id): JsonResponse
    {
        $key = PluginKey::findOrFail($id);
        $key->delete();
        Audit::record($request, 'Removed trusted plugin key', $key->name);

        return response()->json(null, 204);
    }

    public function settings(): JsonResponse
    {
        return response()->json(['developerMode' => self::developerMode()]);
    }

    public function updateSettings(Request $request): JsonResponse
    {
        $data = $request->validate(['developerMode' => ['required', 'boolean']]);
        if ($data['developerMode']) {
            // The developer mode accepts plugins with no signature, so it needs the identity.
            AuthController::confirmIdentity($request);
        }
        Setting::put('plugins.developer_mode', $data['developerMode']);
        Audit::record($request, $data['developerMode'] ? 'Turned on the developer mode for plugins' : 'Turned off the developer mode for plugins');

        return response()->json(['developerMode' => $data['developerMode']]);
    }

    /** The pages of the plugins that the user can open, for the menu of the web panel. */
    public function pages(Request $request): JsonResponse
    {
        // Adversarial review, finding X1-08: only a sign-in session can open a page.
        if (! AuthController::isSession($request) && ! $request->user()->tokenCan('plugins:read')) {
            return response()->json(['data' => []]);
        }
        $role = $request->user()->role->value;
        $pages = [];
        foreach (Plugin::where('enabled', true)->orderBy('name')->get() as $plugin) {
            foreach ($plugin->pages() as $page) {
                if (in_array($role, $page['roles'], true)) {
                    $pages[] = self::presentPage($plugin, $page);
                }
            }
        }

        return response()->json(['data' => $pages]);
    }

    /**
     * A token for a page of a plugin, for the user who opens it. The token has the scopes of the
     * plugin, and the user's own rights still apply: an owner sees only the websites of the
     * owner. Only a sign-in session gets a page token, so an API token cannot make tokens.
     */
    public function pageSession(Request $request, string $id, string $pageId): JsonResponse
    {
        abort_unless(AuthController::isSession($request), 403, 'Only a sign-in session can open the page of a plugin.');
        $plugin = Plugin::where('enabled', true)->findOrFail($id);
        $user = $request->user();
        $page = collect($plugin->pages())->firstWhere('id', $pageId);
        abort_unless($page && in_array($user->role->value, $page['roles'], true), 404);

        $name = self::PAGE_TOKEN_PREFIX.$plugin->id;
        $user->tokens()->where('name', $name)->delete();
        $token = $user->createToken($name, $plugin->scopes() ?: ['none'], now()->addMinutes(self::PAGE_TOKEN_MINUTES));

        return response()->json([
            'token' => $token->plainTextToken,
            'expiresAt' => $token->accessToken->expires_at?->toIso8601String(),
            'scopes' => $plugin->scopes(),
            'page' => self::presentPage($plugin, $page),
            'user' => ['id' => $user->id, 'name' => $user->name, 'role' => $user->role->value, 'customerId' => $user->customer_id],
        ], 201);
    }

    /** The actions of the plugins that the user can select, for the pages of the web panel. */
    public function actions(Request $request): JsonResponse
    {
        if (! AuthController::isSession($request) && ! $request->user()->tokenCan('plugins:read')) {
            return response()->json(['data' => []]);
        }
        $role = $request->user()->role->value;
        $actions = [];
        foreach (Plugin::where('enabled', true)->orderBy('name')->get() as $plugin) {
            foreach ($plugin->actions() as $action) {
                if (in_array($role, $action['roles'], true)) {
                    $actions[] = self::presentAction($plugin, $action);
                }
            }
        }

        return response()->json(['data' => $actions]);
    }

    /**
     * Runs an action of a plugin on a website, a server or a customer that the user can see. The
     * panel sends the plugin a signed request with a token of the user for 15 minutes, with the
     * scopes of the plugin. Only a sign-in session runs an action, so an API token cannot make
     * tokens.
     */
    public function runAction(Request $request, Caller $caller, string $id, string $actionId): JsonResponse
    {
        abort_unless(AuthController::isSession($request), 403, 'Only a sign-in session can run the action of a plugin.');
        $plugin = Plugin::where('enabled', true)->findOrFail($id);
        $user = $request->user();
        $action = collect($plugin->actions())->firstWhere('id', $actionId);
        abort_unless($action && in_array($user->role->value, $action['roles'], true), 404);
        $data = $request->validate(['targetId' => ['required', 'string', 'max:64']]);
        [$targetName, $customerId] = $this->actionTarget($request, $action['target'], $data['targetId']);

        $name = Actions::TOKEN_PREFIX.$plugin->id;
        $user->tokens()->where('name', $name)->delete();
        $token = $user->createToken($name, $plugin->scopes() ?: ['none'], now()->addMinutes(self::PAGE_TOKEN_MINUTES));
        $requestId = strtolower((string) Str::ulid());
        $result = $caller->post($plugin, $plugin->urlOf($action['path']), 'plugin.action', $requestId, [
            'id' => $requestId,
            'action' => ['id' => $action['id'], 'title' => $action['title']],
            'target' => ['type' => $action['target'], 'id' => $data['targetId'], 'name' => $targetName, 'customerId' => $customerId],
            'user' => ['id' => $user->id, 'name' => $user->name, 'role' => $user->role->value, 'customerId' => $user->customer_id],
            'token' => $token->plainTextToken,
            'expiresAt' => $token->accessToken->expires_at?->toIso8601String(),
            'scopes' => $plugin->scopes(),
            'apiBase' => rtrim((string) config('app.url'), '/').'/api/v1',
            'createdAt' => now()->toIso8601String(),
        ], Actions::TIMEOUT);
        Audit::record($request, 'Ran plugin action', "{$plugin->name}: {$action['title']} ({$targetName})", $customerId);
        if ($result['error'] !== null) {
            $token->accessToken->delete();

            // Adversarial review, finding A7-12: the error has the address of the plugin. Only
            // an administrator gets it.
            Log::warning("The action {$action['id']} of the plugin {$plugin->slug} failed: {$result['error']}");
            $detail = $user->isAdministrator() ? $result['error'] : 'Speak to your provider if this continues.';

            return response()->json(['message' => "The plugin {$plugin->name} did not do the action. {$detail}"], 502);
        }

        $answer = $result['response']?->json();
        $message = is_array($answer) && is_string($answer['message'] ?? null) && trim($answer['message']) !== ''
            ? mb_substr(trim($answer['message']), 0, Actions::MESSAGE_LENGTH)
            : "The plugin {$plugin->name} did the action.";
        $url = is_array($answer) && is_string($answer['url'] ?? null) ? $answer['url'] : null;
        // Only a link to the plugin itself, so that a plugin cannot show a link to a different site
        // as a link of the panel.
        $origin = self::presentPage($plugin, ['id' => '', 'title' => '', 'path' => '/', 'roles' => []])['origin'];
        if ($url !== null && ! str_starts_with($url, $origin.'/')) {
            $url = null;
        }

        return response()->json(['message' => $message, 'url' => $url]);
    }

    /**
     * The name and the customer of the target of an action, when the user can see the target.
     *
     * @return array{0: string, 1: ?string}
     */
    private function actionTarget(Request $request, string $type, string $id): array
    {
        $user = $request->user();
        if ($type === 'website') {
            $website = $this->visibleWebsites($request)->findOrFail($id);

            return [$website->name, $website->customer_id];
        }
        if ($type === 'server') {
            abort_unless($user->isAdministrator(), 404);

            return [Server::findOrFail($id)->name, null];
        }
        $customer = Customer::findOrFail($id);
        abort_unless($user->canSeeCustomer($customer->id), 404);

        return [$customer->name, $customer->id];
    }

    /** True when the token of the request is from a plugin, or from a page of a plugin, that is off. */
    public static function refusesToken(Request $request): bool
    {
        $user = $request->user();
        $token = $user?->currentAccessToken();
        $name = $token && method_exists($token, 'getAttribute') ? (string) $token->getAttribute('name') : '';
        $pluginId = $user?->plugin_id;
        foreach ([self::PAGE_TOKEN_PREFIX, Actions::TOKEN_PREFIX] as $prefix) {
            if ($pluginId === null && str_starts_with($name, $prefix)) {
                $pluginId = substr($name, strlen($prefix));
            }
        }

        return $pluginId !== null && ! Plugin::where('id', $pluginId)->where('enabled', true)->exists();
    }

    private static function developerMode(): bool
    {
        return (bool) Setting::get('plugins.developer_mode', false);
    }

    /** @throws ValidationException */
    private static function checkSignature(string $manifest, ?string $signature): ?PluginKey
    {
        $key = $signature ? Signatures::signer($manifest, $signature) : null;
        if ($key === null && ! self::developerMode()) {
            throw ValidationException::withMessages(['signature' => $signature
                ? 'No trusted key signed this manifest. Add the public key of the publisher in Plugins > Trusted keys, or check that the manifest has no change.'
                : 'Give the signature of the manifest. Only the developer mode accepts a plugin with no signature.']);
        }

        return $key;
    }

    private static function newToken(Plugin $plugin, User $user): string
    {
        $user->tokens()->where('name', self::TOKEN_NAME)->delete();

        return $user->createToken(self::TOKEN_NAME, $plugin->scopes() ?: ['none'], now()->addDays(self::TOKEN_DAYS))->plainTextToken;
    }

    /**
     * Adversarial review, finding A7-09: a plugin that is off does not start. The agent keeps
     * a stopped container stopped when it gets new values, and the container uses them at its
     * next start. Only a container that failed is made again, and that starts it. Thus a plugin
     * that is off and failed gets nothing: the administrator turns it on and starts it again.
     */
    private static function canStart(Plugin $plugin): bool
    {
        return $plugin->enabled || $plugin->container_status !== Hosting::FAILED;
    }

    private static function revokePageTokens(Plugin $plugin): void
    {
        DB::table('personal_access_tokens')->whereIn('name', [self::PAGE_TOKEN_PREFIX.$plugin->id, Actions::TOKEN_PREFIX.$plugin->id])->delete();
    }

    /** @return array<string, mixed> */
    public static function present(Plugin $p): array
    {
        $spec = $p->spec();
        $token = $p->user?->tokens()->where('name', self::TOKEN_NAME)->first();
        $counts = $p->deliveries()->where('created_at', '>=', now()->subDay())
            ->selectRaw('state, count(*) as n')->groupBy('state')->pluck('n', 'state');
        $last = $p->deliveries()->latest('created_at')->latest('id')->first();

        return [
            'id' => $p->id,
            'slug' => $p->slug,
            'name' => $p->name,
            'version' => $p->version,
            'publisher' => $p->publisher,
            'description' => $spec['description'] ?? '',
            'enabled' => $p->enabled,
            'baseUrl' => $p->base_url,
            'webhookUrl' => $p->webhookUrl(),
            'scopes' => $p->scopes(),
            'events' => $p->events(),
            'pages' => array_map(fn (array $page) => self::presentPage($p, $page), $p->pages()),
            'actions' => array_map(fn (array $a) => self::presentAction($p, $a), $p->actions()),
            'hooks' => $p->hooks(),
            'commands' => $spec['commands'] ?? [],
            'image' => $spec['image'] ?? null,
            'hosting' => $p->hosted() ? [
                'serverId' => $p->server_id,
                'serverName' => $p->server?->name,
                'host' => $p->host,
                'status' => $p->container_status,
            ] : null,
            'signedBy' => $p->key ? self::presentKey($p->key) : null,
            'tokenExpiresAt' => $token?->expires_at?->toIso8601String(),
            'tokenLastUsedAt' => $token?->last_used_at?->toIso8601String(),
            'deliveries' => [
                'delivered' => (int) ($counts[PluginDelivery::DELIVERED] ?? 0),
                'pending' => (int) ($counts[PluginDelivery::PENDING] ?? 0),
                'failed' => (int) ($counts[PluginDelivery::FAILED] ?? 0),
                'last' => $last ? self::presentDelivery($last) : null,
            ],
            'createdAt' => $p->created_at?->toIso8601String(),
            'updatedAt' => $p->updated_at?->toIso8601String(),
        ];
    }

    /** @param array<string, mixed> $page */
    private static function presentPage(Plugin $p, array $page): array
    {
        $url = $p->urlOf($page['path']);
        $parts = parse_url($url);

        return [
            'pluginId' => $p->id,
            'pluginName' => $p->name,
            'id' => $page['id'],
            'title' => $page['title'],
            'url' => $url,
            'origin' => $parts['scheme'].'://'.$parts['host'].(isset($parts['port']) ? ':'.$parts['port'] : ''),
            'roles' => $page['roles'],
        ];
    }

    /** @param array<string, mixed> $action */
    private static function presentAction(Plugin $p, array $action): array
    {
        return [
            'pluginId' => $p->id,
            'pluginName' => $p->name,
            'id' => $action['id'],
            'title' => $action['title'],
            'target' => $action['target'],
            'confirm' => $action['confirm'] ?: null,
            'roles' => $action['roles'],
        ];
    }

    /** @return array<string, mixed> */
    private static function presentKey(PluginKey $k): array
    {
        return [
            'id' => $k->id,
            'name' => $k->name,
            'fingerprint' => $k->fingerprint,
            'publicKey' => $k->public_key,
            'createdAt' => $k->created_at?->toIso8601String(),
        ];
    }

    /** @return array<string, mixed> */
    private static function presentDelivery(PluginDelivery $d): array
    {
        return [
            'id' => $d->id,
            'event' => $d->event,
            'state' => $d->state,
            'attempts' => $d->attempts,
            'responseStatus' => $d->response_status,
            'error' => $d->error,
            'payload' => $d->payload,
            'createdAt' => $d->created_at?->toIso8601String(),
            'deliveredAt' => $d->delivered_at?->toIso8601String(),
        ];
    }
}
