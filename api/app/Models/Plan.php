<?php

namespace App\Models;

use Illuminate\Database\Eloquent\Attributes\Fillable;
use Illuminate\Database\Eloquent\Concerns\HasUlids;
use Illuminate\Database\Eloquent\Factories\HasFactory;
use Illuminate\Database\Eloquent\Model;

#[Fillable(['name', 'limits', 'websites', 'mailboxes', 'databases', 'php_extensions'])]
class Plan extends Model
{
    use HasFactory, HasUlids;

    protected function casts(): array
    {
        return ['limits' => 'array', 'php_extensions' => 'array'];
    }
}
