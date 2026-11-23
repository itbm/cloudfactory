<?php

namespace App\Services\Deploys;

use Illuminate\Http\Request;

/**
 * The webhook of the deploys of a website (FR-1004). The token in the address finds the Git
 * source. The signature shows that the Git host sent the request and that nobody changed the
 * body: the panel accepts only a request with a correct signature or token from the secret
 * of the source.
 *
 * The Git hosts sign a request in these ways:
 *
 * - GitHub, Gitea, Forgejo and a script: "X-Hub-Signature-256: sha256=<HMAC-SHA256 of the
 *   body with the secret, as hexadecimal>".
 * - Gitea, Forgejo and Gogs: "X-Gitea-Signature", "X-Forgejo-Signature" or
 *   "X-Gogs-Signature" with the HMAC-SHA256 of the body, as hexadecimal.
 * - Bitbucket: "X-Hub-Signature: sha256=<HMAC-SHA256 of the body>".
 * - GitLab: "X-Gitlab-Token: <the secret>". GitLab sends the secret itself, and no signature.
 */
final class Webhook
{
    /** The ID of a commit: SHA-1 or SHA-256, as hexadecimal. */
    public const COMMIT = '/^[0-9a-f]{40}([0-9a-f]{24})?$/';

    /** The headers with an HMAC-SHA256 of the body with the prefix "sha256=". */
    private const PREFIXED = ['X-Hub-Signature-256', 'X-Hub-Signature'];

    /** The headers with an HMAC-SHA256 of the body with no prefix. */
    private const PLAIN = ['X-Gitea-Signature', 'X-Forgejo-Signature', 'X-Gogs-Signature'];

    /** True when the request has a correct signature, or the GitLab token, of the secret. */
    public static function verify(Request $request, string $secret): bool
    {
        if ($secret === '') {
            return false;
        }
        $expected = hash_hmac('sha256', $request->getContent(), $secret);
        foreach (self::PREFIXED as $header) {
            $value = strtolower(trim((string) $request->header($header, '')));
            if (str_starts_with($value, 'sha256=') && hash_equals($expected, substr($value, 7))) {
                return true;
            }
        }
        foreach (self::PLAIN as $header) {
            $value = strtolower(trim((string) $request->header($header, '')));
            if ($value !== '' && hash_equals($expected, $value)) {
                return true;
            }
        }
        $token = $request->header('X-Gitlab-Token');

        return is_string($token) && $token !== '' && hash_equals($secret, $token);
    }

    /** Reads the event of a request. Call it only after verify. */
    public static function read(Request $request): WebhookEvent
    {
        [$provider, $event] = self::provider($request);
        // A pull request event of GitHub, GitLab, Gitea, Forgejo, Gogs or Bitbucket.
        if (PullRequest::provider($request) !== null) {
            return new WebhookEvent($provider, WebhookEvent::PULL_REQUEST, $event);
        }
        $kind = match ($provider) {
            'github' => ['push' => WebhookEvent::PUSH, 'ping' => WebhookEvent::PING][$event] ?? WebhookEvent::OTHER,
            // GitLab sends "Push Hook" also for the test of a webhook.
            'gitlab' => $event === 'Push Hook' ? WebhookEvent::PUSH : WebhookEvent::OTHER,
            'gitea' => $event === 'push' ? WebhookEvent::PUSH : WebhookEvent::OTHER,
            // Bitbucket Cloud sends "repo:push", and Bitbucket Data Center "repo:refs_changed".
            'bitbucket' => ['repo:push' => WebhookEvent::PUSH, 'repo:refs_changed' => WebhookEvent::PUSH, 'diagnostics:ping' => WebhookEvent::PING][$event] ?? WebhookEvent::OTHER,
            // A script gives the branch in "ref" (refs/heads/main or main).
            default => WebhookEvent::PUSH,
        };
        if ($kind !== WebhookEvent::PUSH) {
            return new WebhookEvent($provider, $kind, $event);
        }
        $payload = self::payload($request);
        [$heads, $removed] = $provider === 'bitbucket' ? self::bitbucketChanges($payload) : self::refChange($payload);

        return new WebhookEvent($provider, $kind, $event, $heads, $removed);
    }

