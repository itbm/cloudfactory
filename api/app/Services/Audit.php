<?php

namespace App\Services;

use App\Models\AuditEntry;
use Illuminate\Http\Request;

/** FR-105: record each change with who made it, when, and from which IP address. */
class Audit
{
    public static function record(Request $request, string $action, string $target = '', ?string $customerId = null): void
    {
        $user = $request->user();
        $token = $user?->currentAccessToken();
        $actor = $user?->name ?? 'System';
        if ($token && method_exists($token, 'getAttribute') && $token->getAttribute('name')) {
            $actor = "{$user->name} (token {$token->getAttribute('name')})";
        }

        AuditEntry::create([
            'user_id' => $user?->id,
            'actor' => $actor,
            'ip' => $request->ip(),
            'action' => $action,
            'target' => $target,
            'customer_id' => $customerId ?? $user?->customer_id,
        ]);
    }

    /** Records a change that the panel made by itself, for example an automatic update. */
    public static function system(string $action, string $target = ''): void
    {
        AuditEntry::create(['actor' => 'System', 'ip' => null, 'action' => $action, 'target' => $target]);
    }
}
