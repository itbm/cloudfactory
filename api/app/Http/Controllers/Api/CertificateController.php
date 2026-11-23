<?php

namespace App\Http\Controllers\Api;

use App\Http\Controllers\Controller;
use App\Jobs\AdvanceCertificateOrder;
use App\Models\Certificate;
use App\Models\Customer;
use App\Services\Audit;
use App\Services\Certificates\AcmeOrders;
use App\Services\Certificates\Certificates;
use App\Services\Certificates\DnsProviders;
use App\Services\Certificates\Pem;
use App\Validation\Input;
use App\Validation\InputRules;
use Illuminate\Database\Eloquent\Builder;
use Illuminate\Http\JsonResponse;
use Illuminate\Http\Request;
use Illuminate\Support\Facades\DB;
use Illuminate\Validation\Rule;
use Illuminate\Validation\ValidationException;

/**
 * The certificates of the customers (docs/certificates.md): a certificate that the customer
 * uploads, or a certificate that the panel gets from Let's Encrypt with DNS validation. Each
 * domain of a website of the customer uses the best certificate that is valid for it. The
 * private keys never leave the panel, except to the agents of the servers.
 */
class CertificateController extends Controller
{
    public function __construct(private AcmeOrders $orders, private Certificates $certificates, private DnsProviders $dns) {}

    private function visible(Request $request): Builder
    {
        $ids = $request->user()->visibleCustomerIds();

        return Certificate::query()->when($ids !== null, fn (Builder $q) => $q->whereIn('customer_id', $ids));
    }

    private function find(Request $request, string $id): Certificate
    {
        return $this->visible($request)->findOrFail($id);
    }

    public function index(Request $request): JsonResponse
    {
        $request->validate(['customerId' => ['nullable', 'string']]);
        $list = $this->visible($request)
            ->when($request->query('customerId'), fn ($q, $id) => $q->where('customer_id', $id))
            ->orderBy('created_at')->orderBy('id')->get();
        $usage = Certificates::usage($list);

        return response()->json(['data' => $list->map(fn (Certificate $c) => self::present($c, $usage[$c->id] ?? []))->values()]);
    }

    public function show(Request $request, string $id): JsonResponse
    {
        $certificate = $this->find($request, $id);

        return response()->json(self::present($certificate, Certificates::usage([$certificate])[$certificate->id] ?? [], chain: true));
    }

    /**
     * Adds a certificate. With the source "upload", the request has the certificate chain and
     * its private key in PEM format. With the source "acme", the panel gets a certificate for
     * the domain, and for "*.domain" when "wildcard" is true.
     */
    public function store(Request $request): JsonResponse
    {
        $data = $request->validate([
            'source' => ['required', Rule::in([Certificate::UPLOAD, Certificate::ACME])],
            'customerId' => ['nullable', 'exists:customers,id'],
            'certificate' => ['required_if:source,upload', 'string', 'max:'.InputRules::MAX_CERTIFICATE_BYTES],
            'privateKey' => ['required_if:source,upload', 'string', 'max:'.InputRules::MAX_CERTIFICATE_BYTES],
            'domain' => ['required_if:source,acme', 'string', Input::rule('domain')],
            'wildcard' => ['boolean'],
        ]);
        $user = $request->user();
        $customerId = $data['customerId'] ?? $user->customer_id;
        if (! $customerId || ! $user->canSeeCustomer($customerId)) {
            throw ValidationException::withMessages(['customerId' => 'Select a customer that you control.']);
        }
        $customer = Customer::findOrFail($customerId);
        abort_if($customer->isSuspended(), 409, 'This customer is suspended. Lift the suspension before you add a certificate.');

        if ($data['source'] === Certificate::UPLOAD) {
            $pem = Pem::upload($data['certificate'], $data['privateKey']);
            $this->checkReserved($pem['names'], 'certificate');
            $certificate = $this->create($customer, [
                'source' => Certificate::UPLOAD,
                'names' => $pem['names'],
                'certificate' => $pem['certificate'],
                'private_key' => $pem['private_key'],
                'fingerprint' => $pem['fingerprint'],
                'issuer' => $pem['issuer'],
                'not_before' => $pem['not_before'],
                'expires_at' => $pem['expires_at'],
                'trusted' => $pem['trusted'],
                'user_id' => $user->id,
            ]);
            Audit::record($request, 'Uploaded certificate', implode(', ', $certificate->names), $customer->id);
            $this->certificates->syncCustomer($customer->id, 'Install the certificate '.$certificate->names[0]);
        } else {
            $domain = strtolower(rtrim($data['domain'], '.'));
            $names = ($data['wildcard'] ?? false) ? [$domain, "*.{$domain}"] : [$domain];
            $this->checkReserved($names, 'domain');
            if (($problem = $this->dns->problem($customer->id, $names)) !== null) {
                throw ValidationException::withMessages(['domain' => $problem]);
            }
            $same = Certificate::where('customer_id', $customer->id)->where('source', Certificate::ACME)->get()
                ->first(fn (Certificate $c) => collect($c->names)->sort()->values()->all() === collect($names)->sort()->values()->all());
            if ($same) {
                throw ValidationException::withMessages(['domain' => 'The customer has a certificate for these names. Renew that certificate.']);
            }
            $certificate = $this->create($customer, ['source' => Certificate::ACME, 'names' => $names, 'user_id' => $user->id]);
            $this->orders->start($certificate);
            AdvanceCertificateOrder::dispatch($certificate->id);
            Audit::record($request, 'Requested certificate', implode(', ', $names), $customer->id);
        }

        return response()->json(self::present($certificate->fresh(), []), 201);
    }

