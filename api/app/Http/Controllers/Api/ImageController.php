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
use App\Validation\Input;
use App\Validation\InputRules;
use Illuminate\Http\JsonResponse;
use Illuminate\Http\Request;
use Illuminate\Validation\Rule;
use Illuminate\Validation\ValidationException;

/** FR-307 and FR-1106 to FR-1110: runtime images and their update policy. */
class ImageController extends Controller
{
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
            'phpVersion' => $i->php_version, 'version' => $i->runtime_version, 'custom' => $i->custom, 'supportEndsAt' => $i->support_ends_at?->toIso8601String(),
            // The usual commands of an application image, for the form of a new website.
            'defaults' => $i->appDefaults(),
            'appRuntime' => (bool) $i->app_runtime,
        ])]);
    }

    /** FR-307: an administrator adds an OCI image of their choice. */
    public function store(Request $request): JsonResponse
    {
        $data = $request->validate([
            'name' => ['required', 'string', 'max:100'],
            // Audit finding R5: the reference starts with the host name of a registry, because
            // the agent and the signature policy do not accept a short name such as "nginx".
            'reference' => ['required', Input::rule('imageReference')],
            'tag' => ['required', Input::rule('imageTag')],
            // FR-1110: automatic or manual updates for each image.
            'policy' => ['required', Rule::in([UpdatePolicy::Automatic->value, UpdatePolicy::Manual->value])],
            // The name of a runtime from a manifest of the agent, for example "golang". The image
            // then runs the start command of each website, as a Node.js image does.
            'runtime' => ['sometimes', 'nullable', 'string', 'regex:/^[a-z][a-z0-9-]{1,30}$/', Rule::notIn(Image::BUILT_IN_FAMILIES)],
            // The usual start command of a website with the image. The user can change it.
            'defaultCommand' => ['required_with:runtime', Rule::prohibitedIf(! $request->filled('runtime')), 'string', Input::rule('commandLine')],
            'defaultInstall' => ['sometimes', 'nullable', Rule::prohibitedIf(! $request->filled('runtime')), 'string', Input::rule('commandLine')],
            ...self::runRules(),
        ]);
        $runtime = $data['runtime'] ?? null;
        if ($runtime !== null && (isset($data['containerPort']) || ! empty($data['command']))) {
            throw ValidationException::withMessages(['runtime' => 'An image of a runtime uses port 8080 and the start command of each website. Remove the port and the command.']);
        }
        $fields = self::fields(array_diff_key($data, array_flip(['runtime', 'defaultCommand', 'defaultInstall'])));
        $image = Image::create($runtime === null
            ? [...$fields, 'family' => 'custom', 'custom' => true]
            : [...$fields, 'family' => $runtime, 'custom' => false, 'app_runtime' => true,
                'default_command' => $data['defaultCommand'] ?? null, 'default_install' => $data['defaultInstall'] ?? null]);
        Audit::record($request, 'Added image', "{$data['reference']}:{$data['tag']}".($runtime ? " (runtime {$runtime})" : ''));

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
            'command' => ['sometimes', 'nullable', 'array', 'max:'.InputRules::MAX_COMMAND_ARGUMENTS],
            'command.*' => ['string', Input::rule('commandArgument')],
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
            'version' => $i->runtime_version,
            'supportEndsAt' => $i->support_ends_at?->toDateString(),
            'policy' => $i->policy->value,
            'custom' => $i->custom,
            'appRuntime' => (bool) $i->app_runtime,
            'defaults' => $i->appDefaults(),
            'signatureVerified' => $i->signature_verified,
            'checkedAt' => $i->checked_at?->toIso8601String(),
            'checkError' => $i->check_error,
            'containerPort' => $i->custom ? ($i->container_port ?? 8080) : 8080,
            'command' => $i->command ?? [],
            'websites' => $i->websites_count ?? null,
        ];
    }
}
