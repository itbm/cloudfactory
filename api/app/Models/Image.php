<?php

namespace App\Models;

use App\Enums\UpdatePolicy;
use Illuminate\Database\Eloquent\Attributes\Fillable;
use Illuminate\Database\Eloquent\Concerns\HasUlids;
use Illuminate\Database\Eloquent\Factories\HasFactory;
use Illuminate\Database\Eloquent\Model;
use Illuminate\Database\Eloquent\Relations\HasMany;

#[Fillable([
    'name', 'family', 'reference', 'tag', 'digest', 'available_tag', 'available_digest', 'php_version',
    'support_ends_at', 'policy', 'custom', 'signature_verified',
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
        ];
    }

    public function websites(): HasMany
    {
        return $this->hasMany(Website::class);
    }

    public function isPhp(): bool
    {
        return in_array($this->family, ['frankenphp', 'apache-php-fpm'], true);
    }
}
