<?php

namespace App\Http\Controllers\Api;

use App\Models\Website;
use Illuminate\Database\Eloquent\Builder;
use Illuminate\Http\Request;

/** Helpers that limit each user to the customers that they can see (FR-101, FR-102). */
trait Concerns
{
    protected function visibleWebsites(Request $request): Builder
    {
        $ids = $request->user()->visibleCustomerIds();

        return Website::query()->when($ids !== null, fn (Builder $q) => $q->whereIn('customer_id', $ids));
    }

    protected function findWebsite(Request $request, string $id): Website
    {
        return $this->visibleWebsites($request)->with(['image', 'domains'])->findOrFail($id);
    }
}
