<?php

namespace App\Services;

use App\Enums\UpdatePolicy;
use App\Models\Image;
use App\Models\Rollout;
use App\Services\Registry\CosignVerifier;
use App\Services\Registry\RegistryClient;
use Throwable;

/**
 * FR-1110: finds new versions of each image in its registry.
 *
 * - An official image gets a new tag for each release ("0.2.0") and for each rebuild of
 *   the same release on a new base image ("0.1.0-r42", FR-1109). The panel accepts a new
 *   tag only when it has a good signature from the release key.
 * - An image that an administrator added keeps its tag. The panel looks for a new digest
 *   of the same tag.
 *
 * An image that no website uses changes to the new version at once, unless its policy is
 * manual. For an image with websites, the update policy decides what happens (FR-1106):
 * - automatic: the panel starts a rollout.
 * - security_automatic: the panel starts a rollout for a rebuild of the same release only.
 *   A new release waits for an administrator.
 * - manual: the panel shows the update and waits for an administrator.
 */
class ImageUpdateChecker
{
    public function __construct(
        private RegistryClient $registry,
        private CosignVerifier $verifier,
        private RolloutEngine $rollouts,
    ) {}

    /** Checks one image. It gives a short result for the command output. */
    public function check(Image $image): string
    {
        try {
            $result = $image->custom ? $this->checkCustom($image) : $this->checkOfficial($image);
            $image->update(['checked_at' => now(), 'check_error' => null]);
        } catch (Throwable $e) {
            $image->update(['checked_at' => now(), 'check_error' => mb_substr($e->getMessage(), 0, 250)]);

            return 'error: '.$e->getMessage();
        }
        if ($result !== null) {
            $this->apply($image->fresh(), $result);
        }

        return $result ?? 'no update';
    }

    private function checkCustom(Image $image): ?string
    {
        $digest = $this->registry->digest($image->reference, $image->tag);
        if ($digest === null) {
            throw new \RuntimeException("The tag {$image->tag} is not in the registry.");
        }
        if ($image->digest === null) {
            // The first check records the digest that the websites use now.
            $image->update(['digest' => $digest]);

            return null;
        }
        if ($digest === $image->digest || $digest === $image->available_digest) {
            return null;
        }
        $image->update(['available_tag' => $image->tag, 'available_digest' => $digest]);

        return "new digest {$digest}";
    }

    private function checkOfficial(Image $image): ?string
    {
        if (! $this->verifier->hasKey()) {
            throw new \RuntimeException('The release public key is not installed, so the panel cannot verify new tags.');
        }
        $current = self::version($image->tag);
        if ($current === null) {
            throw new \RuntimeException("The tag {$image->tag} is not a release version.");
        }
        $candidates = [];
        foreach ($this->registry->tags($image->reference) as $tag) {
            $v = self::version($tag);
            if ($v !== null && self::compare($v, $current) > 0) {
                $candidates[$tag] = $v;
            }
        }
        uasort($candidates, fn ($a, $b) => self::compare($b, $a));

        // Take the newest tag with a good signature. Stop after five tags.
        foreach (array_slice(array_keys($candidates), 0, 5) as $tag) {
            if ($tag === $image->available_tag) {
                return null;
            }
            $digest = $this->registry->digest($image->reference, $tag);
            if ($digest === null) {
                continue;
            }
            $signatures = $this->registry->signatures($image->reference, $digest);
            if (! $this->verifier->verifies($signatures, $image->reference, $digest)) {
                Audit::system('Refused image tag without a good signature', "{$image->reference}:{$tag}");

                continue;
            }
            $image->update(['available_tag' => $tag, 'available_digest' => $digest, 'signature_verified' => true]);

            return "new tag {$tag}";
        }

        return null;
    }

    /** Starts a rollout when the policy of the image permits it. */
    private function apply(Image $image, string $result): void
    {
        Audit::system('Found image update', "{$image->name}: {$result}");

        $busy = Rollout::where('image_id', $image->id)->whereIn('state', ['running', 'paused'])->exists();
        $used = $image->inUse();
        // An image that no website uses has nothing to roll out and nothing to check. The panel
        // changes it at once, so that an old version does not stay in the list and new websites
        // get the new version. With the manual policy, the administrator decides.
        if (! $used && ! $busy && $image->policy !== UpdatePolicy::Manual && $image->available_tag !== null) {
            $from = $image->tag;
            $image->changeVersion($image->available_tag, $image->available_digest);
            Audit::system('Changed image version', "{$image->name}: {$from} to {$image->tag}. No website uses the image.");

            return;
        }

        $automatic = match ($image->policy) {
            UpdatePolicy::Automatic => true,
            UpdatePolicy::SecurityAutomatic => self::isRebuild($image->tag, $image->available_tag),
            UpdatePolicy::Manual => false,
        };
        if (! $automatic || $busy || ! $used) {
            return;
        }
        $rollout = $this->rollouts->start($image);
        Audit::system('Started automatic rollout', "{$image->name} {$rollout->to_tag}");
    }

    /**
     * Parses "1.2.3" or "1.2.3-r4" (a rebuild of 1.2.3).
     *
     * @return array{int, int, int, int}|null
     */
    public static function version(string $tag): ?array
    {
        if (! preg_match('/^v?(\d+)\.(\d+)\.(\d+)(?:-r(\d+))?$/', $tag, $m)) {
            return null;
        }

        return [(int) $m[1], (int) $m[2], (int) $m[3], (int) ($m[4] ?? 0)];
    }

    /** @param array{int, int, int, int} $a  @param array{int, int, int, int} $b */
    public static function compare(array $a, array $b): int
    {
        return $a <=> $b;
    }

    /** True when the new tag is a rebuild of the same release, for example 0.1.0 to 0.1.0-r42. */
    public static function isRebuild(string $from, ?string $to): bool
    {
        $a = self::version($from);
        $b = $to === null ? null : self::version($to);

        return $a !== null && $b !== null && array_slice($a, 0, 3) === array_slice($b, 0, 3);
    }
}
