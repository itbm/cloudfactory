<?php

namespace App\Models;

use Illuminate\Database\Eloquent\Attributes\Fillable;
use Illuminate\Database\Eloquent\Concerns\HasUlids;
use Illuminate\Database\Eloquent\Model;

/**
 * The result of one deploy of a website: the directory "releases/<id>" in the files of the
 * website. The column "ref" has the branch. "migration" is true when the deploy ran the
 * migration command. "health_error" has the error of a failed health check after the switch
 * of such a deploy: the website kept the release (FR-1009).
 */
#[Fillable(['website_id', 'ref', 'commit', 'message', 'state', 'live', 'migration', 'duration_seconds', 'document_root', 'trigger', 'agent_job_id', 'error', 'health_error'])]
class Release extends Model
{
    use HasUlids;

    /** The agent gets the code and builds it. */
    public const BUILDING = 'building';

    /** The release is on the server. The website serves it when "live" is true. */
    public const READY = 'ready';

    public const FAILED = 'failed';

    /** The agent removed the directory of the release. */
    public const REMOVED = 'removed';

    /** The number of earlier releases that stay on the server, for "Go back". */
    public const KEEP = 4;

    protected function casts(): array
    {
        return ['live' => 'boolean', 'migration' => 'boolean'];
    }
}
