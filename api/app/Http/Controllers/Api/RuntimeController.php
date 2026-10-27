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
            // Batch 1, finding 11: the directory in /app that the web server serves, for example
            // "web". "." is /app itself.
            'documentRoot' => ['sometimes', 'string', 'max:200', 'regex:#^(\.|[A-Za-z0-9_-][A-Za-z0-9._-]*(/[A-Za-z0-9_-][A-Za-z0-9._-]*)*)$#', 'not_regex:#(^|/)\.\.(/|$)#'],
        ]);
        $plan = $website->plan;
        $changes = [];
        // Batch 1, finding 3: on failure, the panel puts these values back.
        $revert = [
            'image_id' => $website->image_id, 'image_tag' => $website->image_tag, 'previous_image_id' => $website->previous_image_id,
            'previous_image_tag' => $website->previous_image_tag, 'worker_mode' => $website->worker_mode, 'extensions' => $website->extensions,
            'php_ini' => $website->php_ini, 'document_root' => $website->document_root,
        ];
        // Batch 3, finding 6: worker mode is checked against the image after the change.
        $newImage = $website->image;

        if (isset($data['imageId']) && $data['imageId'] !== $website->image_id) {
            $image = Image::findOrFail($data['imageId']);
            if ($image->isPhp() !== $website->image->isPhp()) {
                throw ValidationException::withMessages(['imageId' => 'Select an image of the same kind.']);
            }
            // R-02: as for a new website, only an administrator selects a custom image. The owner
            // can go back to the previous image, which only an administrator could select.
            if ($image->custom && ! $request->user()->isAdministrator() && $image->id !== $website->previous_image_id) {
                throw ValidationException::withMessages(['imageId' => 'Select an official image.']);
            }
            // FR-303: keep the previous image, so that the owner can go back in one action.
            $changes += ['previous_image_id' => $website->image_id, 'previous_image_tag' => $website->currentTag(), 'image_id' => $image->id, 'image_tag' => null];
            $newImage = $image;
        }
        if (isset($data['workerMode'])) {
            if ($data['workerMode'] && $newImage->family !== 'frankenphp') {
                throw ValidationException::withMessages(['workerMode' => 'Worker mode is only for FrankenPHP.']);
            }
            $changes['worker_mode'] = $data['workerMode'];
        } elseif ($newImage->family !== 'frankenphp' && $website->worker_mode) {
            // A different runtime has no worker mode.
            $changes['worker_mode'] = false;
        }
        if (isset($data['documentRoot']) && $data['documentRoot'] !== $website->document_root) {
            $changes['document_root'] = $data['documentRoot'];
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
                if ($error = self::checkIni($key, $value, $website->limits['memoryBytes'] ?? PHP_INT_MAX)) {
                    throw ValidationException::withMessages(["phpIni.{$key}" => $error]);
                }
            }
            $changes['php_ini'] = array_map(fn ($v) => (string) $v, $data['phpIni']);
        }
        if (! $changes) {
            return response()->json(['website' => new WebsiteResource($website), 'job' => null]);
        }

        $website->update($changes);
        $website->refresh()->load(['image', 'domains']);
        // The agent goes back to the image of before the change when the health check fails.
        $payload = JobDispatcher::websitePayload($website) + [
            'previousImageId' => $revert['image_id'],
            'previousTag' => $revert['image_tag'] ?? Image::find($revert['image_id'])?->tag,
            'revert' => $revert,
        ];
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

    private const SIZE = '/^[1-9][0-9]{0,5}[KMG]?$/';

    /**
     * CF-15: each php.ini key has its own format and range. The panel refuses "-1" (no limit),
     * because the website must stay in its limits.
     */
    private static function checkIni(string $key, mixed $value, int $memoryBytes): ?string
    {
        if (! is_string($value) && ! is_int($value)) {
            return 'Enter a value.';
        }
        $value = (string) $value;
        $int = fn (int $min, int $max) => preg_match('/^[0-9]{1,7}$/', $value) && (int) $value >= $min && (int) $value <= $max;

        return match ($key) {
            'memory_limit' => ! preg_match(self::SIZE, $value) ? 'Enter a size, for example 256M.'
                : (self::bytes($value) > $memoryBytes ? 'The value is more than the memory limit of the website.'
                : (self::bytes($value) < 16 * 1024 ** 2 ? 'The value must be 16M or more.' : null)),
            'upload_max_filesize', 'post_max_size' => preg_match(self::SIZE, $value) && self::bytes($value) <= 16 * 1024 ** 3 ? null : 'Enter a size from 1 to 16G, for example 64M.',
            'max_execution_time' => $int(1, 3600) ? null : 'Enter a number of seconds from 1 to 3600.',
            'max_input_vars' => $int(100, 100000) ? null : 'Enter a number from 100 to 100000.',
            'display_errors' => in_array($value, ['On', 'Off', '0', '1'], true) ? null : 'Enter On or Off.',
            'date.timezone' => in_array($value, timezone_identifiers_list(), true) ? null : 'Enter a time zone, for example Europe/London.',
            'opcache.memory_consumption' => $int(8, max(8, intdiv($memoryBytes, 1024 ** 2))) ? null : 'Enter a number of megabytes from 8 to the memory limit of the website.',
            default => 'You cannot set this value.',
        };
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
