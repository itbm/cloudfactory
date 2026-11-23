<?php

namespace App\Models;

use Illuminate\Database\Eloquent\Attributes\Fillable;
use Illuminate\Database\Eloquent\Concerns\HasUlids;
use Illuminate\Database\Eloquent\Model;
use Illuminate\Database\Eloquent\Relations\BelongsTo;

/** One part of an account export that the agent of a server sends: the files of a website or the dump of a database. */
#[Fillable(['account_export_id', 'agent_job_id', 'server_id', 'path', 'received_bytes', 'size_bytes', 'sha256'])]
class AccountExportPart extends Model
{
    use HasUlids;

    protected function casts(): array
    {
        return ['received_bytes' => 'integer', 'size_bytes' => 'integer'];
    }

    public function export(): BelongsTo
    {
        return $this->belongsTo(AccountExport::class, 'account_export_id');
    }

    public function job(): BelongsTo
    {
        return $this->belongsTo(AgentJob::class, 'agent_job_id');
    }

    /** True when the result of the job gave the size and the hash of the part. */
    public function complete(): bool
    {
        return $this->sha256 !== null && $this->size_bytes === $this->received_bytes;
    }
}
