<?php

namespace App\Http\Controllers\Api;

use App\Http\Controllers\Controller;
use App\Models\AuditEntry;
use App\Services\Audit;
use Illuminate\Http\JsonResponse;
use Illuminate\Http\Request;

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
        $hidden = Audit::hiddenActors($items, $ids);

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
}
