<?php

namespace App\Models;

use Illuminate\Database\Eloquent\Attributes\Fillable;
use Illuminate\Database\Eloquent\Attributes\Hidden;
use Illuminate\Database\Eloquent\Concerns\HasUlids;
use Illuminate\Database\Eloquent\Factories\HasFactory;
use Illuminate\Database\Eloquent\Model;
use Illuminate\Database\Eloquent\Relations\HasMany;

#[Fillable([
    'name', 'location', 'roles', 'state', 'agent_url', 'agent_version', 'certificate_fingerprint', 'certificate_pem', 'agent_token_hash',
    'os', 'kernel', 'arch', 'podman_version', 'gvisor_version', 'gvisor_platform', 'bare_metal', 'update_policy',
    'maintenance_window', 'pending_updates', 'reboot_required', 'services', 'usage', 'last_update_at', 'last_seen_at', 'local',
])]
#[Hidden(['agent_token_hash'])]
class Server extends Model
{
    use HasFactory, HasUlids;

    protected function casts(): array
    {
        return [
            'roles' => 'array',
            'pending_updates' => 'array',
            'services' => 'array',
            'usage' => 'array',
            'bare_metal' => 'boolean',
            'reboot_required' => 'boolean',
            'local' => 'boolean',
            'last_update_at' => 'datetime',
            'last_seen_at' => 'datetime',
        ];
    }

    public function websites(): HasMany
    {
        return $this->hasMany(Website::class);
    }

    public function jobs(): HasMany
    {
        return $this->hasMany(AgentJob::class);
    }
}
