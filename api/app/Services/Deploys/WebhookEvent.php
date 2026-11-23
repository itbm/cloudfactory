<?php

namespace App\Services\Deploys;

/**
 * One request from a Git host to the webhook address of a website, after the check of its
 * signature (Webhook::read).
 */
final readonly class WebhookEvent
{
    /** A push to one or more branches. */
    public const PUSH = 'push';

    /** A test request, for example when a user adds the webhook on GitHub. */
    public const PING = 'ping';

    /**
     * A pull request event: the preview of the pull request changes (App\Services\Previews).
     * The panel reads the pull request only when the website has previews (PullRequest::from).
     */
    public const PULL_REQUEST = 'pull_request';

    /** An event that the panel does not use, for example a push of a tag. */
    public const OTHER = 'other';

    /**
     * @param  string  $provider  github, gitlab, gitea (also Forgejo and Gogs), bitbucket or generic
     * @param  string  $kind  PUSH, PING, PULL_REQUEST or OTHER
     * @param  string  $event  the name of the event from the Git host, for example "push" or "Push Hook"
     * @param  array<string, string|null>  $heads  the branches that a push changed, with the ID of the new
     *                                             commit of each branch, or null when the request does
     *                                             not give it
     * @param  list<string>  $removed  the branches that a push removed
     */
    public function __construct(
        public string $provider,
        public string $kind,
        public string $event,
        public array $heads = [],
        public array $removed = [],
    ) {}

    /** True when the event is a push that changed the branch. */
    public function pushed(string $branch): bool
    {
        return $this->kind === self::PUSH && array_key_exists($branch, $this->heads);
    }

    /** The ID of the new commit of the branch, or null when the request does not give it. */
    public function commit(string $branch): ?string
    {
        return $this->heads[$branch] ?? null;
    }
}
