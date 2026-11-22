<?php

namespace App\Http\Controllers\Api;

use App\Http\Controllers\Controller;
use App\Http\Resources\JobResource;
use App\Models\DnsSecondary;
use App\Models\Server;
use App\Services\Audit;
use App\Services\Dns\Secondaries;
use App\Services\Dns\Zones;
use App\Validation\Input;
use App\Validation\InputRules;
use Illuminate\Http\JsonResponse;
use Illuminate\Http\Request;
use Illuminate\Support\Str;
use Illuminate\Validation\Rule;
use Illuminate\Validation\ValidationException;

/**
 * The secondary DNS servers of the panel: DNS servers outside the panel that copy all zones
 * from the DNS servers of the panel with zone transfers. Only an administrator sees and changes
 * them. Refer to App\Services\Dns\Secondaries.
 *
 * The API gives a TSIG secret only through the "secret" route, which has a low rate limit and
 * an entry in the audit log.
 */
class DnsSecondaryController extends Controller
{
    public function __construct(private Secondaries $secondaries) {}

    /** The secondaries, the catalog zone, and the addresses of the DNS servers of the panel. */
    public function index(): JsonResponse
    {
        $servers = Zones::servers();

        return response()->json([
            'data' => DnsSecondary::orderBy('name')->orderBy('id')->get()->map(fn (DnsSecondary $s) => self::present($s))->all(),
            'catalogZone' => Secondaries::catalogZone(),
            // A secondary copies the zones from these servers ("primary" servers).
            'primaries' => $servers->map(fn (Server $s) => ['nameServer' => (string) $s->dns_name, 'addresses' => array_values($s->addresses ?? [])])->values()->all(),
            // The state of the list on each DNS server of the panel.
            'servers' => Secondaries::serverStates($servers),
        ]);
    }

    public function store(Request $request): JsonResponse
    {
        if (DnsSecondary::count() >= InputRules::MAX_DNS_SECONDARIES) {
            throw ValidationException::withMessages(['name' => 'The panel has '.InputRules::MAX_DNS_SECONDARIES.' secondary DNS servers. Remove one first.']);
        }
        $secondary = new DnsSecondary(['notify' => true, 'catalog' => false]);
        $secondary->fill($this->input($request, $secondary))->save();
        $jobs = $this->secondaries->changed("Add secondary DNS server {$secondary->name}", $secondary->name_server !== null);
        Audit::record($request, 'Added secondary DNS server', $secondary->name);

        return response()->json(['secondary' => self::present($secondary), 'jobs' => JobResource::collection(collect($jobs))->resolve($request)], 202);
    }

    public function update(Request $request, string $id): JsonResponse
    {
        $secondary = DnsSecondary::findOrFail($id);
        $nameServer = $secondary->name_server;
        $secondary->fill($this->input($request, $secondary));
        if (! $secondary->isDirty()) {
            return response()->json(['secondary' => self::present($secondary), 'jobs' => []]);
        }
        $secondary->save();
        $jobs = $this->secondaries->changed("Change secondary DNS server {$secondary->name}", $secondary->name_server !== $nameServer);
        Audit::record($request, 'Changed secondary DNS server', $secondary->name);

        return response()->json(['secondary' => self::present($secondary), 'jobs' => JobResource::collection(collect($jobs))->resolve($request)], 202);
    }

    public function destroy(Request $request, string $id): JsonResponse
    {
        $secondary = DnsSecondary::findOrFail($id);
        $secondary->delete();
        $jobs = $this->secondaries->changed("Remove secondary DNS server {$secondary->name}", $secondary->name_server !== null);
        Audit::record($request, 'Removed secondary DNS server', $secondary->name);

        return response()->json(['jobs' => JobResource::collection(collect($jobs))->resolve($request)], 202);
    }

    /** The TSIG key of a secondary with its secret, for the configuration of the secondary. */
    public function secret(Request $request, string $id): JsonResponse
    {
        $secondary = DnsSecondary::findOrFail($id);
        abort_if($secondary->tsig_algorithm === null, 404, 'This secondary DNS server has no TSIG key.');
        Audit::record($request, 'Viewed the TSIG secret of a secondary DNS server', $secondary->name);

        return response()->json(['name' => $secondary->tsig_name, 'algorithm' => $secondary->tsig_algorithm, 'secret' => (string) $secondary->tsig_secret]);
    }

