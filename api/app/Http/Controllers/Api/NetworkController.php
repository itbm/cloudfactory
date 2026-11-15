<?php

namespace App\Http\Controllers\Api;

use App\Http\Controllers\Controller;
use App\Models\Customer;
use App\Models\DatabaseServer;
use App\Models\Network;
use App\Models\NetworkMember;
use App\Models\Website;
use App\Services\Audit;
use App\Services\Mesh;
use App\Services\Networks;
use Illuminate\Database\Eloquent\Builder;
use Illuminate\Http\JsonResponse;
use Illuminate\Http\Request;
use Illuminate\Support\Facades\DB;
use Illuminate\Validation\Rule;
use Illuminate\Validation\ValidationException;

/**
 * The private networks of the customers, like a VPC. Each customer has the network "default".
 * The plan of the customer gives the number of named networks in addition to it.
 *
 * A website connects to each other member of each network that it is in, by the name
 * "<name>.internal", on its own server and on a different server. A change of the members goes
 * to the servers with the mesh state (Mesh::refresh).
 */
class NetworkController extends Controller
{
    private function visible(Request $request): Builder
    {
        $ids = $request->user()->visibleCustomerIds();

        return Network::query()->when($ids !== null, fn (Builder $q) => $q->whereIn('customer_id', $ids));
    }

    public function index(Request $request): JsonResponse
    {
        $request->validate(['customerId' => ['nullable', 'string']]);
        $ids = $request->user()->visibleCustomerIds();
        // Each customer that the user sees has the network "default", also with no members yet.
        Customer::query()->when($ids !== null, fn ($q) => $q->whereIn('id', $ids))
            ->when($request->query('customerId'), fn ($q, $id) => $q->where('id', $id))
            ->whereDoesntHave('networks', fn ($q) => $q->where('is_default', true))
            ->pluck('id')->each(fn (string $id) => Networks::defaultFor($id));
        $networks = $this->visible($request)->with('members')
            ->when($request->query('customerId'), fn ($q, $id) => $q->where('customer_id', $id))
            ->orderBy('customer_id')->orderByDesc('is_default')->orderBy('name')->get();

        return response()->json(['data' => $networks->map(fn (Network $n) => self::present($n))]);
    }

    public function show(Request $request, string $id): JsonResponse
    {
        return response()->json(self::present($this->visible($request)->with('members')->findOrFail($id)));
    }

    public function store(Request $request): JsonResponse
    {
        $data = $request->validate([
            'name' => ['required', 'string', 'regex:'.Network::NAME, Rule::notIn([Network::DEFAULT])],
            'customerId' => ['nullable', 'exists:customers,id'],
            'websiteIds' => ['sometimes', 'array', 'max:'.Networks::MAX_MEMBERS],
            'websiteIds.*' => ['string'],
            'databaseServerIds' => ['sometimes', 'array', 'max:'.Networks::MAX_MEMBERS],
            'databaseServerIds.*' => ['string'],
        ], ['name.regex' => 'Use lower-case letters, digits and "-", up to 40 characters.', 'name.not_in' => 'Each customer has the network "default". Use a different name.']);
        $user = $request->user();
        $customerId = $data['customerId'] ?? $user->customer_id;
        if (! $customerId || ! $user->canSeeCustomer($customerId)) {
            throw ValidationException::withMessages(['customerId' => 'Select a customer that you control.']);
        }
        $network = DB::transaction(function () use ($customerId, $data) {
            $customer = Customer::with('plan')->lockForUpdate()->findOrFail($customerId);
            Networks::checkLimit($customer);
            if (Network::where(['customer_id' => $customer->id, 'name' => $data['name']])->exists()) {
                throw ValidationException::withMessages(['name' => 'This customer has a network with this name.']);
            }
            $network = Network::create(['customer_id' => $customer->id, 'name' => $data['name'], 'is_default' => false]);
            self::setMembers($network, $data['websiteIds'] ?? [], $data['databaseServerIds'] ?? []);

            return $network;
        });
        Mesh::refreshLater();
        Audit::record($request, 'Created network', $network->name, $network->customer_id);

        return response()->json(self::present($network->load('members')), 201);
    }

