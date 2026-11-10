<?php

namespace App\Http\Controllers\Api;

use App\Enums\UpdatePolicy;
use App\Http\Controllers\Controller;
use App\Models\Image;
use App\Services\Audit;
use App\Services\ImageUpdateChecker;
use App\Services\Registry\CosignVerifier;
use App\Services\Registry\RegistryClient;
use App\Services\RolloutEngine;
use Illuminate\Http\JsonResponse;
use Illuminate\Http\Request;
use Illuminate\Validation\Rule;

/** FR-307 and FR-1106 to FR-1110: runtime images and their update policy. */
class ImageController extends Controller
{
    /**
     * An image reference with no tag. Audit finding R5: it must start with the host name of a
     * registry (a name with a dot or a port, or "localhost"), because the agent and the
     * signature policy do not accept a short name such as "nginx". The agent has the same
     * rule, and the two test it with testdata/names.json.
     */
    public const REFERENCE = '#^(?=[^/]*[.:]|localhost/)[a-z0-9]([a-z0-9.-]*[a-z0-9])?(:\d+)?(/[a-z0-9._-]+)+$#';

    public function index(): JsonResponse
    {
        return response()->json(['data' => Image::withCount('websites')->orderBy('name')->get()->map(fn (Image $i) => self::present($i))]);
    }

    /**
     * The images that the user can select for a new website. A customer or a reseller sees
     * the official images only. An administrator also sees the images that they added.
     */
    public function runtimes(Request $request): JsonResponse
    {
        $images = Image::where('family', '!=', 'edge')
            ->when(! $request->user()->isAdministrator(), fn ($q) => $q->where('custom', false))
            ->orderBy('name')->get();

        return response()->json(['data' => $images->map(fn (Image $i) => [
            'id' => $i->id, 'name' => $i->name, 'family' => $i->family, 'tag' => $i->tag,
            'phpVersion' => $i->php_version, 'custom' => $i->custom, 'supportEndsAt' => $i->support_ends_at?->toIso8601String(),
        ])]);
    }

    /** FR-307: an administrator adds an OCI image of their choice. */
    public function store(Request $request): JsonResponse
    {
        $data = $request->validate([
            'name' => ['required', 'string', 'max:100'],
            'reference' => ['required', 'max:200', 'regex:'.self::REFERENCE],
            'tag' => ['required', 'regex:/^[\w][\w.-]{0,127}$/'],
            // FR-1110: automatic or manual updates for each image.
            'policy' => ['required', Rule::in([UpdatePolicy::Automatic->value, UpdatePolicy::Manual->value])],
            ...self::runRules(),
        ]);
        $image = Image::create([...self::fields($data), 'family' => 'custom', 'custom' => true]);
        Audit::record($request, 'Added image', "{$data['reference']}:{$data['tag']}");

        return response()->json(self::present($image), 201);
    }

    public function update(Request $request, string $id): JsonResponse
    {
        $image = Image::findOrFail($id);
        $data = $request->validate(['policy' => ['sometimes', Rule::enum(UpdatePolicy::class)], ...($image->custom ? self::runRules() : [])]);
        $image->update(self::fields($data));
        Audit::record($request, 'Changed image', $image->name.(isset($data['policy']) ? ": {$data['policy']}" : ''));

        return response()->json(self::present($image));
    }

    /** FR-1110: check the registry for a new version of this image now. */
    public function check(Request $request, string $id, RolloutEngine $rollouts): JsonResponse
    {
        $image = Image::findOrFail($id);
        $checker = new ImageUpdateChecker(new RegistryClient, CosignVerifier::fromConfig(), $rollouts);
        $checker->check($image);
        Audit::record($request, 'Checked image for updates', $image->name);

        return response()->json(self::present($image->fresh()));
    }

    /** FR-307: the HTTP port and the start command of a custom image. The agent checks the same limits. */
    private static function runRules(): array
    {
        return [
            'containerPort' => ['sometimes', 'nullable', 'integer', 'between:1,65535'],
            'command' => ['sometimes', 'nullable', 'array', 'max:32'],
            'command.*' => ['string', 'min:1', 'max:1024', 'not_regex:/[\x00-\x1f\x7f]/'],
        ];
    }

    private static function fields(array $data): array
    {
        $out = array_diff_key($data, ['containerPort' => true, 'command' => true]);
        if (array_key_exists('containerPort', $data)) {
            $out['container_port'] = $data['containerPort'];
        }
        if (array_key_exists('command', $data)) {
            $out['command'] = $data['command'] ?: null;
        }

        return $out;
    }

    public static function present(Image $i): array
    {
        return [
            'id' => $i->id,
            'name' => $i->name,
            'family' => $i->family,
            'reference' => $i->reference,
            'tag' => $i->tag,
            'digest' => $i->digest,
            'availableTag' => $i->available_tag,
            'availableDigest' => $i->available_digest,
            'phpVersion' => $i->php_version,
            'supportEndsAt' => $i->support_ends_at?->toDateString(),
            'policy' => $i->policy->value,
            'custom' => $i->custom,
            'signatureVerified' => $i->signature_verified,
            'checkedAt' => $i->checked_at?->toIso8601String(),
            'checkError' => $i->check_error,
            'containerPort' => $i->custom ? ($i->container_port ?? 8080) : 8080,
            'command' => $i->command ?? [],
            'websites' => $i->websites_count ?? null,
        ];
    }
}
