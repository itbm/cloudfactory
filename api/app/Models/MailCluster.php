<?php

namespace App\Models;

use Illuminate\Database\Eloquent\Attributes\Fillable;
use Illuminate\Database\Eloquent\Attributes\Hidden;
use Illuminate\Database\Eloquent\Concerns\HasUlids;
use Illuminate\Database\Eloquent\Model;
use Illuminate\Database\Eloquent\Relations\HasMany;
use Illuminate\Support\Collection;

/**
 * Mail servers on three or five servers that share their data. Each member has a mail server,
 * a node of the database cluster and a member of the cluster store.
 */
#[Fillable(['name', 'hostname', 'status', 'phase', 'network', 'secrets', 'error'])]
#[Hidden(['secrets'])]
class MailCluster extends Model
{
    use HasUlids;

    public const SETTING_UP = 'setting_up';

    public const RUNNING = 'running';

    public const FAILED = 'failed';

    /** The steps of the setup, in order. */
    public const PHASES = ['prepare', 'bootstrap', 'join', 'first', 'rest', 'done'];

    /** A cluster needs a majority after one failure: three or five members. */
    public const SIZES = [3, 5];

    protected function casts(): array
    {
        return ['secrets' => 'encrypted:array'];
    }

    public function servers(): HasMany
    {
        return $this->hasMany(Server::class);
    }

    /**
     * The members in the order of their numbers.
     *
     * @return Collection<int, Server>
     */
    public function members()
    {
        return $this->servers->sortBy(fn (Server $s) => $s->mail_cluster_member['id'] ?? 0)->values();
    }
}