    /** Renames a named network, or changes the members of a network. */
    public function update(Request $request, string $id): JsonResponse
    {
        $network = $this->visible($request)->findOrFail($id);
        $data = $request->validate([
            'name' => ['sometimes', 'string', 'regex:'.Network::NAME, Rule::notIn([Network::DEFAULT])],
            'websiteIds' => ['sometimes', 'array', 'max:'.Networks::MAX_MEMBERS],
            'websiteIds.*' => ['string'],
            'databaseServerIds' => ['sometimes', 'array', 'max:'.Networks::MAX_MEMBERS],
            'databaseServerIds.*' => ['string'],
        ], ['name.regex' => 'Use lower-case letters, digits and "-", up to 40 characters.', 'name.not_in' => 'Each customer has the network "default". Use a different name.']);
        if (isset($data['name']) && $data['name'] !== $network->name) {
            if ($network->is_default) {
                throw ValidationException::withMessages(['name' => 'The network "default" keeps its name.']);
            }
            if (Network::where(['customer_id' => $network->customer_id, 'name' => $data['name']])->exists()) {
                throw ValidationException::withMessages(['name' => 'This customer has a network with this name.']);
            }
        }
        DB::transaction(function () use ($network, $data) {
            if (isset($data['name'])) {
                $network->update(['name' => $data['name']]);
            }
            if (array_key_exists('websiteIds', $data) || array_key_exists('databaseServerIds', $data)) {
                $current = $network->members()->get();
                self::setMembers(
                    $network,
                    $data['websiteIds'] ?? $current->pluck('website_id')->filter()->values()->all(),
                    $data['databaseServerIds'] ?? $current->pluck('database_server_id')->filter()->values()->all(),
                );
                Networks::checkDatabases($network->customer_id);
            }
        });
        Mesh::refreshLater();
        Audit::record($request, 'Changed network', $network->name, $network->customer_id);

        return response()->json(self::present($network->fresh('members')));
    }

    /** Removes a named network. The members stay, and they lose the names of this network. */
    public function destroy(Request $request, string $id): JsonResponse
    {
        $network = $this->visible($request)->findOrFail($id);
        if ($network->is_default) {
            throw ValidationException::withMessages(['network' => 'Each customer has the network "default". The panel does not remove it.']);
        }
        DB::transaction(function () use ($network) {
            $network->delete();
            Networks::checkDatabases($network->customer_id);
        });
        Mesh::refreshLater();
        Audit::record($request, 'Removed network', $network->name, $network->customer_id);

        return response()->json(null, 204);
    }

    /**
     * Replaces the members of a network. Each member must be a website or a database server of
     * the customer of the network.
     *
     * @param  list<string>  $websiteIds
     * @param  list<string>  $databaseServerIds
     *
     * @throws ValidationException
     */
    private static function setMembers(Network $network, array $websiteIds, array $databaseServerIds): void
    {
        $websiteIds = array_values(array_unique($websiteIds));
        $databaseServerIds = array_values(array_unique($databaseServerIds));
        if (Website::whereIn('id', $websiteIds)->where('customer_id', $network->customer_id)->count() !== count($websiteIds)) {
            throw ValidationException::withMessages(['websiteIds' => 'Select only websites of the customer of the network.']);
        }
        if (DatabaseServer::whereIn('id', $databaseServerIds)->where('customer_id', $network->customer_id)->count() !== count($databaseServerIds)) {
            throw ValidationException::withMessages(['databaseServerIds' => 'Select only database servers of the customer of the network.']);
        }
        $network->members()->whereNotNull('website_id')->whereNotIn('website_id', $websiteIds)->delete();
        $network->members()->whereNotNull('database_server_id')->whereNotIn('database_server_id', $databaseServerIds)->delete();
        foreach ($websiteIds as $id) {
            NetworkMember::firstOrCreate(['network_id' => $network->id, 'website_id' => $id]);
        }
        foreach ($databaseServerIds as $id) {
            NetworkMember::firstOrCreate(['network_id' => $network->id, 'database_server_id' => $id]);
            Networks::addressFor(DatabaseServer::findOrFail($id));
        }
    }

    public static function present(Network $n): array
    {
        return [
            'id' => $n->id,
            'customerId' => $n->customer_id,
            'name' => $n->name,
            'isDefault' => $n->is_default,
            'websiteIds' => $n->members->pluck('website_id')->filter()->values(),
            'databaseServerIds' => $n->members->pluck('database_server_id')->filter()->values(),
            'createdAt' => $n->created_at?->toIso8601String(),
        ];
    }
}
