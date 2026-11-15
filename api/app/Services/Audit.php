<?php

namespace App\Services;

use App\Models\AuditEntry;
use App\Services\Plugins\Events;
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

        self::announce(AuditEntry::create([
            'user_id' => $user?->id,
            'actor' => $actor,
            'ip' => $request->ip(),
            'action' => $action,
            'target' => $target,
            'customer_id' => $customerId ?? $user?->customer_id,
        ]), $user?->plugin_id);
    }

    /** Records a change that the panel made by itself, for example an automatic update. */
    public static function system(string $action, string $target = ''): void
    {
        self::announce(AuditEntry::create(['actor' => 'System', 'ip' => null, 'action' => $action, 'target' => $target]), null);
    }

    /**
     * Sends the entry to the plugins that want the event "audit.recorded". "pluginId" names the
     * plugin that made the change, so that a plugin can ignore its own changes.
     */
    private static function announce(AuditEntry $entry, ?string $pluginId): void
    {
        Events::emit(Events::AUDIT_RECORDED, [
            'action' => $entry->action,
            'target' => $entry->target,
            'actor' => $entry->actor,
            'customerId' => $entry->customer_id,
            'pluginId' => $pluginId,
            'at' => $entry->created_at?->toIso8601String(),
        ]);
    }
}
