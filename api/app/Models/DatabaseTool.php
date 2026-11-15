<?php

namespace App\Models;

use Illuminate\Database\Eloquent\Attributes\Fillable;
use Illuminate\Database\Eloquent\Concerns\HasUlids;
use Illuminate\Database\Eloquent\Model;
use Illuminate\Database\Eloquent\Relations\BelongsTo;
use Illuminate\Database\Eloquent\Relations\HasMany;

/**
 * The database tool (Adminer) of one customer: one container on the panel server, with its own
 * Unix user and its own gVisor sandbox. The agent operates it as a website with no files of the
 * customer. It has a route with its own path on the Adminer domain of the panel, and the
 * firewall lets it connect only to the database servers of its customer.
 */
#[Fillable(['customer_id', 'server_id', 'unix_user', 'image_tag', 'status'])]
class DatabaseTool extends Model
{
    use HasUlids;

    /** The agent makes the container, or changes it to a new image. */
    public const STARTING = 'starting';

    public const RUNNING = 'running';

    /** The agent could not make the container. The next request to open a database tries again. */
    public const FAILED = 'failed';

    public function customer(): BelongsTo
    {
        return $this->belongsTo(Customer::class);
    }

    public function server(): BelongsTo
    {
        return $this->belongsTo(Server::class);
    }

    public function logins(): HasMany
    {
        return $this->hasMany(DatabaseToolLogin::class);
    }

    /** The path of the tool on the Adminer domain, for example "/01jbq3z5k8n6t4w2x9y7r5p3m1/". */
    public function path(): string
    {
        return '/'.strtolower($this->id).'/';
    }
}