    /** Replaces an uploaded certificate, for example with a renewed certificate from the CA. */
    public function update(Request $request, string $id): JsonResponse
    {
        $certificate = $this->find($request, $id);
        if ($certificate->source !== Certificate::UPLOAD) {
            throw ValidationException::withMessages(['certificate' => 'The panel renews this certificate itself.']);
        }
        abort_if($certificate->customer?->isSuspended(), 409, 'This customer is suspended. Lift the suspension before you change a certificate.');
        $data = $request->validate([
            'certificate' => ['required', 'string', 'max:'.InputRules::MAX_CERTIFICATE_BYTES],
            'privateKey' => ['required', 'string', 'max:'.InputRules::MAX_CERTIFICATE_BYTES],
        ]);
        $pem = Pem::upload($data['certificate'], $data['privateKey']);
        $this->checkReserved($pem['names'], 'certificate');
        $certificate->update([
            'names' => $pem['names'],
            'certificate' => $pem['certificate'],
            'private_key' => $pem['private_key'],
            'fingerprint' => $pem['fingerprint'],
            'issuer' => $pem['issuer'],
            'not_before' => $pem['not_before'],
            'expires_at' => $pem['expires_at'],
            'trusted' => $pem['trusted'],
        ]);
        Audit::record($request, 'Replaced certificate', implode(', ', $certificate->names), $certificate->customer_id);
        $this->certificates->syncCustomer($certificate->customer_id, 'Install the new certificate '.$certificate->names[0]);

        return response()->json(self::present($certificate->fresh(), Certificates::usage([$certificate])[$certificate->id] ?? []));
    }

    /**
     * Starts a new order for a certificate with DNS validation now: a renewal, or a new try
     * after an order failed. An order that runs starts again.
     */
    public function renew(Request $request, string $id): JsonResponse
    {
        $certificate = $this->find($request, $id);
        if ($certificate->source !== Certificate::ACME) {
            throw ValidationException::withMessages(['certificate' => 'Upload a new certificate to replace an uploaded certificate.']);
        }
        abort_if($certificate->customer?->isSuspended(), 409, 'This customer is suspended. Lift the suspension before you renew a certificate.');
        if (($problem = $this->dns->problem($certificate->customer_id, $certificate->names)) !== null) {
            throw ValidationException::withMessages(['certificate' => $problem]);
        }
        $certificate->update(['tries' => 0]);
        $this->orders->start($certificate);
        AdvanceCertificateOrder::dispatch($certificate->id);
        Audit::record($request, 'Renewed certificate', implode(', ', $certificate->names), $certificate->customer_id);

        return response()->json(self::present($certificate->fresh(), Certificates::usage([$certificate])[$certificate->id] ?? []), 202);
    }

