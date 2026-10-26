<?php

namespace App\Http\Controllers\Api;

use App\Http\Controllers\Controller;
use App\Models\AuditEntry;
use Illuminate\Http\JsonResponse;
use Illuminate\Http\Request;

/** FR-105: the audit log. */
class AuditController extends Controller
{
    public function index(Request $request): JsonResponse
    {
        $ids = $request->user()->visibleCustomerIds();
        $page = AuditEntry::query()
            ->when($ids !== null, fn ($q) => $q->whereIn('customer_id', $ids))
            ->when($request->query('q'), fn ($q, $s) => $q->where(fn ($w) => $w->where('actor', 'like', "%{$s}%")->orWhere('action', 'like', "%{$s}%")->orWhere('target', 'like', "%{$s}%")->orWhere('ip', $s)))
            ->latest('id')
            ->paginate(min(200, (int) $request->query('perPage', 50)));

        return response()->json([
            'data' => collect($page->items())->map(fn (AuditEntry $a) => [
                'id' => $a->id,
                'at' => $a->created_at?->toIso8601String(),
                'actor' => $a->actor,
                'ip' => $a->ip,
                'action' => $a->action,
                'target' => $a->target,
            ]),
            'meta' => ['currentPage' => $page->currentPage(), 'lastPage' => $page->lastPage(), 'total' => $page->total()],
        ]);
    }
}
