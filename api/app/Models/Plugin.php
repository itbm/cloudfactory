<?php

namespace App\Models;

use Illuminate\Database\Eloquent\Attributes\Fillable;
use Illuminate\Database\Eloquent\Attributes\Hidden;
use Illuminate\Database\Eloquent\Concerns\HasUlids;
use Illuminate\Database\Eloquent\Model;
use Illuminate\Database\Eloquent\Relations\BelongsTo;
use Illuminate\Database\Eloquent\Relations\HasMany;

/**
 * A plugin: a program outside the panel that uses the public API with its own token. The
 * panel keeps the signed manifest, the address of the plugin and the secret of its webhooks.
 */
#[Fillable(['slug', 'name', 'version', 'publisher', 'manifest', 'signature', 'plugin_key_id', 'enabled', 'base_url', 'webhook_secret', 'user_id', 'server_id', 'unix_user', 'host', 'container_status', 'hosted_token'])]
#[Hidden(['webhook_secret', 'hosted_token'])]
class Plugin extends Model
{
    use HasUlids;

    protected function casts(): array
    {
        return [
            'enabled' => 'boolean',
            'webhook_secret' => 'encrypted',
            'hosted_token' => 'encrypted',
        ];
    }

    /** True when the panel runs the plugin in a container (App\Services\Plugins\Hosting). */
    public function hosted(): bool
    {
        return $this->unix_user !== null;
    }

    /** @return array{ref: string, digest: string, port: int}|null The image of the manifest. */
    public function image(): ?array
    {
        return $this->spec()['image'] ?? null;
    }

    public function server(): BelongsTo
    {
        return $this->belongsTo(Server::class);
    }

    /** @return array<string, mixed> */
    public function spec(): array
    {
        return json_decode($this->manifest, true) ?: [];
    }

    /** @return list<string> */
    public function scopes(): array
    {
        return $this->spec()['scopes'] ?? [];
    }

    /** @return list<string> */
    public function events(): array
    {
        return $this->spec()['events'] ?? [];
    }

    /** @return list<array{id: string, title: string, path: string, roles: list<string>}> */
    public function pages(): array
    {
        return $this->spec()['pages'] ?? [];
    }

    /** @return list<array{id: string, title: string, target: string, path: string, roles: list<string>, confirm: string}> */
    public function actions(): array
    {
        return $this->spec()['actions'] ?? [];
    }

    /** @return list<array{event: string, path: string, onError: string}> */
    public function hooks(): array
    {
        return $this->spec()['hooks'] ?? [];
    }

    /**
     * The external DNS providers that the plugin offers (contract cf-dns-provider/1).
     *
     * @return list<array{id: string, name: string, path: string, dnssec: bool, types: list<string>, credentials: list<array{id: string, label: string, secret: bool, optional: bool, hint: string}>}>
     */
    public function dnsProviders(): array
    {
        return $this->spec()['dnsProviders'] ?? [];
    }

    /** The full address of a path of the manifest. */
    public function urlOf(string $path): string
    {
        return rtrim($this->base_url, '/').'/'.ltrim($path, '/');
    }

    public function webhookUrl(): ?string
    {
        $path = $this->spec()['webhook'] ?? null;

        return $path ? $this->urlOf($path) : null;
    }

    public function key(): BelongsTo
    {
        return $this->belongsTo(PluginKey::class, 'plugin_key_id');
    }

    public function user(): BelongsTo
    {
        return $this->belongsTo(User::class);
    }

    public function deliveries(): HasMany
    {
        return $this->hasMany(PluginDelivery::class);
    }
}
