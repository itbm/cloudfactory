<?php

namespace App\Models;

use App\Enums\RolloutState;
use App\Models\Concerns\GuardsState;
use Illuminate\Database\Eloquent\Attributes\Fillable;
use Illuminate\Database\Eloquent\Concerns\HasUlids;
use Illuminate\Database\Eloquent\Model;
use Illuminate\Database\Eloquent\Relations\BelongsTo;

#[Fillable(['image_id', 'from_tag', 'to_tag', 'severity', 'state', 'stage', 'website_ids', 'updated_ids', 'rolled_back'])]
class Rollout extends Model
{
    use GuardsState, HasUlids;

    /** The table of the permitted changes is in App\Enums\RolloutState. */
    public static function stateAttribute(): string
    {
        return 'state';
    }

    /** FR-1103: canary, 10%, 50%, then all websites. */
    public const STAGES = [
        ['name' => 'Canary', 'share' => 0.0],
        ['name' => '10%', 'share' => 0.1],
        ['name' => '50%', 'share' => 0.5],
        ['name' => 'Everyone', 'share' => 1.0],
    ];

    protected function casts(): array
    {
        return ['state' => RolloutState::class, 'website_ids' => 'array', 'updated_ids' => 'array', 'rolled_back' => 'array', 'stage' => 'integer'];
    }

    public function image(): BelongsTo
    {
        return $this->belongsTo(Image::class);
    }

    /** The number of websites that must be updated at the end of a stage. */
    public function stageTarget(int $stage): int
    {
        $total = count($this->website_ids);
        if ($total === 0) {
            return 0;
        }
        if ($stage === 0) {
            return 1;
        }

        return max(1, (int) ceil($total * self::STAGES[$stage]['share']));
    }
}
