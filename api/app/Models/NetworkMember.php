<?php

namespace App\Models;

use Illuminate\Database\Eloquent\Attributes\Fillable;
use Illuminate\Database\Eloquent\Concerns\HasUlids;
use Illuminate\Database\Eloquent\Model;
use Illuminate\Database\Eloquent\Relations\BelongsTo;

/** One member of a network: a website or a database server. */
#[Fillable(['network_id', 'website_id', 'database_server_id'])]
class NetworkMember extends Model
{
    use HasUlids;

    public function network(): BelongsTo
    {
        return $this->belongsTo(Network::class);
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
