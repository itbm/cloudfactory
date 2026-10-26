<?php

namespace App\Models;

use App\Enums\DomainKind;
use Illuminate\Database\Eloquent\Attributes\Fillable;
use Illuminate\Database\Eloquent\Model;
use Illuminate\Database\Eloquent\Relations\BelongsTo;

#[Fillable(['website_id', 'name', 'kind', 'dns_points_here', 'certificate_state', 'certificate_expires_at', 'verification_token', 'verified_at'])]
class Domain extends Model
{
    protected function casts(): array
    {
        return [
            'kind' => DomainKind::class,
            'dns_points_here' => 'boolean',
            'certificate_expires_at' => 'datetime',
            'verified_at' => 'datetime',
        ];
    }

    public function website(): BelongsTo
    {
        return $this->belongsTo(Website::class);
    }

    /** CF-03: the edge proxy serves a domain only after the customer proves that they control it. */
    public function verified(): bool
    {
        return $this->verified_at !== null;
    }

    /** The DNS TXT record that proves control of the domain. */
    public function verificationRecord(): string
    {
        return '_cloudfactory-challenge.'.$this->name;
    }
}
