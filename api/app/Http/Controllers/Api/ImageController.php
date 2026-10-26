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
    public function index(): JsonResponse
    {
        return response()->json(['data' => Image::withCount('websites')->orderBy('name')->get()->map(fn (Image $i) => self::present($i))]);
    }

    /** FR-307: an administrator adds an OCI image of their choice. */
    public function store(Request $request): JsonResponse
    {
        $data = $request->validate([
            'name' => ['required', 'string', 'max:100'],
            'reference' => ['required', 'regex:#^([a-z0-9.-]+(:\d+)?/)?[a-z0-9._/-]+$#'],
            'tag' => ['required', 'regex:/^[\w][\w.-]{0,127}$/'],
            // FR-1110: automatic or manual updates for each image.
            'policy' => ['required', Rule::in([UpdatePolicy::Automatic->value, UpdatePolicy::Manual->value])],
        ]);
        $image = Image::create([...$data, 'family' => 'custom', 'custom' => true]);
        Audit::record($request, 'Added image', "{$data['reference']}:{$data['tag']}");

        return response()->json(self::present($image), 201);
    }

    public function update(Request $request, string $id): JsonResponse
    {
        $image = Image::findOrFail($id);
        $data = $request->validate(['policy' => ['required', Rule::enum(UpdatePolicy::class)]]);
        $image->update($data);
        Audit::record($request, 'Changed update policy', "{$image->name}: {$data['policy']}");

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
            'websites' => $i->websites_count ?? null,
        ];
    }
}
