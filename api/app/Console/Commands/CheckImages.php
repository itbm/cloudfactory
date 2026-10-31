<?php

namespace App\Console\Commands;

use App\Models\Image;
use App\Services\ImageUpdateChecker;
use App\Services\Registry\CosignVerifier;
use App\Services\Registry\RegistryClient;
use App\Services\RolloutEngine;
use Illuminate\Console\Attributes\Description;
use Illuminate\Console\Attributes\Signature;
use Illuminate\Console\Command;

/** FR-1110: checks the registry of each image for a new version. The scheduler runs it each hour. */
#[Signature('cloudfactory:check-images {--image= : Check only the image with this ID}')]
#[Description('Check the registry of each image for a new signed version')]
class CheckImages extends Command
{
    public function handle(RolloutEngine $rollouts): int
    {
        $checker = new ImageUpdateChecker(new RegistryClient, CosignVerifier::fromConfig(), $rollouts);
        $images = Image::query()->when($this->option('image'), fn ($q, $id) => $q->whereKey($id))->get();
        $failed = 0;
        foreach ($images as $image) {
            $result = $checker->check($image);
            $failed += str_starts_with($result, 'error:') ? 1 : 0;
            $this->line("{$image->reference}:{$image->tag}: {$result}");
        }

        return $failed === 0 ? self::SUCCESS : self::FAILURE;
    }
}
