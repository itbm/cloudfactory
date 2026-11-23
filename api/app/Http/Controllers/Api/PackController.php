<?php

namespace App\Http\Controllers\Api;

use App\Http\Controllers\Controller;
use App\Models\Image;
use App\Models\Pack;
use App\Models\Setting;
use App\Services\Audit;
use App\Services\DatabaseEngines;
use App\Services\Packs\PackManifest;
use App\Services\Packs\Packs;
use App\Validation\InputRules;
use Illuminate\Http\JsonResponse;
use Illuminate\Http\Request;

/**
 * Packs (docs/packs.md): signed manifests from publishers that add a website runtime or a
 * database engine. Only an administrator installs, updates and removes a pack, with the
 * password and the TOTP code. The panel installs only a manifest that a trusted key signed,
 * unless the developer mode for packs is on.
 */
class PackController extends Controller
{
    public function __construct(private Packs $packs) {}

    public function index(): JsonResponse
    {
        return response()->json(['data' => Pack::with(['key', 'user', 'images' => fn ($q) => $q->withCount('websites')])->orderBy('name')->get()->map(fn (Pack $p) => self::present($p))]);
    }

    public function show(string $id): JsonResponse
    {
        return response()->json(self::present(Pack::with(['key', 'user', 'images' => fn ($q) => $q->withCount('websites')])->findOrFail($id)));
    }

    /**
     * Reads a manifest and gives what it adds, before the administrator installs it: the
     * runtime, its images and the key that signed it.
     */
    public function preview(Request $request): JsonResponse
    {
        $data = self::input($request);
        $spec = PackManifest::parse($data['manifest']);
        $signer = Packs::signer($data['manifest'], $data['signature'] ?? null);
        $installed = Pack::where('slug', $spec['slug'])->first();

        return response()->json([
            'manifest' => $spec,
            'signedBy' => $signer ? ['name' => $signer['name'], 'fingerprint' => $signer['key']?->fingerprint, 'release' => $signer['key'] === null] : null,
            'installed' => $installed ? ['id' => $installed->id, 'version' => $installed->version] : null,
            'developerMode' => Packs::developerMode(),
        ]);
    }

    public function store(Request $request): JsonResponse
    {
        $data = self::input($request);
        AuthController::confirmIdentity($request);
        $pack = $this->packs->install($data['manifest'], $data['signature'] ?? null, $request->user());
        Audit::record($request, 'Installed pack', "{$pack->name} {$pack->version}".($pack->signer ? " (signed by {$pack->signer})" : ' (no signature)'));

        return response()->json(self::present(self::load($pack)), 201);
    }

    /** A new version of a pack, with a new signed manifest. */
    public function update(Request $request, string $id): JsonResponse
    {
        $pack = Pack::findOrFail($id);
        $data = self::input($request);
        AuthController::confirmIdentity($request);
        $old = $pack->version;
        $this->packs->upgrade($pack, $data['manifest'], $data['signature'] ?? null);
        Audit::record($request, 'Updated pack', "{$pack->name} {$old} to {$pack->version}");

        return response()->json(self::present(self::load($pack->fresh())));
    }

    public function destroy(Request $request, string $id): JsonResponse
    {
        $pack = Pack::findOrFail($id);
        $this->packs->remove($pack);
        Audit::record($request, 'Removed pack', "{$pack->name} {$pack->version}");

        return response()->json(null, 204);
    }

    /** Sends all packs to each web server again, for example after a failed job. */
    public function sync(Request $request): JsonResponse
    {
        $this->packs->pushAll('Send the packs');
        Audit::record($request, 'Sent the packs to the servers again');

        return response()->json(['servers' => Packs::servers()->count()], 202);
    }

    public function settings(): JsonResponse
    {
        return response()->json(['developerMode' => Packs::developerMode()]);
    }

    public function updateSettings(Request $request): JsonResponse
    {
        $data = $request->validate(['developerMode' => ['required', 'boolean']]);
        if ($data['developerMode']) {
            // The developer mode accepts packs with no signature, so it needs the identity.
            AuthController::confirmIdentity($request);
        }
        Setting::put('packs.developer_mode', $data['developerMode']);
        Audit::record($request, $data['developerMode'] ? 'Turned on the developer mode for packs' : 'Turned off the developer mode for packs');

        return response()->json(['developerMode' => $data['developerMode']]);
    }

    /** @return array{manifest: string, signature?: string|null} */
    private static function input(Request $request): array
    {
        return $request->validate([
            'manifest' => ['required', 'string', 'max:'.InputRules::MAX_PACK_MANIFEST_BYTES],
            'signature' => ['nullable', 'string', 'max:512'],
        ]);
    }

    private static function load(Pack $pack): Pack
    {
        return $pack->load(['key', 'user', 'images' => fn ($q) => $q->withCount('websites')]);
    }

    /** @return array<string, mixed> */
    public static function present(Pack $p): array
    {
        $spec = $p->spec();
        // A database pack has no image records: its images come from the manifest, with the
        // number of database servers of each version.
        $servers = $p->isDatabase() ? $p->databaseServers()->selectRaw('engine_version, count(*) as n')->groupBy('engine_version')->pluck('n', 'engine_version') : collect();
        $images = $p->isDatabase()
            ? array_map(fn (array $img) => [
                'id' => null, 'name' => "{$p->name} {$img['version']}", 'version' => $img['version'], 'reference' => $img['repository'], 'tag' => $img['tag'],
                'digest' => $img['digest'], 'availableDigest' => null, 'supportEndsAt' => $img['supportEndsAt'], 'websites' => 0,
                'databaseServers' => (int) ($servers[$img['version']] ?? 0),
            ], $spec['images'])
            : $p->images->sort(fn (Image $a, Image $b) => version_compare((string) $b->runtime_version, (string) $a->runtime_version))->values()->map(fn (Image $i) => [
                'id' => $i->id, 'name' => $i->name, 'version' => $i->runtime_version, 'reference' => $i->reference, 'tag' => $i->tag,
                'digest' => $i->digest, 'availableDigest' => $i->available_digest, 'supportEndsAt' => $i->support_ends_at?->toDateString(),
                'websites' => $i->websites_count ?? 0, 'databaseServers' => 0,
            ])->all();

        return [
            'id' => $p->id,
            'slug' => $p->slug,
            'name' => $p->name,
            'version' => $p->version,
            'publisher' => $p->publisher,
            'description' => $p->description,
            'signed' => $p->signed(),
            'signedBy' => $p->signer ? ['name' => $p->signer, 'fingerprint' => $p->key?->fingerprint, 'release' => $p->signer === Pack::RELEASE_KEY && $p->plugin_key_id === null] : null,
            'kind' => $p->kind,
            'runtime' => $spec['runtime'],
            // The engine of a database pack, and the block of host ports of its database
            // servers. Null for a runtime pack.
            'database' => $p->isDatabase() ? [
                ...$spec['database'], 'firstPort' => $p->database_port, 'lastPort' => $p->database_port + DatabaseEngines::PORTS - 1,
                'databaseServers' => (int) $servers->sum(),
            ] : null,
            'images' => $images,
            'servers' => Packs::serverStates($p),
            'installedBy' => $p->user?->name,
            'installedAt' => $p->created_at?->toIso8601String(),
            'updatedAt' => $p->updated_at?->toIso8601String(),
        ];
    }
}
