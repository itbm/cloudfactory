<?php

namespace App\Models;

use Illuminate\Database\Eloquent\Attributes\Fillable;
use Illuminate\Database\Eloquent\Concerns\HasUlids;
use Illuminate\Database\Eloquent\Model;
use Illuminate\Database\Eloquent\Relations\BelongsTo;

/**
 * The preview website of one pull request (App\Services\Previews). "website" is the website of
 * the Git source, and "previewWebsite" is the preview. A preview with no preview website is a
 * preview that the panel could not make: "error" has the cause.
 */
#[Fillable(['website_id', 'preview_website_id', 'number', 'branch', 'title', 'url', 'deploy_again', 'error'])]
class Preview extends Model
{
    use HasUlids;

    protected function casts(): array
    {
        return ['number' => 'integer', 'deploy_again' => 'boolean'];
    }

    public function website(): BelongsTo
    {
        return $this->belongsTo(Website::class);
    }

    public function previewWebsite(): BelongsTo
    {
        return $this->belongsTo(Website::class, 'preview_website_id');
    }
}
