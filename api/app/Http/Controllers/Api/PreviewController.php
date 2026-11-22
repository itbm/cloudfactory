<?php

namespace App\Http\Controllers\Api;

use App\Enums\AgentAction;
use App\Enums\WebsiteStatus;
use App\Http\Controllers\Controller;
use App\Http\Resources\JobResource;
use App\Models\AgentJob;
use App\Models\GitSource;
use App\Models\Preview;
use App\Models\Release;
use App\Models\Website;
use App\Services\Audit;
use App\Services\Deploys\PullRequest;
use App\Services\Previews;
use App\Validation\Input;
use Illuminate\Http\JsonResponse;
use Illuminate\Http\Request;
use Illuminate\Validation\ValidationException;

/**
 * The preview websites of the pull requests of a website that deploys from Git
 * (App\Services\Previews). The webhook of the website makes and removes the previews. A user
 * can also make or remove a preview here, for example from a CI pipeline.
 */
class PreviewController extends Controller
{
    use Concerns;

    public function __construct(private Previews $previews) {}

    public function index(Request $request, string $websiteId): JsonResponse
    {
        return response()->json(self::resource($request, $this->findWebsite($request, $websiteId)));
    }

    /** Turns the previews on or off, and changes their settings. */
    public function update(Request $request, string $websiteId): JsonResponse
    {
        $website = $this->changeable($request, $websiteId);
        $source = $this->source($website);
        $data = $request->validate([
            'enabled' => ['required', 'boolean'],
            'domain' => ['nullable', 'string', 'lowercase', 'max:200'],
            'limit' => ['sometimes', 'integer', 'min:1', 'max:'.Previews::MAX_LIMIT],
            'copyEnvironment' => ['sometimes', 'boolean'],
        ]);
        $enabled = filter_var($data['enabled'], FILTER_VALIDATE_BOOLEAN);
        $domain = array_key_exists('domain', $data) ? trim((string) $data['domain'], " \t.") : $source->preview_domain;
        if ($enabled) {
            if (! $website->isPublic()) {
                throw ValidationException::withMessages(['enabled' => 'A '.Website::KIND_NAMES[$website->kind].' has no public address, so it cannot have previews.']);
            }
            if (! $domain) {
                throw ValidationException::withMessages(['domain' => 'Enter the domain of the previews, for example '.Previews::suggestedDomain($website).'.']);
            }
            $this->previews->checkDomain($website, $domain);
        }
        $was = $source->previews;
        $source->update([
            'previews' => $enabled,
            'preview_domain' => $domain ?: null,
            'preview_limit' => $data['limit'] ?? $source->preview_limit,
            'preview_environment' => array_key_exists('copyEnvironment', $data) ? filter_var($data['copyEnvironment'], FILTER_VALIDATE_BOOLEAN) : $source->preview_environment,
        ]);
        if ($was && ! $enabled) {
            $this->previews->removeAll($website, $request);
        }
        Audit::record($request, match (true) {
            $enabled && ! $was => 'Turned on previews',
            $was && ! $enabled => 'Turned off previews',
            default => 'Changed preview settings',
        }, $website->name, $website->customer_id);

        return response()->json(self::resource($request, $website->refresh()));
    }

    /**
     * Makes the preview of a pull request, or deploys it again. This is for a Git host whose
     * pull request events the panel does not read, and for a CI pipeline.
     */
    public function store(Request $request, string $websiteId): JsonResponse
    {
        $website = $this->changeable($request, $websiteId);
        $this->refuseSuspended($website->customer);
        $source = $this->source($website);
        if (! $source->previews) {
            throw ValidationException::withMessages(['number' => 'Turn on the previews of this website first.']);
        }
        $data = $request->validate([
            'number' => ['required', 'integer', 'min:1', 'max:'.PullRequest::MAX_NUMBER],
            'branch' => ['required', 'string', Input::rule('gitBranch')],
            'title' => ['sometimes', 'nullable', 'string', 'max:200'],
            'url' => ['sometimes', 'nullable', 'string', 'max:500', 'url:http,https'],
        ]);
        [$preview, $status, $message] = $this->previews->open($source, (int) $data['number'], $data['branch'], PullRequest::title($data['title'] ?? ''), PullRequest::url($data['url'] ?? null), GitSource::MANUAL, $request);
        if ($status === Previews::FAILED) {
            throw ValidationException::withMessages(['number' => $message]);
        }

        return response()->json(['status' => $status, 'message' => $message, 'preview' => self::preview($request, $preview->refresh())], 202);
    }

