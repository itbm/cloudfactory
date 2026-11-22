<?php

namespace App\Http\Controllers\Api;

use App\Enums\WebsiteStatus;
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

    /**
     * A website that moves to a different server accepts no change: the job would go to the old
     * server, and the old server sends the files from before the change. A request that only
     * reads the website continues.
     */
    protected function findWebsite(Request $request, string $id): Website
    {
        $website = $this->visibleWebsites($request)->with(['image', 'domains', 'server'])->findOrFail($id);
        abort_if(! $request->isMethodSafe() && $website->status === WebsiteStatus::Moving, 409, 'This website moves to a different server now. Try again when the move is complete.');

        return $website;
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
