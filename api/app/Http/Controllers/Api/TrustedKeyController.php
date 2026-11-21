<?php

namespace App\Http\Controllers\Api;

use App\Http\Controllers\Controller;
use App\Models\Pack;
use App\Models\PluginKey;
use App\Services\Audit;
use App\Services\Plugins\Signatures;
use Illuminate\Http\JsonResponse;
use Illuminate\Http\Request;
use Illuminate\Validation\ValidationException;

/**
 * The public keys of the publishers that an administrator trusts. The panel installs a plugin
 * or a pack only when one of these keys signed its manifest, unless the developer mode is on.
 * The panel also trusts the release key of Cloudfactory for packs.
 */
class TrustedKeyController extends Controller
{
    public function index(): JsonResponse
    {
        return response()->json(['data' => PluginKey::orderBy('name')->get()->map(fn (PluginKey $k) => self::present($k))]);
    }

    public function store(Request $request): JsonResponse
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
        Audit::record($request, 'Trusted publisher key', "{$key->name} (".substr($fingerprint, 0, 16).')');

        return response()->json(self::present($key), 201);
    }

    /**
     * Removes a trusted key. A plugin that the key signed stays. A pack that the key signed
     * stops the removal: each server checks the signature of the pack with the key.
     */
    public function destroy(Request $request, string $id): JsonResponse
    {
        $key = PluginKey::findOrFail($id);
        $packs = Pack::where('plugin_key_id', $key->id)->orderBy('name')->pluck('name');
        if ($packs->isNotEmpty()) {
            throw ValidationException::withMessages(['key' => "The key signed the pack {$packs->join(', ')}. Remove the pack first, or update it with a manifest from a different key."]);
        }
        $key->delete();
        Audit::record($request, 'Removed trusted publisher key', $key->name);

        return response()->json(null, 204);
    }

    /** @return array<string, mixed> */
    public static function present(PluginKey $k): array
    {
        return [
            'id' => $k->id,
            'name' => $k->name,
            'fingerprint' => $k->fingerprint,
            'publicKey' => $k->public_key,
            'createdAt' => $k->created_at?->toIso8601String(),
        ];
    }
}
