<?php

namespace App\Models;

use Illuminate\Database\Eloquent\Attributes\Fillable;
use Illuminate\Database\Eloquent\Concerns\HasUlids;
use Illuminate\Database\Eloquent\Model;
use Illuminate\Database\Eloquent\Relations\BelongsTo;

/**
 * A temporary database user for one database, that a user of the panel opens in Adminer. The
 * panel gives its password to the browser one time, and does not keep it. The panel removes
 * the database user from the database server when it expires.
 */
#[Fillable(['database_tool_id', 'database_id', 'website_id', 'unix_user', 'database_server_id', 'user_id', 'engine', 'database_name', 'privileges', 'name', 'address_key', 'status', 'expires_at'])]
class DatabaseToolLogin extends Model
{
    use HasUlids;

    /** The agent of the database server makes the database user. */
    public const STARTING = 'starting';

    /** The database user exists. Adminer can sign in with it until it expires. */
    public const READY = 'ready';

    /** The agent could not make the database user. */
    public const FAILED = 'failed';

    /** The panel sent the job that removes the database user. */
    public const REMOVING = 'removing';

    protected function casts(): array
    {
        return ['expires_at' => 'datetime'];
    }

    public function tool(): BelongsTo
    {
        return $this->belongsTo(DatabaseTool::class, 'database_tool_id');
    }

    public function database(): BelongsTo
    {
        return $this->belongsTo(Database::class);
    }

    public function website(): BelongsTo
    {
        return $this->belongsTo(Website::class);
    }

    public function databaseServer(): BelongsTo
    {
        return $this->belongsTo(DatabaseServer::class);
    }
}
