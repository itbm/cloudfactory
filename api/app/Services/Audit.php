<?php

namespace App\Services;

use App\Enums\Role;
use App\Models\AuditEntry;
use App\Models\User;
use App\Services\Plugins\Events;
use Illuminate\Database\Eloquent\Model;
use Illuminate\Http\Request;
use Illuminate\Support\Collection;

/** FR-105: record each change with who made it, when, and from which IP address. */
class Audit
{
    public static function record(Request $request, string $action, string $target = '', ?string $customerId = null): void
    {
        $user = $request->user();

        self::announce(AuditEntry::create([
            'user_id' => $user?->id,
            'actor' => self::actor($request),
            'ip' => $request->ip(),
            'action' => $action,
            'target' => $target,
            'customer_id' => $customerId ?? $user?->customer_id,
        ]), $user?->plugin_id);
    }

    /** The name of the person of a request in a log: the user, and the name of an API token. */
    public static function actor(Request $request): string
    {
        $user = $request->user();
        $token = $user?->currentAccessToken();
        $actor = $user?->name ?? 'System';
        if ($token && method_exists($token, 'getAttribute') && $token->getAttribute('name')) {
            $actor = "{$user->name} (token {$token->getAttribute('name')})";
        }

        return $actor;
    }

    /**
     * Records a change that the panel made by itself, for example an automatic update. A change
     * of a customer, for example a deploy from a check of the branch, has the customer, so
     * that the customer sees it in its audit log.
     */
    public static function system(string $action, string $target = '', ?string $customerId = null): void
    {
        self::announce(AuditEntry::create(['actor' => 'System', 'ip' => null, 'action' => $action, 'target' => $target, 'customer_id' => $customerId]), null);
    }

    /**
     * Sends the entry to the plugins that want the event "audit.recorded", and to the plugins
     * that want the named event of the action (Events::NAMED), for example "website.created". "pluginId" names the
     * plugin that made the change, so that a plugin can ignore its own changes.
     */
    private static function announce(AuditEntry $entry, ?string $pluginId): void
    {
        $data = [
            'action' => $entry->action,
            'target' => $entry->target,
            'actor' => $entry->actor,
            'customerId' => $entry->customer_id,
            'pluginId' => $pluginId,
            'at' => $entry->created_at?->toIso8601String(),
        ];
        Events::emit(Events::AUDIT_RECORDED, $data);
        if ($named = Events::NAMED[$entry->action] ?? null) {
            Events::emit($named, $data);
        }
    }

    /**
     * Audit finding S9: a customer or a reseller sees that an administrator or its provider
     * made a change, but not the name and the client address of that person.
     *
     * The entries are rows with "id" and "user_id": entries of the audit log or of the
     * command log of a website.
     *
     * @param  Collection<int, Model>  $entries
     * @param  list<string>|null  $visibleCustomerIds  null for an administrator
     * @return array<int|string, string> entry ID => the text in place of the actor
     */
    public static function hiddenActors(Collection $entries, ?array $visibleCustomerIds): array
    {
        if ($visibleCustomerIds === null) {
            return [];
        }
        $users = User::whereIn('id', $entries->pluck('user_id')->filter()->unique())->get(['id', 'role', 'customer_id'])->keyBy('id');
        $hidden = [];
        foreach ($entries as $entry) {
            if ($entry->user_id === null) {
                continue;
            }
            $user = $users->get($entry->user_id);
            if ($user && in_array($user->customer_id, $visibleCustomerIds, true)) {
                continue;
            }
            $hidden[$entry->id] = match ($user?->role) {
                Role::Administrator => 'Administrator',
                Role::Reseller => 'Your provider',
                default => 'A former user',
            };
        }

        return $hidden;
    }
}