    /**
     * The fields of a request. A new secondary has a TSIG key unless "tsig" is false. The panel
     * makes the secret, or keeps the secret that the administrator copies from the secondary
     * DNS service.
     *
     * @return array<string, mixed>
     *
     * @throws ValidationException
     */
    private function input(Request $request, DnsSecondary $secondary): array
    {
        $new = ! $secondary->exists;
        $data = $request->validate([
            'name' => [$new ? 'required' : 'sometimes', 'string', 'min:1', 'max:60'],
            'addresses' => [$new ? 'required' : 'sometimes', 'array'],
            'nameServer' => ['sometimes', 'nullable', 'string', Input::rule('domain')],
            'tsig' => ['sometimes', 'boolean'],
            'keyName' => ['sometimes', 'nullable', 'string', Input::rule('tsigName')],
            'algorithm' => ['sometimes', Rule::in(InputRules::TSIG_ALGORITHMS)],
            'secret' => ['sometimes', 'nullable', 'string', 'max:88'],
            'newSecret' => ['sometimes', 'boolean'],
            'notify' => ['sometimes', 'boolean'],
            'catalog' => ['sometimes', 'boolean'],
        ]);
        $fields = [];
        if (array_key_exists('name', $data)) {
            $fields['name'] = trim($data['name']);
        }
        if (array_key_exists('addresses', $data)) {
            // The administrator can give a private address, for example of a secondary in the
            // same private network.
            $fields['addresses'] = Secondaries::addresses('addresses', $data['addresses'], false);
        }
        if (array_key_exists('nameServer', $data)) {
            $fields['name_server'] = $data['nameServer'] !== null ? strtolower($data['nameServer']) : null;
            if ($fields['name_server'] !== null && in_array($fields['name_server'], Zones::nameServers(), true)) {
                throw ValidationException::withMessages(['nameServer' => 'This is the host name of a DNS server of the panel.']);
            }
        }
        foreach (['notify', 'catalog'] as $field) {
            if (array_key_exists($field, $data)) {
                $fields[$field] = (bool) $data[$field];
            }
        }
        $tsig = (bool) ($data['tsig'] ?? ($new || $secondary->tsig_algorithm !== null));
        if (! $tsig) {
            return [...$fields, 'tsig_name' => null, 'tsig_algorithm' => null, 'tsig_secret' => null];
        }
        $algorithm = $data['algorithm'] ?? $secondary->tsig_algorithm ?? 'hmac-sha256';
        $name = ($data['keyName'] ?? null) ?: ($secondary->tsig_name ?? 'cf-'.strtolower((string) Str::ulid()));
        if (str_starts_with($name, Secondaries::ZONE_KEY_PREFIX)) {
            throw ValidationException::withMessages(['keyName' => 'A key name cannot start with "transfer.". The panel uses these names for the keys of the zones.']);
        }
        if (DnsSecondary::where('tsig_name', $name)->when($secondary->exists, fn ($q) => $q->whereKeyNot($secondary->id))->exists()) {
            throw ValidationException::withMessages(['keyName' => 'A different secondary DNS server has a key with this name.']);
        }
        if (($data['secret'] ?? '') !== '') {
            Secondaries::checkSecret('secret', $data['secret']);
            $secret = $data['secret'];
        } elseif (($data['newSecret'] ?? false) || $secondary->tsig_secret === null || $secondary->tsig_algorithm !== $algorithm) {
            $secret = Secondaries::newSecret($algorithm);
        } else {
            $secret = $secondary->tsig_secret;
        }

        return [...$fields, 'tsig_name' => $name, 'tsig_algorithm' => $algorithm, 'tsig_secret' => $secret];
    }

    public static function present(DnsSecondary $s): array
    {
        return [
            'id' => $s->id,
            'name' => $s->name,
            'addresses' => array_values($s->addresses ?? []),
            'nameServer' => $s->name_server,
            // The name and the algorithm of the TSIG key, or null when the transfers have no key.
            'tsig' => $s->tsig_algorithm ? ['name' => $s->tsig_name, 'algorithm' => $s->tsig_algorithm] : null,
            'notify' => $s->notify,
            'catalog' => $s->catalog,
            'createdAt' => $s->created_at?->toIso8601String(),
        ];
    }
}
