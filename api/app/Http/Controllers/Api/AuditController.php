<?php

namespace App\Http\Controllers\Api;

use App\Enums\Role;
use App\Http\Controllers\Controller;
use App\Models\AuditEntry;
use App\Models\User;
use Illuminate\Http\JsonResponse;
use Illuminate\Http\Request;
use Illuminate\Support\Collection;

/** FR-105: the audit log. */
class AuditController extends Controller
{
    public function index(Request $request): JsonResponse
    {
        // The search text must be text. An array value ("q[]=x") gave a server error.
        $search = $request->validate(['q' => ['nullable', 'string', 'max:200']])['q'] ?? null;
        $ids = $request->user()->visibleCustomerIds();
        $page = AuditEntry::query()
            ->when($ids !== null, fn ($q) => $q->whereIn('customer_id', $ids))
            // whereLike ignores the case of the letters in each database engine. Only an
            // administrator searches the actor and the address, because other users do not see
            // these values for each entry.
            ->when($search, fn ($q, $s) => $q->where(fn ($w) => $w->whereLike('action', "%{$s}%")->orWhereLike('target', "%{$s}%")
                ->when($ids === null, fn ($a) => $a->orWhereLike('actor', "%{$s}%")->orWhere('ip', $s))))
            ->latest('id')
            ->paginate(self::perPage($request, 50, 200));
        $items = collect($page->items());
        $hidden = self::hiddenActors($items, $ids);

        return response()->json([
            'data' => $items->map(fn (AuditEntry $a) => [
                'id' => $a->id,
                'at' => $a->created_at?->toIso8601String(),
                'actor' => $hidden[$a->id] ?? $a->actor,
                'ip' => isset($hidden[$a->id]) ? null : $a->ip,
                'action' => $a->action,
                'target' => $a->target,
            ]),
            'meta' => ['currentPage' => $page->currentPage(), 'lastPage' => $page->lastPage(), 'total' => $page->total()],
        ]);
    }

    /**
     * Audit finding S9: a customer or a reseller sees that an administrator or its provider
     * made a change, but not the name and the client address of that person.
     *
     * @param  Collection<int, AuditEntry>  $entries
     * @param  list<string>|null  $visibleCustomerIds  null for an administrator
     * @return array<int, string> entry ID => the text in place of the actor
     */
    private static function hiddenActors(Collection $entries, ?array $visibleCustomerIds): array
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
