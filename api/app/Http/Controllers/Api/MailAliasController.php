<?php

namespace App\Http\Controllers\Api;

use App\Enums\AgentAction;
use App\Http\Controllers\Controller;
use App\Http\Resources\JobResource;
use App\Models\MailAlias;
use App\Models\Mailbox;
use App\Models\MailDomain;
use App\Services\Audit;
use App\Services\JobDispatcher;
use App\Services\Mail;
use App\Validation\Input;
use Illuminate\Database\Eloquent\Builder;
use Illuminate\Http\JsonResponse;
use Illuminate\Http\Request;
use Illuminate\Validation\Rule;
use Illuminate\Validation\ValidationException;

/**
 * The aliases and the forwarders of a customer: addresses with no mailbox.
 *
 * - An alias is a second address of one mailbox of the same customer.
 * - A forwarder sends each message to a list of addresses. They can be on other mail servers.
 */
class MailAliasController extends Controller
{
    public function __construct(private JobDispatcher $jobs) {}

    private function visible(Request $request): Builder
    {
        $ids = $request->user()->visibleCustomerIds();

        return MailAlias::query()->with('domain.server')->when($ids !== null, fn (Builder $q) => $q->whereIn('customer_id', $ids));
    }

    public function index(Request $request): JsonResponse
    {
        $request->validate(['customerId' => ['nullable', 'string'], 'domainId' => ['nullable', 'string']]);

        return response()->json(['data' => $this->visible($request)
            ->when($request->query('customerId'), fn ($q, $id) => $q->where('customer_id', $id))
            ->when($request->query('domainId'), fn ($q, $id) => $q->where('mail_domain_id', $id))
            ->get()->sortBy(fn (MailAlias $a) => $a->domain->name.' '.$a->local_part)->values()->map(fn (MailAlias $a) => self::present($a))]);
    }

    public function store(Request $request): JsonResponse
    {
        $data = $request->validate([
            'domainId' => ['required', 'string'],
            'localPart' => ['required', 'string', Input::rule('mailLocalPart')],
            'kind' => ['required', Rule::in([MailAlias::ALIAS, MailAlias::FORWARDER])],
            'destinations' => ['required', 'array', 'min:1', 'max:'.MailAlias::MAX_DESTINATIONS],
            'destinations.*' => ['string', 'distinct', Input::rule('mailAddress')],
        ]);
        $domain = MailboxController::usableDomain($request, $data['domainId']);
        $customer = $domain->customer;
        abort_if($customer->isSuspended(), 409, 'This customer is suspended. Lift the suspension before you make an address.');
        if (! Mail::addressFree($domain, $data['localPart'])) {
            throw ValidationException::withMessages(['localPart' => 'This address is in use.']);
        }
        $address = $data['localPart'].'@'.$domain->name;
        if (in_array($address, $data['destinations'], true)) {
            throw ValidationException::withMessages(['destinations' => 'An address cannot send to itself.']);
        }
        if ($data['kind'] === MailAlias::ALIAS) {
            // An alias is for one mailbox of the customer on the same mail server.
            $mailbox = count($data['destinations']) === 1 ? Mailbox::with('domain')->where('customer_id', $customer->id)->get()
                ->first(fn (Mailbox $m) => $m->address() === $data['destinations'][0] && $m->domain->server_id === $domain->server_id && $m->status !== MailDomain::REMOVING) : null;
            if (! $mailbox) {
                throw ValidationException::withMessages(['destinations' => 'Select one mailbox of this customer. Use a forwarder to send to other addresses.']);
            }
        }

        $alias = MailAlias::create([
            'customer_id' => $customer->id, 'mail_domain_id' => $domain->id, 'local_part' => $data['localPart'],
            'kind' => $data['kind'], 'destinations' => array_values($data['destinations']), 'status' => MailDomain::PENDING,
        ])->setRelation('domain', $domain);
        $job = $this->jobs->dispatch($domain->server, AgentAction::SetMailAlias, self::payload($alias) + ['destinations' => $alias->destinations, 'create' => true], "Create {$alias->kind} {$address}", customerId: $customer->id);
        Audit::record($request, "Created {$alias->kind}", $address, $customer->id);

        return response()->json(['mailAlias' => self::present($alias), 'job' => new JobResource($job)], 202);
    }

    public function destroy(Request $request, string $id): JsonResponse
    {
        $alias = $this->visible($request)->findOrFail($id);
        Mail::refuseRemoved($alias, 'mailAliasId', $alias->kind);
        Mail::refuseOpenChange($alias, 'mailAliasId', $alias->kind);
        $previous = $alias->status;
        $alias->update(['status' => MailDomain::REMOVING]);
        $job = $this->jobs->dispatch($alias->domain->server, AgentAction::DeleteMailAlias, self::payload($alias) + ['previousStatus' => $previous], "Remove {$alias->kind} {$alias->address()}", customerId: $alias->customer_id);
        Audit::record($request, "Removed {$alias->kind}", $alias->address(), $alias->customer_id);

        return response()->json(['job' => new JobResource($job)], 202);
    }

    private static function payload(MailAlias $a): array
    {
        return ['mailAliasId' => $a->id, 'domain' => $a->domain->name, 'localPart' => $a->local_part];
    }

    public static function present(MailAlias $a): array
    {
        return [
            'id' => $a->id,
            'address' => $a->address(),
            'localPart' => $a->local_part,
            'domainId' => $a->mail_domain_id,
            'customerId' => $a->customer_id,
            'kind' => $a->kind,
            'destinations' => $a->destinations,
            'status' => $a->status,
            'createdAt' => $a->created_at?->toIso8601String(),
        ];
    }
}