    /**
     * The Git host of a request, from its event header, and the name of the event. Gitea and
     * Forgejo also send the header of GitHub, so the panel looks for their headers first.
     *
     * @return array{string, string}
     */
    private static function provider(Request $request): array
    {
        foreach (['X-Forgejo-Event' => 'gitea', 'X-Gitea-Event' => 'gitea', 'X-Gogs-Event' => 'gitea', 'X-GitHub-Event' => 'github', 'X-Gitlab-Event' => 'gitlab', 'X-Event-Key' => 'bitbucket'] as $header => $provider) {
            $event = $request->header($header);
            if (is_string($event) && $event !== '') {
                return [$provider, mb_substr($event, 0, 100)];
            }
        }

        return ['generic', ''];
    }

    /**
     * The body as JSON. GitHub and Gitea can also send a form with the JSON in "payload".
     *
     * @return array<string, mixed>
     */
    private static function payload(Request $request): array
    {
        $data = json_decode($request->getContent(), true);
        if (! is_array($data)) {
            $form = $request->input('payload');
            $data = is_string($form) ? json_decode($form, true) : null;
        }

        return is_array($data) ? $data : [];
    }

    /**
     * The branch of a push of GitHub, GitLab, Gitea or a script: "ref" is "refs/heads/<branch>",
     * and "after" is the new commit. A removed branch has "after" with only zeros.
     *
     * @param  array<string, mixed>  $payload
     * @return array{array<string, string|null>, list<string>}
     */
    private static function refChange(array $payload): array
    {
        $ref = $payload['ref'] ?? null;
        if (! is_string($ref) || $ref === '') {
            return [[], []];
        }
        if (str_starts_with($ref, 'refs/')) {
            if (! str_starts_with($ref, 'refs/heads/')) {
                // For example a tag.
                return [[], []];
            }
            $ref = substr($ref, strlen('refs/heads/'));
        }
        $after = $payload['after'] ?? $payload['checkout_sha'] ?? null;
        if (($payload['deleted'] ?? false) === true || (is_string($after) && preg_match('/^0+$/', $after))) {
            return [[], [$ref]];
        }

        return [[$ref => self::commit($after)], []];
    }

    /**
     * The branches of a push of Bitbucket Cloud ("push.changes" with "new" and "old") or of
     * Bitbucket Data Center ("changes" with "ref", "toHash" and "type").
     *
     * @param  array<string, mixed>  $payload
     * @return array{array<string, string|null>, list<string>}
     */
    private static function bitbucketChanges(array $payload): array
    {
        $heads = [];
        $removed = [];
        foreach ((array) ($payload['push']['changes'] ?? []) as $change) {
            $new = is_array($change) ? ($change['new'] ?? null) : null;
            $old = is_array($change) ? ($change['old'] ?? null) : null;
            if (is_array($new) && ($new['type'] ?? '') === 'branch' && is_string($new['name'] ?? null)) {
                $heads[$new['name']] = self::commit($new['target']['hash'] ?? null);
            } elseif ($new === null && is_array($old) && ($old['type'] ?? '') === 'branch' && is_string($old['name'] ?? null)) {
                $removed[] = $old['name'];
            }
        }
        foreach ((array) ($payload['changes'] ?? []) as $change) {
            $ref = is_array($change) ? ($change['ref'] ?? null) : null;
            if (! is_array($ref) || ($ref['type'] ?? '') !== 'BRANCH' || ! is_string($ref['displayId'] ?? null)) {
                continue;
            }
            if (($change['type'] ?? '') === 'DELETE') {
                $removed[] = $ref['displayId'];
            } else {
                $heads[$ref['displayId']] = self::commit($change['toHash'] ?? null);
            }
        }

        return [$heads, $removed];
    }

    private static function commit(mixed $value): ?string
    {
        return is_string($value) && preg_match(self::COMMIT, $value) ? $value : null;
    }
}
