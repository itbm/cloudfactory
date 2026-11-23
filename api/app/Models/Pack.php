<?php

namespace App\Models;

use App\Services\Packs\PackManifest;
use Illuminate\Database\Eloquent\Attributes\Fillable;
use Illuminate\Database\Eloquent\Concerns\HasUlids;
use Illuminate\Database\Eloquent\Model;
use Illuminate\Database\Eloquent\Relations\BelongsTo;
use Illuminate\Database\Eloquent\Relations\HasMany;

/**
 * A pack (docs/packs.md): a signed manifest from a publisher that adds a website runtime or a
 * database engine to Cloudfactory. The panel keeps the exact bytes of the manifest, because
 * each agent checks the signature again. Each version of the runtime of a runtime pack is an
 * image of the pack. A database pack has no images for websites: each database server of its
 * engine has one version of the pack.
 */
#[Fillable(['slug', 'name', 'version', 'publisher', 'description', 'kind', 'database_port', 'manifest', 'signature', 'public_key', 'plugin_key_id', 'signer', 'user_id'])]
class Pack extends Model
{
    use HasUlids;

    /** The name of the signer when the release key of Cloudfactory signed the manifest. */
    public const RELEASE_KEY = 'Cloudfactory release key';

    protected function casts(): array
    {
        return ['database_port' => 'integer'];
    }

    /** True when the pack adds a database engine. */
    public function isDatabase(): bool
    {
        return $this->kind === PackManifest::KIND_DATABASE;
    }

    /** The database servers of the engine of a database pack. */
    public function databaseServers(): HasMany
    {
        return $this->hasMany(DatabaseServer::class, 'engine', 'slug');
    }

    public function images(): HasMany
    {
        return $this->hasMany(Image::class);
    }

    public function key(): BelongsTo
    {
        return $this->belongsTo(PluginKey::class, 'plugin_key_id');
    }

    public function user(): BelongsTo
    {
        return $this->belongsTo(User::class);
    }

    /** True when a key signed the manifest. A pack from the developer mode has no signature. */
    public function signed(): bool
    {
        return $this->public_key !== null;
    }

    /**
     * The checked manifest.
     *
     * @return array<string, mixed>
     */
    public function spec(): array
    {
        return PackManifest::parse($this->manifest);
    }

    /**
     * The pack as the agent gets it in a SetPacks job: the exact bytes of the manifest, the
     * signature and the public key of the publisher.
     *
     * @return array{manifest: string, signature: string, publicKey: string}
     */
    public function forAgent(): array
    {
        return ['manifest' => $this->manifest, 'signature' => (string) $this->signature, 'publicKey' => (string) $this->public_key];
    }
}
