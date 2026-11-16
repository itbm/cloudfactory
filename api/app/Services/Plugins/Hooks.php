<?php

namespace App\Services\Plugins;

use App\Models\Plugin;
use App\Models\User;
use Illuminate\Support\Facades\Log;
use Illuminate\Support\Str;
use Illuminate\Validation\ValidationException;

/**
 * Before hooks. Before some changes, the panel asks each plugin that is on and has the hook. The
 * plugin answers {"allow": true}, or {"allow": false, "message": "..."} to refuse the change.
 * The panel waits for each plugin for 5 seconds or less. When a plugin does not answer in that
 * time, or answers with an error, the "onError" value of the hook in the manifest applies:
 * "allow" (the default) or "refuse".
 *
 * A before hook cannot change the request. It can only allow it or refuse it. The panel does not
 * ask a plugin about a change that the plugin makes itself.
 */
class Hooks
{
    /** The time in seconds that the panel waits for each plugin. */
    public const TIMEOUT = 5;

    /** The longest message of a refusal that the panel shows. */
    public const MESSAGE_LENGTH = 300;

    /** The hooks that a manifest can give, and the field of the error when a plugin refuses. */
    public const FIELDS = [
        'website.creating' => 'name',
        'website.removing' => 'website',
        'domain.adding' => 'name',
        'database.creating' => 'name',
        'customer.creating' => 'name',
    ];

    public const NAMES = ['website.creating', 'website.removing', 'domain.adding', 'database.creating', 'customer.creating'];

    public function __construct(private Caller $caller) {}

    /**
     * Asks each plugin with the hook. Refuses the change with status 422 when a plugin refuses.
     *
     * @param  array<string, mixed>  $data
     *
     * @throws ValidationException
     */
    public function check(string $hook, array $data, ?User $user): void
    {
        foreach (Plugin::where('enabled', true)->orderBy('name')->get() as $plugin) {
            if ($user?->plugin_id === $plugin->id) {
                continue;
            }
            $spec = collect($plugin->hooks())->firstWhere('event', $hook);
            if (! $spec || ! Manifest::permits($plugin->scopes(), $hook)) {
                continue;
            }
            [$allow, $message] = $this->ask($plugin, $spec, $hook, $data, $user);
            if (! $allow) {
                throw ValidationException::withMessages([self::FIELDS[$hook] => "The plugin {$plugin->name} refused this change: {$message}"]);
            }
        }
    }

    /**
     * @param  array{event: string, path: string, onError: string}  $spec
     * @param  array<string, mixed>  $data
     * @return array{0: bool, 1: string}
     */
    private function ask(Plugin $plugin, array $spec, string $hook, array $data, ?User $user): array
    {
        $id = strtolower((string) Str::ulid());
        $result = $this->caller->post($plugin, $plugin->urlOf($spec['path']), $hook, $id, [
            'id' => $id,
            'hook' => $hook,
            'createdAt' => now()->toIso8601String(),
            'data' => $data,
            'user' => $user ? ['id' => $user->id, 'name' => $user->name, 'role' => $user->role->value, 'customerId' => $user->customer_id] : null,
        ], self::TIMEOUT);
        $answer = $result['error'] === null ? $result['response']?->json() : null;
        if (is_array($answer) && is_bool($answer['allow'] ?? null)) {
            $message = is_string($answer['message'] ?? null) && trim($answer['message']) !== ''
                ? mb_substr(trim($answer['message']), 0, self::MESSAGE_LENGTH)
                : 'The plugin gave no reason.';

            return [$answer['allow'], $message];
        }
        $reason = $result['error'] ?? 'The plugin did not answer with {"allow": true} or {"allow": false}.';
        Log::warning("The before hook {$hook} of the plugin {$plugin->slug} failed: {$reason}");

        return [$spec['onError'] !== 'refuse', 'The plugin did not answer, and it refuses changes when it does not answer.'];
    }
}
