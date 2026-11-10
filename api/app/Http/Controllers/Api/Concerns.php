<?php

namespace App\Http\Controllers\Api;

use App\Models\Customer;
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
        return $this->visibleWebsites($request)->with(['image', 'domains', 'server'])->findOrFail($id);
    }

    /**
     * Audit finding S4: no request starts a website while its customer, or the reseller of its
     * customer, is suspended. The person who can lift the suspension must lift it first.
     */
    protected function refuseSuspended(?Customer $customer): void
    {
        abort_if($customer?->isSuspended() ?? false, 409, 'This customer is suspended. Lift the suspension before you start a website.');
    }
}
