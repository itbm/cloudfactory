<?php

namespace App\Models;

use Illuminate\Database\Eloquent\Attributes\Fillable;
use Illuminate\Database\Eloquent\Attributes\Hidden;
use Illuminate\Database\Eloquent\Concerns\HasUlids;
use Illuminate\Database\Eloquent\Model;

/**
 * A secondary DNS server outside the panel, for example the secondary DNS service of a
 * different company. It copies all zones from each DNS server of the panel with zone
 * transfers (App\Services\Dns\Secondaries).
 */
#[Fillable(['name', 'addresses', 'name_server', 'tsig_name', 'tsig_algorithm', 'tsig_secret', 'notify', 'catalog'])]
#[Hidden(['tsig_secret'])]
class DnsSecondary extends Model
{
    use HasUlids;

    protected function casts(): array
    {
        return [
            'addresses' => 'array',
            'tsig_secret' => 'encrypted',
            'notify' => 'boolean',
            'catalog' => 'boolean',
        ];
    }
}
