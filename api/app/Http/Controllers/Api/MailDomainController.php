<?php

namespace App\Http\Controllers\Api;

use App\Enums\AgentAction;
use App\Http\Controllers\Controller;
use App\Http\Resources\JobResource;
use App\Models\Customer;
use App\Models\MailDomain;
use App\Models\Server;
use App\Services\Audit;
use App\Services\DomainPolicy;
use App\Services\JobDispatcher;
use App\Services\Mail;
use App\Services\MailDns;
use App\Validation\Input;
use Illuminate\Database\Eloquent\Builder;
use Illuminate\Http\JsonResponse;
use Illuminate\Http\Request;
use Illuminate\Validation\ValidationException;

/**
 * The mail domains of a customer. A mail domain is a domain that receives and sends email on
 * the mail server of one server.
 *
 * - A customer adds only a domain that it controls: a verified domain of one of its websites,
 *   a sub-domain of one, or a domain with the DNS TXT record that the panel asks for.
 * - The mail server makes the DKIM keys. The panel shows the DNS records that the customer
 *   must add, and checks them.
 */
class MailDomainController extends Controller
{
    public function __construct(private JobDispatcher $jobs, private DomainPolicy $policy) {}

    private function visible(Request $request): Builder
    {
        $ids = $request->user()->visibleCustomerIds();

        return MailDomain::query()->with('server')->withCount(['mailboxes', 'aliases'])->when($ids !== null, fn (Builder $q) => $q->whereIn('customer_id', $ids));
    }

    private function find(Request $request, string $id): MailDomain
    {
        return $this->visible($request)->with('customer')->findOrFail($id);
    }

    public function index(Request $request): JsonResponse
    {
        $request->validate(['customerId' => ['nullable', 'string'], 'serverId' => ['nullable', 'string']]);

        return response()->json(['data' => $this->visible($request)
            ->when($request->query('customerId'), fn ($q, $id) => $q->where('customer_id', $id))
            ->when($request->query('serverId'), fn ($q, $id) => $q->where('server_id', $id))
            ->orderBy('name')->get()->map(fn (MailDomain $d) => self::present($d))]);
    }

    public function show(Request $request, string $id): JsonResponse
    {
        return response()->json(self::present($this->find($request, $id)));
    }

    public function store(Request $request): JsonResponse
    {
        $data = $request->validate([
            'name' => ['required', 'string', Input::rule('domain')],
            'customerId' => ['nullable', 'exists:customers,id'],
            'serverId' => ['nullable', 'exists:servers,id'],
        ]);
        $user = $request->user();
        $customerId = $data['customerId'] ?? $user->customer_id;
        if (! $customerId || ! $user->canSeeCustomer($customerId)) {
            throw ValidationException::withMessages(['customerId' => 'Select a customer that you control.']);
        }
        $customer = Customer::with('plan')->findOrFail($customerId);
        abort_if($customer->isSuspended(), 409, 'This customer is suspended. Lift the suspension before you add a mail domain.');
        $name = $data['name'];
        if (MailDomain::where('name', $name)->exists()) {
            throw ValidationException::withMessages(['name' => 'This domain is a mail domain already.']);
        }
        if ($this->policy->isReserved($name, $customer->id) || Server::where('mail_hostname', $name)->exists()) {
            throw ValidationException::withMessages(['name' => 'The panel or a server uses this domain. Use a different domain.']);
        }
        $host = Mail::host($user, $customer, $data['serverId'] ?? null);
        // The customer proves that it controls the domain. If not, a customer could receive
        // the mail of a domain of a different person.
        if (! $this->policy->preVerified($user, $customer->id, $name) && ! $this->policy->proves($customer->id, $name)) {
            return response()->json([
                'message' => 'Show that you control this domain. Add the DNS TXT record, wait some minutes, and add the domain again.',
                'errors' => ['name' => ['Show that you control this domain. Add the DNS TXT record, wait some minutes, and add the domain again.']],
                'verification' => ['type' => 'TXT', 'name' => '_cloudfactory-challenge.'.$name, 'value' => $this->policy->claimToken($customer->id, $name)],
            ], 422);
        }

        $domain = MailDomain::create(['customer_id' => $customer->id, 'server_id' => $host->id, 'name' => $name, 'status' => MailDomain::PENDING]);
        $job = $this->send($domain, $host);
        Audit::record($request, 'Added mail domain', "{$customer->name} {$name}", $customer->id);

        return response()->json(['mailDomain' => self::present($domain->load('server')->loadCount(['mailboxes', 'aliases'])), 'job' => new JobResource($job)], 202);
    }