    /** Removes a certificate. Its domains get a certificate from the edge proxy again. */
    public function destroy(Request $request, string $id): JsonResponse
    {
        $certificate = $this->find($request, $id);
        if ($certificate->ordering()) {
            $this->orders->cleanUp($certificate);
        }
        $certificate->delete();
        Audit::record($request, 'Removed certificate', implode(', ', $certificate->names ?? []), $certificate->customer_id);
        $jobs = $this->certificates->syncCustomer($certificate->customer_id, 'Remove the certificate '.($certificate->names[0] ?? ''));

        return response()->json(['jobs' => count($jobs)], 202);
    }

    /**
     * Adds a certificate in a transaction with a lock on the customer, so that two requests at
     * the same time cannot go above the limit.
     */
    private function create(Customer $customer, array $values): Certificate
    {
        return DB::transaction(function () use ($customer, $values) {
            Customer::whereKey($customer->id)->lockForUpdate()->first();
            $limit = (int) config('cloudfactory.certificates_per_customer');
            if (Certificate::where('customer_id', $customer->id)->count() >= $limit) {
                throw ValidationException::withMessages(['certificate' => "A customer can have {$limit} certificates. Remove a certificate first."]);
            }

            return Certificate::create(['customer_id' => $customer->id, ...$values]);
        });
    }

    /**
     * A certificate from the panel must not be valid for a host name of the panel or of a
     * server: the edge proxy could otherwise send it for that name.
     *
     * @param  list<string>  $names
     */
    private function checkReserved(array $names, string $field): void
    {
        foreach ($this->certificates->reserved() as $reserved) {
            if (Certificate::namesCover($names, $reserved)) {
                throw ValidationException::withMessages([$field => "The certificate is valid for {$reserved}, a name of the panel. Use a certificate without this name."]);
            }
        }
    }

    /**
     * @param  list<array{domain: string, websiteId: ?string, websiteName: ?string, serverName: string}>  $usedBy
     * @return array<string, mixed>
     */
    public static function present(Certificate $c, array $usedBy, bool $chain = false): array
    {
        return [
            'id' => $c->id,
            'customerId' => $c->customer_id,
            // "upload" or "acme".
            'source' => $c->source,
            'names' => $c->names ?? [],
            // "issuing", "failed", "renewing", "expired" or "valid".
            'state' => $c->state(),
            'issuer' => $c->issuer,
            'fingerprint' => $c->fingerprint,
            'notBefore' => $c->not_before?->toIso8601String(),
            'expiresAt' => $c->expires_at?->toIso8601String(),
            // False for a certificate that browsers do not trust, for example a self-signed certificate.
            'trusted' => $c->certificate !== null ? (bool) $c->trusted : null,
            // Only for "acme": the time of the next renewal, and the stage of the order that runs.
            'renewAt' => $c->source === Certificate::ACME ? $c->renew_at?->toIso8601String() : null,
            'order' => $c->ordering() ? [
                'stage' => (string) ($c->order['stage'] ?? 'new'),
                'startedAt' => $c->order['startedAt'] ?? null,
                // The TXT records of the DNS validation: the name and the value of each one.
                'records' => collect((array) ($c->order['challenges'] ?? []))->map(fn ($r) => ['name' => $r['name'], 'value' => $r['value']])->values()->all(),
            ] : null,
            // The error of the last order, and the time of the next try of a renewal.
            'error' => $c->error,
            'nextTryAt' => ! $c->ordering() && $c->error !== null ? $c->next_try_at?->toIso8601String() : null,
            'usedBy' => $usedBy,
            ...($chain ? ['certificatePem' => $c->certificate] : []),
            'createdAt' => $c->created_at?->toIso8601String(),
            'updatedAt' => $c->updated_at?->toIso8601String(),
        ];
    }
}
