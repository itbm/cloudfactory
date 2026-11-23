<?php

namespace App\Models;

use Illuminate\Database\Eloquent\Attributes\Fillable;
use Illuminate\Database\Eloquent\Concerns\HasUlids;
use Illuminate\Database\Eloquent\Model;
use Illuminate\Database\Eloquent\Relations\BelongsTo;

/**
 * FR-206: WordPress in the document root of a website. The agent reports the versions of
 * WordPress, of the plugins and of the themes, and the available updates.
 */
#[Fillable(['website_id', 'database_id', 'status', 'version', 'core_minor', 'core_latest', 'plugins', 'themes', 'site_url', 'admin_user',
    'checked_at', 'auto_checked_at', 'auto_update_core', 'auto_update_plugins', 'auto_update_themes', 'last_update', 'restore_point', 'error', 'pending_install'])]
class WordPressSite extends Model
{
    use HasUlids;

    public const INSTALLING = 'installing';

    public const CHECKING = 'checking';

    /** FR-205: the panel makes the WordPress site of a new staging copy. */
    public const COPYING = 'copying';

    public const READY = 'ready';

    public const FAILED = 'failed';

    /** The settings of the automatic updates of WordPress itself. */
    public const CORE_POLICIES = ['off', 'minor', 'all'];

    protected $table = 'wordpress_sites';

    protected $attributes = ['status' => self::INSTALLING, 'auto_update_core' => 'minor', 'auto_update_plugins' => false, 'auto_update_themes' => false];

    protected function casts(): array
    {
        return [
            'plugins' => 'array',
            'themes' => 'array',
            'last_update' => 'array',
            'restore_point' => 'array',
            'pending_install' => 'encrypted:array',
            'auto_update_plugins' => 'boolean',
            'auto_update_themes' => 'boolean',
            'checked_at' => 'datetime',
            'auto_checked_at' => 'datetime',
        ];
    }

    public function website(): BelongsTo
    {
        return $this->belongsTo(Website::class);
    }

    public function database(): BelongsTo
    {
        return $this->belongsTo(Database::class);
    }
}
