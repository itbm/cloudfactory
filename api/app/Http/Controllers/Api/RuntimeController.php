<?php

namespace App\Http\Controllers\Api;

use App\Enums\AgentAction;
use App\Http\Controllers\Controller;
use App\Http\Resources\JobResource;
use App\Http\Resources\WebsiteResource;
use App\Models\Image;
use App\Services\Audit;
use App\Services\JobDispatcher;
use Illuminate\Http\JsonResponse;
use Illuminate\Http\Request;
use Illuminate\Validation\ValidationException;

/** FR-303 to FR-305: PHP version, extensions, php.ini values and worker mode. */
class RuntimeController extends Controller
{
    use Concerns;

    private const INI_KEYS = ['memory_limit', 'upload_max_filesize', 'post_max_size', 'max_execution_time', 'max_input_vars', 'display_errors', 'date.timezone', 'opcache.memory_consumption'];

    public function __construct(private JobDispatcher $jobs) {}

    public function update(Request $request, string $websiteId): JsonResponse
    {
        $website = $this->findWebsite($request, $websiteId);
        $data = $request->validate([
            'imageId' => ['sometimes', 'exists:images,id'],
            'workerMode' => ['sometimes', 'boolean'],
            'extensions' => ['sometimes', 'array'],
            'extensions.*' => ['string'],
            'phpIni' => ['sometimes', 'array'],
        ]);
        $plan = $website->plan;
        $changes = [];

        if (isset($data['imageId']) && $data['imageId'] !== $website->image_id) {
            $image = Image::findOrFail($data['imageId']);
            if ($image->isPhp() !== $website->image->isPhp()) {
                throw ValidationException::withMessages(['imageId' => 'Select an image of the same kind.']);
            }
            // FR-303: keep the previous image, so that the owner can go back in one action.
            $changes += ['previous_image_id' => $website->image_id, 'previous_image_tag' => $website->currentTag(), 'image_id' => $image->id, 'image_tag' => null];
        }
        if (isset($data['workerMode'])) {
            if ($website->image->family !== 'frankenphp') {
                throw ValidationException::withMessages(['workerMode' => 'Worker mode is only for FrankenPHP.']);
            }
            $changes['worker_mode'] = $data['workerMode'];
        }
        if (isset($data['extensions'])) {
            // FR-304: only the extensions of the plan.
            $extra = array_diff($data['extensions'], $plan->php_extensions);
            if ($extra) {
                throw ValidationException::withMessages(['extensions' => "The {$plan->name} plan does not include: ".implode(', ', $extra).'.']);
            }
            $changes['extensions'] = array_values($data['extensions']);
        }
        if (isset($data['phpIni'])) {
            foreach ($data['phpIni'] as $key => $value) {
                if (! in_array($key, self::INI_KEYS, true)) {
                    throw ValidationException::withMessages(["phpIni.{$key}" => "You cannot set {$key}."]);
                }
                if ($key === 'memory_limit' && self::bytes((string) $value) > ($website->limits['memoryBytes'] ?? PHP_INT_MAX)) {
                    throw ValidationException::withMessages(['phpIni.memory_limit' => 'The value is more than the memory limit of the website.']);
                }
            }
            $changes['php_ini'] = $data['phpIni'];
        }
        if (! $changes) {
            return response()->json(['website' => new WebsiteResource($website), 'job' => null]);
        }

        $website->update($changes);
        $website->refresh()->load(['image', 'domains']);
        $payload = JobDispatcher::websitePayload($website) + ['previousImageId' => $website->previous_image_id, 'previousTag' => $website->previous_image_tag];
        $job = $this->jobs->dispatch($website->server, AgentAction::SetRuntime, $payload, isset($changes['image_id']) ? "Change runtime to {$website->image->name}" : 'Apply PHP settings', $website);
        Audit::record($request, 'Changed runtime', $website->name, $website->customer_id);

        return response()->json(['website' => new WebsiteResource($website), 'job' => new JobResource($job)], 202);
    }

    /** FR-303: go back to the previous image in one action. */
    public function revert(Request $request, string $websiteId): JsonResponse
    {
        $website = $this->findWebsite($request, $websiteId);
        if (! $website->previous_image_id) {
            throw ValidationException::withMessages(['imageId' => 'There is no previous image.']);
        }
        $request->merge(['imageId' => $website->previous_image_id]);

        return $this->update($request, $websiteId);
    }

    private static function bytes(string $value): int
    {
        $n = (int) $value;

        return match (strtoupper(substr($value, -1))) {
            'G' => $n * 1024 ** 3,
            'M' => $n * 1024 ** 2,
            'K' => $n * 1024,
            default => $n,
        };
    }
}
