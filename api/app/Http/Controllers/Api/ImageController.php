<?php

namespace App\Http\Controllers\Api;

use App\Enums\UpdatePolicy;
use App\Http\Controllers\Controller;
use App\Models\Image;
use App\Services\Audit;
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
            'phpVersion' => $i->php_version,
            'supportEndsAt' => $i->support_ends_at?->toDateString(),
            'policy' => $i->policy->value,
            'custom' => $i->custom,
            'signatureVerified' => $i->signature_verified,
            'websites' => $i->websites_count ?? null,
        ];
    }
}
