<?php

namespace App\Models;

use App\Enums\UpdatePolicy;
use App\Enums\WebsiteStatus;
use Illuminate\Database\Eloquent\Attributes\Fillable;
use Illuminate\Database\Eloquent\Concerns\HasUlids;
use Illuminate\Database\Eloquent\Factories\HasFactory;
use Illuminate\Database\Eloquent\Model;
use Illuminate\Database\Eloquent\Relations\BelongsTo;
use Illuminate\Database\Eloquent\Relations\HasMany;

#[Fillable([
    'name', 'family', 'reference', 'tag', 'digest', 'available_tag', 'available_digest', 'php_version',
    'support_ends_at', 'policy', 'custom', 'signature_verified', 'checked_at', 'check_error', 'container_port', 'command', 'runtime_version',
    'pack_id', 'default_command', 'default_install',
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

    /** The pack of the image (docs/packs.md), or null for an official image or a custom image. */
    public function pack(): BelongsTo
    {
        return $this->belongsTo(Pack::class);
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

    /** The families of the official images that run an application of the customer. */
    public const APP_FAMILIES = ['node', 'python'];

    /**
     * The usual start command and install command of each application family. The user can
     * change them. The Node.js install command keeps the lock file when it is there. The
     * Python install command makes a virtual environment in the application directory: the
     * start script of the image uses it (images/python/cf-app).
     */
    public const APP_DEFAULTS = [
        'node' => ['command' => 'npm start', 'install' => 'npm install --omit=dev'],
        'python' => ['command' => 'python app.py', 'install' => 'python -m venv .venv && .venv/bin/pip install -r requirements.txt'],
    ];

    public function isPhp(): bool
    {
        return in_array($this->family, ['frankenphp', 'apache-php-fpm'], true);
    }

    /**
     * True for a Node.js or Python image, or an image of a pack: the container runs the start
     * command of the website.
     */
    public function isApp(): bool
    {
        return ! $this->custom && ($this->pack_id !== null || in_array($this->family, self::APP_FAMILIES, true));
    }

    /**
     * The usual start command and install command of an application image, or null for an
     * image that is not an application image.
     *
     * @return array{command: string, install: string}|null
     */
    public function appDefaults(): ?array
    {
        if ($this->pack_id !== null && ! $this->custom) {
            return ['command' => (string) $this->default_command, 'install' => (string) $this->default_install];
        }

        return self::APP_DEFAULTS[$this->family] ?? null;
    }

    /**
     * The kind of the image for a change of the runtime: a website changes only to an image of
     * the same kind. All PHP images are one kind, so that a website can change from FrankenPHP
     * to Apache.
     */
    public function kind(): string
    {
        return $this->isPhp() ? 'php' : (string) $this->family;
    }
}