    private function send(MailDomain $domain, Server $host)
    {
        return $this->jobs->dispatch($host, AgentAction::SetMailDomain, ['domain' => $domain->name, 'mailDomainId' => $domain->id], "Add mail domain {$domain->name}", customerId: $domain->customer_id);
    }

    /** Adds a mail domain again after its job failed. The job of the agent can run again. */
    public function retry(Request $request, string $id): JsonResponse
    {
        $domain = $this->find($request, $id);
        if ($domain->status !== MailDomain::FAILED) {
            throw ValidationException::withMessages(['mailDomainId' => 'The mail server has this domain. Only a domain that the panel could not add can be added again.']);
        }
        $domain->update(['status' => MailDomain::PENDING]);
        $job = $this->send($domain, $domain->server);
        Audit::record($request, 'Added mail domain again', "{$domain->customer->name} {$domain->name}", $domain->customer_id);

        return response()->json(['mailDomain' => self::present($domain), 'job' => new JobResource($job)], 202);
    }

    /** Looks up the DNS records of the domain, and gives the domain with the result. */
    public function check(Request $request, string $id, MailDns $dns): JsonResponse
    {
        $domain = $this->find($request, $id);
        $dns->check($domain);

        return response()->json(self::present($domain));
    }

    /** Removes a mail domain with no mailboxes and no other addresses. */
    public function destroy(Request $request, string $id): JsonResponse
    {
        $domain = $this->find($request, $id);
        Mail::refuseRemoved($domain, 'mailDomainId', 'mail domain');
        if ($domain->mailboxes_count > 0 || $domain->aliases_count > 0) {
            throw ValidationException::withMessages(['mailDomainId' => $domain->mailboxes_count > 0
                ? 'This mail domain has mailboxes. Remove the mailboxes first.'
                : 'This mail domain has aliases or forwarders. Remove them first.']);
        }
        Mail::refuseOpenChange($domain, 'mailDomainId', 'mail domain');
        $previous = $domain->status;
        $domain->update(['status' => MailDomain::REMOVING]);
        $job = $this->jobs->dispatch($domain->server, AgentAction::DeleteMailDomain, ['domain' => $domain->name, 'mailDomainId' => $domain->id, 'previousStatus' => $previous], "Remove mail domain {$domain->name}", customerId: $domain->customer_id);
        Audit::record($request, 'Removed mail domain', "{$domain->customer->name} {$domain->name}", $domain->customer_id);

        return response()->json(['job' => new JobResource($job)], 202);
    }

    public static function present(MailDomain $d): array
    {
        $records = $d->records ?? [];

        return [
            'id' => $d->id,
            'name' => $d->name,
            'customerId' => $d->customer_id,
            'serverId' => $d->server_id,
            'status' => $d->status,
            // Mail programs connect to this host name: IMAP on port 993, SMTP on port 465 or 587.
            'mailHost' => $d->server->mail_hostname,
            // The DNS records that the customer adds. "found" is null before the first check.
            'records' => array_map(fn (array $r) => ['type' => $r['type'], 'name' => $r['name'], 'value' => $r['value'], 'purpose' => $r['purpose'], 'found' => $r['found'] ?? null], $records),
            // True when the last check found all records.
            'dnsReady' => $records !== [] && collect($records)->every(fn (array $r) => ($r['found'] ?? null) === true),
            'dnsCheckedAt' => $d->dns_checked_at?->toIso8601String(),
            'mailboxes' => $d->mailboxes_count ?? $d->mailboxes()->count(),
            'aliases' => $d->aliases_count ?? $d->aliases()->count(),
            'createdAt' => $d->created_at?->toIso8601String(),
        ];
    }
}
