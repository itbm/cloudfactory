<?php

namespace App\Http\Controllers\Api;

use App\Enums\AgentAction;
use App\Enums\WebsiteStatus;
use App\Http\Controllers\Controller;
use App\Http\Resources\JobResource;
use App\Http\Resources\WebsiteResource;
use App\Models\AgentJob;
use App\Models\Image;
use App\Models\Website;
use App\Services\Audit;
use App\Services\JobDispatcher;
use App\Validation\Input;
use App\Validation\InputRules;
use Illuminate\Http\JsonResponse;
use Illuminate\Http\Request;
use Illuminate\Validation\ValidationException;

/**
 * FR-303 to FR-305: PHP version, extensions, php.ini values and worker mode. The version,
 * the start command and the install command of a Node.js or Python website. The environment
 * variables of each website.
 */
class RuntimeController extends Controller
{
    use Concerns;

    private const INI_KEYS = ['memory_limit', 'upload_max_filesize', 'post_max_size', 'max_execution_time', 'max_input_vars', 'display_errors', 'date.timezone', 'opcache.memory_consumption'];

    public function __construct(private JobDispatcher $jobs) {}

    public function update(Request $request, string $websiteId): JsonResponse
    {
        $website = $this->findWebsite($request, $websiteId);
        // Adversarial review, finding X1-04: a new runtime starts the container.
        $this->refuseSuspended($website->customer);
        $data = $request->validate([
            'imageId' => ['sometimes', 'exists:images,id'],
            'workerMode' => ['sometimes', 'boolean'],
            'extensions' => ['sometimes', 'array'],
            'extensions.*' => ['string', Input::rule('phpExtension')],
            'phpIni' => ['sometimes', 'array'],
            // Batch 1, finding 11: the directory in /app that the web server serves, for example
            // "web". "." is /app itself.
            'documentRoot' => ['sometimes', 'string', Input::rule('documentRoot')],
            // The start command of a worker, or of a Node.js or Python website.
            'command' => ['sometimes', 'string', Input::rule('commandLine')],
            // The install command of a Node.js or Python website. Empty: no install step.
            'installCommand' => ['sometimes', 'nullable', 'string', Input::rule('commandLine')],
        ]);
        if (isset($data['command']) && ! $website->hasStartCommand()) {
            throw ValidationException::withMessages(['command' => 'Only a worker, or a Node.js or Python website, has a start command.']);
        }
        if (array_key_exists('installCommand', $data) && ! $website->image->isApp()) {
            throw ValidationException::withMessages(['installCommand' => 'Only a Node.js or Python website has an install command.']);
        }
        $plan = $website->plan;
        $changes = [];
        // Batch 1, finding 3: on failure, the panel puts these values back.
        $revert = [
            'image_id' => $website->image_id, 'image_tag' => $website->image_tag, 'previous_image_id' => $website->previous_image_id,
            'previous_image_tag' => $website->previous_image_tag, 'worker_mode' => $website->worker_mode, 'extensions' => $website->extensions,
            'php_ini' => $website->php_ini, 'document_root' => $website->document_root, 'command' => $website->command,
            'install_command' => $website->install_command,
        ];
        // Batch 3, finding 6: worker mode is checked against the image after the change.
        $newImage = $website->image;

        if (isset($data['imageId']) && $data['imageId'] !== $website->image_id) {
            $image = Image::findOrFail($data['imageId']);
            if ($image->kind() !== $website->image->kind()) {
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
        if (isset($data['command']) && $data['command'] !== $website->command) {
            $changes['command'] = trim($data['command']);
        }
        if (array_key_exists('installCommand', $data)) {
            $install = trim((string) $data['installCommand']);
            if (($install === '' ? null : $install) !== $website->install_command) {
                $changes['install_command'] = $install === '' ? null : $install;
            }
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
            // A website that deploys from Git serves the directory of its current release.
            if ($website->gitSource()->exists() && str_starts_with((string) $website->document_root, 'releases/')) {
                throw ValidationException::withMessages(['documentRoot' => 'This website deploys from Git. Change the web directory in the deploy settings, and deploy again.']);
            }
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
                // The agent checks each key and each value with these two rules.
                $error = self::checkIni($key, $value, $website->limits['memoryBytes'] ?? PHP_INT_MAX)
                    ?? (Input::valid('iniKey', $key) && Input::valid('iniValue', (string) $value) ? null : InputRules::MESSAGES['iniValue']);
                if ($error) {
                    throw ValidationException::withMessages(["phpIni.{$key}" => $error]);
                }
            }
            $changes['php_ini'] = array_map(fn ($v) => (string) $v, $data['phpIni']);
        }
        if (! $changes) {
            return response()->json(['website' => new WebsiteResource($website), 'job' => null]);
        }

        if ($website->status === WebsiteStatus::Failed) {
            // An image that never operated is not an image to go back to.
            unset($changes['previous_image_id'], $changes['previous_image_tag']);
        }
        $website->update($changes);
        $website->refresh()->load(['image', 'domains']);
        if ($website->status === WebsiteStatus::Failed) {
            // The panel could not make this website, so the server has no container to change.
            // The panel makes the website with the new settings: thus a user can select a
            // different image when the first image was the cause.
            $job = WebsiteController::createAgain($this->jobs, $website);
            Audit::record($request, 'Created website again', $website->name, $website->customer_id);

            return response()->json(['website' => new WebsiteResource($website->refresh()->load(['image', 'domains'])), 'job' => new JobResource($job)], 202);
        }
        // The agent goes back to the image of before the change when the health check fails.
        $payload = JobDispatcher::websitePayload($website) + [
            'previousImageId' => $revert['image_id'],
            'previousTag' => $revert['image_tag'] ?? Image::find($revert['image_id'])?->tag,
            'revert' => $revert,
        ];
        // A new Node.js or Python version needs the dependencies again: a native module of
        // Node.js and a virtual environment of Python are for one version.
        if (isset($changes['image_id']) && $website->image->isApp() && $website->install_command) {
            $job = self::install($this->jobs, $website, $payload, "Change runtime to {$website->image->name}");
        } else {
            $job = $this->jobs->dispatch($website->server, AgentAction::SetRuntime, $payload, match (true) {
                isset($changes['image_id']) => "Change runtime to {$website->image->name}",
                isset($changes['command']) => 'Change start command',
                $website->image->isApp() => 'Apply runtime settings',
                default => 'Apply PHP settings',
            }, $website);
        }
        Audit::record($request, 'Changed runtime', $website->name, $website->customer_id);

        return response()->json(['website' => new WebsiteResource($website), 'job' => new JobResource($job)], 202);
    }

    /** FR-303: go back to the previous image in one action. */
    public function revert(Request $request, string $websiteId): JsonResponse
    {
        $website = $this->findWebsite($request, $websiteId);
        $this->refuseSuspended($website->customer);
        if (! $website->previous_image_id) {
            throw ValidationException::withMessages(['imageId' => 'There is no previous image.']);
        }
        $request->merge(['imageId' => $website->previous_image_id]);

        return $this->update($request, $websiteId);
    }

    /**
     * Installs the dependencies of a Node.js or Python website, and starts the website again
     * with no stop: the job InstallDependencies.
     */
    public function installDependencies(Request $request, string $websiteId): JsonResponse
    {
        $website = $this->changeable($request, $websiteId);
        if (! $website->image->isApp()) {
            throw ValidationException::withMessages(['installCommand' => 'Only a Node.js or Python website has an install command.']);
        }
        if (! $website->install_command) {
            throw ValidationException::withMessages(['installCommand' => 'Enter an install command first.']);
        }
        $job = self::install($this->jobs, $website, JobDispatcher::websitePayload($website), 'Install dependencies');
        Audit::record($request, 'Installed dependencies', $website->name, $website->customer_id);

        return response()->json(['job' => new JobResource($job)], 202);
    }

    /**
     * Sets and removes environment variables of the website container, and starts the website
     * again with no stop. A value of null removes the variable. The API never gives the values.
     */
    public function environment(Request $request, string $websiteId): JsonResponse
    {
        $website = $this->changeable($request, $websiteId);
        $data = $request->validate([
            'variables' => ['present', 'array', 'max:'.InputRules::MAX_ENVIRONMENT_VARIABLES],
            'variables.*' => ['nullable', 'string', Input::rule('envValue')],
        ]);
        $env = $website->variables();
        foreach ($data['variables'] as $name => $value) {
            if (! Input::valid('envName', (string) $name)) {
                throw ValidationException::withMessages(["variables.{$name}" => InputRules::MESSAGES['envName']]);
            }
            if ($value === null) {
                unset($env[$name]);
            } else {
                $env[$name] = $value;
            }
        }
        if (count($env) > InputRules::MAX_ENVIRONMENT_VARIABLES) {
            throw ValidationException::withMessages(['variables' => 'A website can have '.InputRules::MAX_ENVIRONMENT_VARIABLES.' environment variables. Remove one first.']);
        }
        ksort($env);
        if ($env === $website->variables()) {
            return response()->json(['website' => new WebsiteResource($website), 'job' => null]);
        }
        $website->update(['environment' => $env ?: null]);
        Audit::record($request, 'Changed environment variables', $website->name.': '.implode(', ', array_keys($data['variables'])), $website->customer_id);
        $website->refresh()->load(['image', 'domains']);
        // The new container gets the new values. If its health check fails, the old container
        // continues with the old values, and the panel keeps the new values for the next start.
        $job = $this->jobs->dispatch($website->server, AgentAction::SetRuntime, JobDispatcher::websitePayload($website), 'Apply environment variables', $website);

        return response()->json(['website' => new WebsiteResource($website), 'job' => new JobResource($job)], 202);
    }

    /** @param  array<string, mixed>  $payload */
    private static function install(JobDispatcher $jobs, Website $website, array $payload, string $title): AgentJob
    {
        return $jobs->dispatch($website->server, AgentAction::InstallDependencies, $payload + ['command' => $website->install_command], $title, $website);
    }

    /** A website that can change: the panel made it, and its customer is not suspended. */
    private function changeable(Request $request, string $websiteId): Website
    {
        $website = $this->findWebsite($request, $websiteId);
        WebsiteController::refuseFailed($website);
        abort_if($website->customer?->isSuspended() ?? false, 409, 'This customer is suspended. Lift the suspension first.');

        return $website;
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
