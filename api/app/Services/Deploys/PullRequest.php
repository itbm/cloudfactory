<?php

namespace App\Services\Deploys;

use App\Validation\Input;
use Illuminate\Http\Request;
use Illuminate\Support\Str;
use Illuminate\Validation\ValidationException;

/**
 * A pull request event that a Git host sends to the webhook of a website: GitHub, GitLab (a
 * merge request), Gitea, Forgejo or Bitbucket Cloud. The panel reads only the fields that a
 * preview needs (App\Services\Previews).
 *
 * The values are text of the Git host, and anyone with the webhook address can send them. The
 * panel checks each value. A preview always gets the code from the repository of the website,
 * so a branch name can select only a branch of that repository.
 */
final readonly class PullRequest
{
    /** A new pull request, or a pull request that opened again: the panel makes the preview. */
    public const OPENED = 'opened';

    /** The pull request has new commits: the panel deploys the preview again. */
    public const UPDATED = 'updated';

    /** The pull request has a new title: the panel changes the title of the preview. */
    public const EDITED = 'edited';

    /** The pull request closed, merged or not: the panel removes the preview. */
    public const CLOSED = 'closed';

    /** An action that does not change a preview, for example a review or a label. */
    public const OTHER = 'other';

    /** The largest number of a pull request. */
    public const MAX_NUMBER = 999999;

    public function __construct(
        /** github, gitlab, gitea or bitbucket. Gitea and Forgejo use the same format. */
        public string $provider,
        /** One of the constants above. */
        public string $action,
        public int $number,
        /** The head branch: the branch with the changes. */
        public string $branch,
        /** The base branch: the branch that the pull request changes. */
        public string $base,
        /** True when the head branch is in a different repository, for example a fork. */
        public bool $fork,
        public string $title,
        /** The page of the pull request on the Git host, or null. */
        public ?string $url,
    ) {}

    /**
     * The Git host of a pull request event: github, gitea, gitlab or bitbucket. Null when the
     * request is a different event, for example a push. This reads only the headers, and the
     * kind of a GitLab request with no header.
     */
    public static function provider(Request $request): ?string
    {
        $github = strtolower((string) $request->header('X-GitHub-Event'));
        $gitea = strtolower((string) ($request->header('X-Gitea-Event') ?? $request->header('X-Forgejo-Event') ?? $request->header('X-Gogs-Event')));
        $gitlab = (string) $request->header('X-Gitlab-Event');
        if ($gitea !== '' || $github !== '') {
            // Gitea and Forgejo also send the header of GitHub.
            return in_array($gitea !== '' ? $gitea : $github, ['pull_request', 'pull_request_sync'], true) ? ($gitea !== '' ? 'gitea' : 'github') : null;
        }
        if ($gitlab !== '' || $request->input('object_kind') === 'merge_request') {
            return $gitlab === '' || $gitlab === 'Merge Request Hook' ? 'gitlab' : null;
        }

        return str_starts_with((string) $request->header('X-Event-Key'), 'pullrequest:') ? 'bitbucket' : null;
    }

    /**
     * The pull request event of a webhook request, or null when the request is a different
     * event, for example a push.
     *
     * @throws ValidationException when the request is a pull request event with a value that is not correct.
     */
    public static function from(Request $request): ?self
    {
        return match (self::provider($request)) {
            'github', 'gitea' => self::github($request, (string) self::provider($request)),
            'gitlab' => self::gitlab($request),
            'bitbucket' => self::bitbucket($request, substr((string) $request->header('X-Event-Key'), strlen('pullrequest:'))),
            default => null,
        };
    }

    /** GitHub, Gitea and Forgejo. */
    private static function github(Request $request, string $provider): self
    {
        $pr = (array) $request->input('pull_request', []);
        $action = match ((string) $request->input('action')) {
            'opened', 'reopened', 'ready_for_review' => self::OPENED,
            // GitHub: "synchronize". Gitea and Forgejo: "synchronized".
            'synchronize', 'synchronized' => self::UPDATED,
            'edited' => self::EDITED,
            'closed' => self::CLOSED,
            default => self::OTHER,
        };
        // The head repository is null when the fork of the pull request was deleted.
        $head = data_get($pr, 'head.repo.full_name');
        $fork = ! is_string($head) || strcasecmp($head, (string) data_get($pr, 'base.repo.full_name')) !== 0;

        return self::make($provider, $action, $request->input('number', $pr['number'] ?? null), data_get($pr, 'head.ref'), data_get($pr, 'base.ref'), $fork, $pr['title'] ?? '', $pr['html_url'] ?? null);
    }

    private static function gitlab(Request $request): self
    {
        $mr = (array) $request->input('object_attributes', []);
        $action = match ((string) ($mr['action'] ?? '')) {
            'open', 'reopen' => self::OPENED,
            // "oldrev" is in an update with new commits. Without it, an update changes only
            // the title, the description, the labels or the people.
            'update' => isset($mr['oldrev']) ? self::UPDATED : self::EDITED,
            'close', 'merge' => self::CLOSED,
            default => self::OTHER,
        };
        $fork = (string) ($mr['source_project_id'] ?? '') !== (string) ($mr['target_project_id'] ?? '');

        return self::make('gitlab', $action, $mr['iid'] ?? null, $mr['source_branch'] ?? null, $mr['target_branch'] ?? null, $fork, $mr['title'] ?? '', $mr['url'] ?? null);
    }

    private static function bitbucket(Request $request, string $event): self
    {
        $pr = (array) $request->input('pullrequest', []);
        $action = match ($event) {
            'created' => self::OPENED,
            'updated' => self::UPDATED,
            'fulfilled', 'rejected' => self::CLOSED,
            default => self::OTHER,
        };
        $head = data_get($pr, 'source.repository.full_name');
        $fork = ! is_string($head) || strcasecmp($head, (string) data_get($pr, 'destination.repository.full_name')) !== 0;

        return self::make('bitbucket', $action, $pr['id'] ?? null, data_get($pr, 'source.branch.name'), data_get($pr, 'destination.branch.name'), $fork, $pr['title'] ?? '', data_get($pr, 'links.html.href'));
    }

    /** Checks the values of an event. */
    private static function make(string $provider, string $action, mixed $number, mixed $branch, mixed $base, bool $fork, mixed $title, mixed $url): self
    {
        if (! is_int($number) && ! (is_string($number) && ctype_digit($number))) {
            throw ValidationException::withMessages(['number' => 'The pull request has no number.']);
        }
        $number = (int) $number;
        if ($number < 1 || $number > self::MAX_NUMBER) {
            throw ValidationException::withMessages(['number' => 'The number of the pull request is not correct.']);
        }
        if (! is_string($branch) || ! Input::valid('gitBranch', $branch)) {
            throw ValidationException::withMessages(['branch' => 'The head branch of the pull request is not a valid branch name.']);
        }
        $base = is_string($base) && Input::valid('gitBranch', $base) ? $base : '';

        return new self($provider, $action, $number, $branch, $base, $fork, self::title($title), self::url($url));
    }

    /** One line of printable text, at most 200 characters. */
    public static function title(mixed $title): string
    {
        $text = is_string($title) ? (string) preg_replace('/[\x00-\x1f\x7f]+/u', ' ', mb_convert_encoding($title, 'UTF-8', 'UTF-8')) : '';

        return Str::limit(trim($text), 197);
    }

    /** An "https://" or "http://" address of at most 500 characters, or null. */
    public static function url(mixed $url): ?string
    {
        if (! is_string($url) || strlen($url) > 500 || ! preg_match('#^https?://[^\s"<>]+$#', $url) || filter_var($url, FILTER_VALIDATE_URL) === false) {
            return null;
        }

        return $url;
    }
}
