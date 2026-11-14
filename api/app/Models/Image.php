<?php

namespace App\Models;

use App\Enums\UpdatePolicy;
use App\Enums\WebsiteStatus;
use Illuminate\Database\Eloquent\Attributes\Fillable;
use Illuminate\Database\Eloquent\Concerns\HasUlids;
use Illuminate\Database\Eloquent\Factories\HasFactory;
use Illuminate\Database\Eloquent\Model;
use Illuminate\Database\Eloquent\Relations\HasMany;

#[Fillable([
    'name', 'family', 'reference', 'tag', 'digest', 'available_tag', 'available_digest', 'php_version',
    'support_ends_at', 'policy', 'custom', 'signature_verified', 'checked_at', 'check_error', 'container_port', 'command',
])]
class Image extends Model
{
    use HasFactory, HasUlids;

    protected function casts(): array
    {
        return [
            'policy' => UpdatePolicy::class,
            'support_ends_at' => 'date',
            'custom' => 'boolean',
            'signature_verified' => 'boolean',
            'checked_at' => 'datetime',
            'container_port' => 'integer',
            'command' => 'array',
        ];
    }

    public function websites(): HasMany
    {
        return $this->hasMany(Website::class);
    }

    /**
     * True when a website on a server uses the image. A website that the panel could not make
     * (status "failed") has no container, so it does not use the image: it gets the tag of the
     * image when the panel makes it again.
     */
    public function inUse(): bool
    {
        return $this->websites()->where('status', '!=', WebsiteStatus::Failed->value)->exists();
    }

    /**
     * Changes the image to a new version with no rollout. This is correct only when no website
     * uses the image: there is no website to update and to check. New websites get the new version.
     */
    public function changeVersion(string $tag, ?string $digest): void
    {
        $this->update(['tag' => $tag, 'digest' => $digest, 'available_tag' => null, 'available_digest' => null]);
    }

    public function isPhp(): bool
    {
        return in_array($this->family, ['frankenphp', 'apache-php-fpm'], true);
    }
}
