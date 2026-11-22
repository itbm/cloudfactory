<?php

namespace App\Http\Controllers\Api;

use App\Http\Controllers\Controller;
use App\Jobs\SyncDnsZone;
use App\Models\DnsAccount;
use App\Models\DnsZone;
use App\Models\Plugin;
use App\Models\User;
use App\Services\Audit;
use App\Services\Dns\Providers;
use Illuminate\Database\Eloquent\Builder;
use Illuminate\Http\JsonResponse;
use Illuminate\Http\Request;
use Illuminate\Validation\ValidationException;

/**
 * The accounts at external DNS providers, for example Cloudflare. A plugin offers each
 * provider. A customer adds an account with the credentials of the provider, and then makes a
 * zone at that provider. An administrator can add an account for the panel, which each
 * customer can use.
 *
 * The API never gives a secret credential back. It gives only the values of the fields that
 * are not secret, and only to the owner of the account.
 */
class DnsAccountController extends Controller
{
    /** The most accounts of one customer, or of the panel. */
    public const MAX_ACCOUNTS = 20;

    public function __construct(private Providers $providers) {}

    private function visible(Request $request): Builder
    {
        $ids = $request->user()->visibleCustomerIds();

        return DnsAccount::with('plugin')->when($ids !== null, fn (Builder $q) => $q->where(fn (Builder $q) => $q->whereNull('customer_id')->orWhereIn('customer_id', $ids)));
    }

    /** An account that the user can change: an account of a customer that the user controls, or any account for an administrator. */
    private function editable(Request $request, string $id): DnsAccount
    {
        $account = $this->visible($request)->findOrFail($id);
        if ($account->customer_id === null && ! $request->user()->isAdministrator()) {
            abort(403, 'Only an administrator can change a DNS account of the panel.');
        }

        return $account;
    }

    /** The accounts that the user can see, and the providers that the plugins offer. */
    public function index(Request $request): JsonResponse
    {
        $ids = $request->user()->visibleCustomerIds();
        // A customer counts only its own zones in an account of the panel.
        $accounts = $this->visible($request)
            ->withCount(['zones' => fn (Builder $q) => $q->when($ids !== null, fn (Builder $q) => $q->whereIn('customer_id', $ids))])
            ->orderBy('name')->get();

        return response()->json([
            'data' => $accounts->map(fn (DnsAccount $a) => self::present($a, $request->user()))->values(),
            'providers' => array_map(fn (array $p) => self::presentProvider($p), Providers::available()),
        ]);
    }

    public function store(Request $request): JsonResponse
    {
        $user = $request->user();
        $data = $request->validate([
            'name' => ['required', 'string', 'max:60'],
            'pluginId' => ['required', 'string', 'max:64'],
            'provider' => ['required', 'string', 'max:40'],
            'credentials' => ['present', 'array'],
            'credentials.*' => ['nullable', 'string', 'max:4096'],
            'customerId' => ['nullable', 'string', 'max:64'],
        ]);
        $customerId = $data['customerId'] ?? ($user->isAdministrator() ? null : $user->customer_id);
        if ($customerId === null ? ! $user->isAdministrator() : ! $user->canSeeCustomer($customerId)) {
            throw ValidationException::withMessages(['customerId' => 'Select a customer that you control.']);
        }
        $plugin = Plugin::where('enabled', true)->find($data['pluginId']);
        $definition = $plugin ? collect($plugin->dnsProviders())->firstWhere('id', $data['provider']) : null;
        if ($definition === null) {
            throw ValidationException::withMessages(['provider' => 'Select a DNS provider of a plugin that is on.']);
        }
        if (DnsAccount::where('customer_id', $customerId)->count() >= self::MAX_ACCOUNTS) {
            throw ValidationException::withMessages(['name' => 'You can have '.self::MAX_ACCOUNTS.' DNS accounts. Remove an account that you do not use.']);
        }
        $credentials = self::credentials($definition, $data['credentials']);
        $label = $this->providers->verify($plugin, $definition, $credentials);
        $account = DnsAccount::create([
            'customer_id' => $customerId, 'plugin_id' => $plugin->id, 'provider' => $definition['id'], 'name' => trim($data['name']),
            'credentials' => $credentials, 'account' => $label, 'checked_at' => now(),
        ]);
        Audit::record($request, 'Added DNS account', "{$account->name} ({$definition['name']})", $customerId);

        return response()->json(self::present($account->loadCount('zones'), $user), 201);
    }