    /** Removes the preview of a pull request. */
    public function destroy(Request $request, string $websiteId, string $number): JsonResponse
    {
        $website = $this->findWebsite($request, $websiteId);
        $preview = $website->previews()->with('previewWebsite')->where('number', (int) $number)->firstOrFail();
        $job = $this->previews->remove($preview, $request);

        return response()->json(['job' => $job ? new JobResource($job) : null], 202);
    }

    private function changeable(Request $request, string $websiteId): Website
    {
        $website = $this->findWebsite($request, $websiteId);
        WebsiteController::refuseFailed($website);
        abort_if($website->customer?->isSuspended() ?? false, 409, 'This customer is suspended. Lift the suspension before you change the previews.');
        Previews::refuseChange($website);

        return $website;
    }

    private function source(Website $website): GitSource
    {
        return $website->gitSource ?? throw ValidationException::withMessages(['enabled' => 'Connect a Git repository first.']);
    }

    /** @return array<string, mixed> */
    public static function resource(Request $request, Website $website): array
    {
        $source = $website->previewOf()->exists() ? null : $website->gitSource;

        return [
            // Null when the website has no Git source, or is a preview itself.
            'settings' => $source ? [
                'enabled' => $source->previews,
                'domain' => $source->preview_domain,
                'limit' => $source->preview_limit,
                'copyEnvironment' => $source->preview_environment,
            ] : null,
            'suggestedDomain' => $website->isPublic() ? Previews::suggestedDomain($website) : null,
            'maxLimit' => Previews::MAX_LIMIT,
            'previews' => $website->previews()->with('previewWebsite')->get()->map(fn (Preview $p) => self::preview($request, $p))->all(),
        ];
    }

    /** @return array<string, mixed> */
    public static function preview(Request $request, Preview $p): array
    {
        $site = $p->previewWebsite;
        // The last deploy. Two deploys can start in the same second.
        $release = $site?->releases()->reorder()->orderByDesc('created_at')->orderByDesc('id')->first();

        return [
            'number' => $p->number,
            'title' => $p->title,
            'branch' => $p->branch,
            // The page of the pull request on the Git host.
            'url' => $p->url,
            'websiteId' => $site?->id,
            'name' => $site?->name,
            'state' => self::state($p, $site, $release),
            // The last deploy of the preview.
            'release' => $release ? DeployController::release($request, $release) : null,
            // Why the panel could not make the preview, or could not start its deploy.
            'error' => $p->error,
            'createdAt' => $p->created_at?->toIso8601String(),
            'updatedAt' => $p->updated_at?->toIso8601String(),
        ];
    }

    /** creating, deploying, live, failed or removing. */
    private static function state(Preview $p, ?Website $site, ?Release $release): string
    {
        if (! $site || $site->status === WebsiteStatus::Failed) {
            return 'failed';
        }
        if (AgentJob::where(['website_id' => $site->id, 'action' => AgentAction::DeleteWebsite])->whereNull('finished_at')->exists()) {
            return 'removing';
        }

        return match (true) {
            $release === null => $p->error !== null ? 'failed' : 'creating',
            $release->state === Release::BUILDING || $p->deploy_again => 'deploying',
            $release->state === Release::FAILED || $p->error !== null => 'failed',
            default => 'live',
        };
    }
}
