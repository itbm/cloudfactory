<?php

namespace App\Http\Controllers\Api;

use App\Http\Controllers\Controller;
use App\Http\Resources\JobResource;
use App\Models\AgentJob;
use App\Models\DatabaseServer;
use App\Models\DnsZone;
use Illuminate\Http\Request;
use Illuminate\Http\Resources\Json\AnonymousResourceCollection;

/** Jobs and their progress. The UI and the Terraform provider wait on these (FR-1606). */
class JobController extends Controller
{
    use Concerns;

    private function query(Request $request)
    {
        $user = $request->user();

        // A user sees the jobs of the websites, of the database servers, of the DNS zones and of the email of
        // its customers.
        $token = $user->currentAccessToken();
        $can = fn (string $area): bool => (bool) ($token?->can("{$area}:read") || $token?->can("{$area}:write"));

        return AgentJob::query()
            // A job of a website needs the scope of the websites, and so on. A job with a
            // customer only is a job of the email. A job with none of them is a job of a server.
            ->where(fn ($q) => $q->whereRaw('1 = 0')
                ->when($can('websites') || $can('backups') || $can('databases'), fn ($q) => $q->orWhereNotNull('website_id'))
                ->when($can('databases'), fn ($q) => $q->orWhereNotNull('database_server_id'))
                ->when($can('dns'), fn ($q) => $q->orWhereNotNull('dns_zone_id'))
                ->when($can('mail'), fn ($q) => $q->orWhere(fn ($q) => $q->whereNotNull('customer_id')->whereNull('website_id')->whereNull('database_server_id')->whereNull('dns_zone_id')))
                ->when($can('servers'), fn ($q) => $q->orWhere(fn ($q) => $q->whereNull('customer_id')->whereNull('website_id')->whereNull('database_server_id')->whereNull('dns_zone_id'))))
            ->when(! $user->isAdministrator(), fn ($q) => $q->where(fn ($q) => $q
                ->whereIn('website_id', $this->visibleWebsites($request)->select('id'))
                ->orWhereIn('database_server_id', DatabaseServer::whereIn('customer_id', $user->visibleCustomerIds())->select('id'))
                ->orWhereIn('dns_zone_id', DnsZone::whereIn('customer_id', $user->visibleCustomerIds())->select('id'))
                ->orWhereIn('customer_id', $user->visibleCustomerIds())));
    }

    public function index(Request $request): AnonymousResourceCollection
    {
        $request->validate(['websiteId' => ['nullable', 'string'], 'databaseServerId' => ['nullable', 'string'], 'dnsZoneId' => ['nullable', 'string'], 'state' => ['nullable', 'in:queued,running,succeeded,failed']]);

        return JobResource::collection($this->query($request)
            ->when($request->query('websiteId'), fn ($q, $id) => $q->where('website_id', $id))
            ->when($request->query('databaseServerId'), fn ($q, $id) => $q->where('database_server_id', $id))
            ->when($request->query('dnsZoneId'), fn ($q, $id) => $q->where('dns_zone_id', $id))
            ->when($request->query('serverId'), fn ($q, $id) => $q->where('server_id', $id))
            ->when($request->query('state'), fn ($q, $s) => $q->where('state', $s))
            ->latest()
            ->paginate(self::perPage($request)));
    }

    public function show(Request $request, string $id): JobResource
    {
        return new JobResource($this->query($request)->findOrFail($id));
    }
}