    /**
     * Changes the name or the credentials of an account. The plugin checks new credentials
     * before the panel keeps them. An empty secret field keeps its current value.
     */
    public function update(Request $request, string $id): JsonResponse
    {
        $account = $this->editable($request, $id);
        $data = $request->validate([
            'name' => ['sometimes', 'string', 'max:60'],
            'credentials' => ['sometimes', 'array'],
            'credentials.*' => ['nullable', 'string', 'max:4096'],
        ]);
        if (array_key_exists('credentials', $data)) {
            $definition = $account->definition();
            if ($definition === null || ! $account->plugin->enabled) {
                throw ValidationException::withMessages(['credentials' => 'The plugin of this DNS account is off. Speak to your provider.']);
            }
            $credentials = self::credentials($definition, $data['credentials'], $account->credentials ?? []);
            $account->fill(['credentials' => $credentials, 'account' => $this->providers->verify($account->plugin, $definition, $credentials), 'checked_at' => now()]);
        }
        if (array_key_exists('name', $data)) {
            $account->name = trim($data['name']);
        }
        $account->save();
        // The zones that the provider could not change with the old credentials go again.
        if (array_key_exists('credentials', $data)) {
            foreach ($account->zones()->where('status', DnsZone::ACTIVE)->get() as $zone) {
                if (($zone->provider_state['state'] ?? '') !== 'ok' || ($zone->provider_state['serial'] ?? 0) < $zone->serial) {
                    SyncDnsZone::dispatch($zone->id);
                }
            }
        }
        Audit::record($request, 'Changed DNS account', "{$account->name} ({$account->providerName()})", $account->customer_id);

        return response()->json(self::present($account->loadCount('zones'), $request->user()));
    }

    /** Removes an account. An account that zones use stays: move or remove the zones first. */
    public function destroy(Request $request, string $id): JsonResponse
    {
        $account = $this->editable($request, $id);
        $count = $account->zones()->count();
        if ($count > 0) {
            throw ValidationException::withMessages(['account' => "{$count} DNS ".($count === 1 ? 'zone uses' : 'zones use').' this account. Move the zones to a different DNS provider, or remove them, first.']);
        }
        $account->delete();
        Audit::record($request, 'Removed DNS account', "{$account->name} ({$account->providerName()})", $account->customer_id);

        return response()->json(null, 204);
    }

    /**
     * Checks the credentials against the fields of the provider, and gives them with no empty
     * value. An empty secret field keeps the value of $current.
     *
     * @param  array<string, mixed>  $definition
     * @param  array<string, mixed>  $input
     * @param  array<string, string>  $current
     * @return array<string, string>
     *
     * @throws ValidationException
     */
    private static function credentials(array $definition, array $input, array $current = []): array
    {
        $fields = collect($definition['credentials'])->keyBy('id');
        foreach (array_keys($input) as $key) {
            if (! $fields->has($key)) {
                throw ValidationException::withMessages(["credentials.{$key}" => "{$definition['name']} has no field \"{$key}\"."]);
            }
        }
        $out = [];
        foreach ($fields as $id => $field) {
            $value = trim((string) ($input[$id] ?? ''));
            if ($value === '' && $field['secret'] && isset($current[$id])) {
                $value = $current[$id];
            }
            if ($value === '' && ! $field['optional']) {
                throw ValidationException::withMessages(["credentials.{$id}" => "Enter the {$field['label']}."]);
            }
            if ($value !== '') {
                $out[$id] = $value;
            }
        }

        return $out;
    }

    /** @return array<string, mixed> */
    public static function present(DnsAccount $a, User $user): array
    {
        $definition = $a->definition();
        // A customer does not see the values of an account of the panel.
        $owner = $user->isAdministrator() || $a->customer_id !== null;

        return [
            'id' => $a->id,
            'name' => $a->name,
            // Null for an account of the panel.
            'customerId' => $a->customer_id,
            'pluginId' => $a->plugin_id,
            'provider' => $a->provider,
            'providerName' => $definition['name'] ?? $a->provider,
            // False when the plugin is off, or does not offer the provider now.
            'available' => $definition !== null && (bool) $a->plugin?->enabled,
            'account' => $a->account,
            'dnssec' => (bool) ($definition['dnssec'] ?? false),
            'types' => $definition['types'] ?? [],
            // The values of the fields that are not secret. A secret field is null.
            'credentials' => $owner ? (object) collect($definition['credentials'] ?? [])
                ->mapWithKeys(fn (array $f) => [$f['id'] => $f['secret'] ? null : ($a->credentials[$f['id']] ?? null)])->all() : (object) [],
            'zones' => (int) ($a->zones_count ?? 0),
            'checkedAt' => $a->checked_at?->toIso8601String(),
            'createdAt' => $a->created_at?->toIso8601String(),
        ];
    }

    /** @param  array<string, mixed>  $p */
    public static function presentProvider(array $p): array
    {
        return [
            'pluginId' => $p['plugin']->id,
            'pluginName' => $p['plugin']->name,
            'id' => $p['id'],
            'name' => $p['name'],
            'dnssec' => $p['dnssec'],
            'types' => $p['types'],
            'credentials' => $p['credentials'],
        ];
    }
}
