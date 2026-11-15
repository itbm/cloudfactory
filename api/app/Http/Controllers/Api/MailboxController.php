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
use App\Services\OpenPgpKey;
use App\Validation\Input;
use Illuminate\Database\Eloquent\Builder;
use Illuminate\Http\JsonResponse;
use Illuminate\Http\Request;
use Illuminate\Validation\ValidationException;

/**
 * The mailboxes of a customer.
 *
 * - The plan of the customer gives the number of mailboxes and their total size.
 * - The panel shows the password of a mailbox one time, in the reply to the request that set
 *   it. The panel sends only a hash to the mail server, and it keeps no password.
 * - With "encrypt", the panel makes the OpenPGP keys of the mailbox. The mail server gets the
 *   public key and encrypts each new message with it. The panel keeps the private key in a
 *   form that only the password of the mailbox opens.
 */
class MailboxController extends Controller
{
    public function __construct(private JobDispatcher $jobs) {}

    private function visible(Request $request): Builder
    {
        $ids = $request->user()->visibleCustomerIds();

        return Mailbox::query()->with('domain.server')->when($ids !== null, fn (Builder $q) => $q->whereIn('customer_id', $ids));
    }

    public function index(Request $request): JsonResponse
    {
        $request->validate(['customerId' => ['nullable', 'string'], 'domainId' => ['nullable', 'string']]);

        return response()->json(['data' => $this->visible($request)
            ->when($request->query('customerId'), fn ($q, $id) => $q->where('customer_id', $id))
            ->when($request->query('domainId'), fn ($q, $id) => $q->where('mail_domain_id', $id))
            ->get()->sortBy(fn (Mailbox $m) => $m->domain->name.' '.$m->local_part)->values()->map(fn (Mailbox $m) => self::present($m))]);
    }

    public function store(Request $request): JsonResponse
    {
        $data = $request->validate([
            'domainId' => ['required', 'string'],
            'localPart' => ['required', 'string', Input::rule('mailLocalPart')],
            'quotaBytes' => ['required', 'integer', 'min:'.Mailbox::MIN_QUOTA, 'max:'.Mailbox::MAX_QUOTA],
            ...self::passwordRule(),
            'encrypt' => ['sometimes', 'boolean'],
        ], self::MESSAGES);
        $domain = $this->domain($request, $data['domainId']);
        $customer = $domain->customer;
        abort_if($customer->isSuspended(), 409, 'This customer is suspended. Lift the suspension before you make a mailbox.');
        if (! Mail::addressFree($domain, $data['localPart'])) {
            throw ValidationException::withMessages(['localPart' => 'This address is in use.']);
        }
        Mail::checkPlan($customer, $data['quotaBytes']);

        $password = $data['password'] ?? Mail::password();
        $keys = ($data['encrypt'] ?? false) ? OpenPgpKey::generate($data['localPart'].'@'.$domain->name, $password) : null;
        $mailbox = Mailbox::create([
            'customer_id' => $customer->id, 'mail_domain_id' => $domain->id, 'local_part' => $data['localPart'],
            'quota_bytes' => $data['quotaBytes'], 'status' => MailDomain::PENDING, 'private_key' => $keys['privateKey'] ?? null,
        ])->setRelation('domain', $domain);
        $job = $this->jobs->dispatch($domain->server, AgentAction::SetMailbox, self::payload($mailbox) + ['passwordHash' => Mail::hash($password), 'create' => true]
            + ($keys ? ['setEncryption' => true, 'publicKey' => $keys['publicKey']] : []), "Create mailbox {$mailbox->address()}", customerId: $customer->id);
        Audit::record($request, 'Created mailbox', $mailbox->address(), $customer->id);

        // The password is in this reply only.
        return response()->json(['mailbox' => self::present($mailbox), 'password' => $password, 'job' => new JobResource($job)], 202);
    }

    /**
     * Changes the size, the password or the encryption. "newPassword" asks the panel for a new
     * password.
     *
     * - "encrypt": true makes the OpenPGP keys of the mailbox. The request must also set the
     *   password, because the private key gets that password and the panel keeps no password.
     *   False turns the encryption off.
     * - "publicKey" is an OpenPGP public key of the owner, for an owner who keeps the private
     *   key. Null turns the encryption off.
     * - "currentPassword" with a new password: the private keys that the panel made get the new
     *   password too. Without it, they keep the old password.
     */
    public function update(Request $request, string $id): JsonResponse
    {
        $mailbox = $this->visible($request)->with('customer.plan')->findOrFail($id);
        $data = $request->validate([
            'quotaBytes' => ['sometimes', 'integer', 'min:'.Mailbox::MIN_QUOTA, 'max:'.Mailbox::MAX_QUOTA],
            ...self::passwordRule(),
            'newPassword' => ['sometimes', 'boolean'],
            'currentPassword' => ['sometimes', 'string', 'max:128'],
            'encrypt' => ['sometimes', 'boolean', 'prohibits:publicKey'],
            // An OpenPGP public key turns on the encryption. Null turns it off.
            'publicKey' => ['sometimes', 'nullable', 'string', 'max:'.Mailbox::MAX_PUBLIC_KEY],
        ], self::MESSAGES);
        Mail::refuseRemoved($mailbox, 'mailboxId', 'mailbox');
        if ($mailbox->status === MailDomain::FAILED) {
            throw ValidationException::withMessages(['mailboxId' => 'The panel could not make this mailbox. Remove it and make it again.']);
        }
        $password = $data['password'] ?? (($data['newPassword'] ?? false) ? Mail::password() : null);
        $quota = $data['quotaBytes'] ?? $mailbox->quota_bytes;
        $generate = ($data['encrypt'] ?? false) === true;
        if ($generate && $mailbox->encryption !== null && $mailbox->private_key !== null && $mailbox->public_key !== null) {
            // The mailbox has keys of the panel, and the mail server uses them.
            $generate = false;
            unset($data['encrypt']);
        }
        if ($generate && $password === null) {
            throw ValidationException::withMessages(['encrypt' => 'Give the password of the mailbox, or ask for a new password. The private key gets that password, and the panel keeps no password.']);
        }
        if (($data['encrypt'] ?? null) === false) {
            $data['publicKey'] = null;
        }
        $setKey = $generate || array_key_exists('publicKey', $data);
        $key = match (true) {
            $generate => '',
            $setKey && $data['publicKey'] !== null => Mail::publicKey($data['publicKey']),
            default => '',
        };
        // The same key again, or no key for a mailbox with no encryption, changes nothing.
        $setKey = $generate || ($setKey && $key !== ($mailbox->public_key ?? ''));
        if ($password === null && $quota === $mailbox->quota_bytes && ! $setKey) {
            return response()->json(['mailbox' => self::present($mailbox), 'password' => null, 'job' => null]);
        }
        if ($quota > $mailbox->quota_bytes) {
            Mail::checkPlan($mailbox->customer, $quota, $mailbox);
        }
        Mail::refuseOpenChange($mailbox, 'mailboxId', 'mailbox');

        $changes = ['quota_bytes' => $quota];
        if ($password !== null && $mailbox->private_key !== null) {
            // The private keys get the new password only when the user gives the old password.
            $changes['key_password_current'] = false;
            if (isset($data['currentPassword'])) {
                $moved = OpenPgpKey::changePassword($mailbox->private_key, $data['currentPassword'], $password);
                if ($moved['changed'] === 0) {
                    throw ValidationException::withMessages(['currentPassword' => 'This password does not open the private key of the mailbox.']);
                }
                $changes['private_key'] = $moved['privateKey'];
                $changes['key_password_current'] = $moved['kept'] === 0;
            }
        }
        if ($generate) {
            $keys = OpenPgpKey::generate($mailbox->address(), $password);
            $key = $keys['publicKey'];
            // The old private keys stay: they open the messages from before this change.
            $old = $changes['private_key'] ?? $mailbox->private_key;
            $changes['private_key'] = $keys['privateKey'].($old ?? '');
            $changes['key_password_current'] = $old === null ? true : ($changes['key_password_current'] ?? false);
        }
        $previous = $mailbox->quota_bytes;
        $mailbox->update($changes);
        $job = $this->jobs->dispatch($mailbox->domain->server, AgentAction::SetMailbox, self::payload($mailbox) + ['previousQuotaBytes' => $previous]
            + ($password !== null ? ['passwordHash' => Mail::hash($password)] : [])
            + ($setKey ? ['setEncryption' => true, 'publicKey' => $key] : []), "Change mailbox {$mailbox->address()}", customerId: $mailbox->customer_id);
        Audit::record($request, match (true) {
            $setKey && $key !== '' => 'Turned on mailbox encryption',
            $setKey => 'Turned off mailbox encryption',
            $password !== null => 'Changed mailbox password',
            default => 'Changed mailbox size',
        }, $mailbox->address(), $mailbox->customer_id);

        return response()->json(['mailbox' => self::present($mailbox), 'password' => $password, 'job' => new JobResource($job)], 202);
    }

    /**
     * Gives the private keys that the panel made for a mailbox. Each key is protected with a
     * password of the mailbox: the file is not usable without that password. The owner puts
     * the file in a mail program to read the encrypted messages.
     */
    public function privateKey(Request $request, string $id): JsonResponse
    {
        $mailbox = $this->visible($request)->findOrFail($id);
        abort_if($mailbox->private_key === null, 404, 'The panel made no keys for this mailbox.');
        Audit::record($request, 'Downloaded mailbox private key', $mailbox->address(), $mailbox->customer_id);

        return response()->json([
            'address' => $mailbox->address(),
            'privateKey' => $mailbox->private_key,
            'publicKey' => $mailbox->public_key,
            'passwordCurrent' => (bool) ($mailbox->key_password_current ?? true),
        ]);
    }

    /**
     * Removes a mailbox and all its messages. The aliases of the mailbox go with it: an alias
     * with no mailbox would refuse all its mail.
     */
    public function destroy(Request $request, string $id): JsonResponse
    {
        $mailbox = $this->visible($request)->findOrFail($id);
        Mail::refuseRemoved($mailbox, 'mailboxId', 'mailbox');
        Mail::refuseOpenChange($mailbox, 'mailboxId', 'mailbox');
        $aliases = MailAlias::with('domain.server')->where('customer_id', $mailbox->customer_id)->where('kind', MailAlias::ALIAS)->where('status', '!=', MailDomain::REMOVING)
            ->get()->filter(fn (MailAlias $a) => $a->destinations === [$mailbox->address()]);
        foreach ($aliases as $alias) {
            Mail::refuseOpenChange($alias, 'mailAliasId', 'alias');
        }
        foreach ($aliases as $alias) {
            $was = $alias->status;
            $alias->update(['status' => MailDomain::REMOVING]);
            $this->jobs->dispatch($alias->domain->server, AgentAction::DeleteMailAlias, ['mailAliasId' => $alias->id, 'domain' => $alias->domain->name, 'localPart' => $alias->local_part, 'previousStatus' => $was], "Remove alias {$alias->address()}", customerId: $alias->customer_id);
        }
        $previous = $mailbox->status;
        $mailbox->update(['status' => MailDomain::REMOVING]);
        $job = $this->jobs->dispatch($mailbox->domain->server, AgentAction::DeleteMailbox, self::payload($mailbox) + ['previousStatus' => $previous], "Remove mailbox {$mailbox->address()}", customerId: $mailbox->customer_id);
        Audit::record($request, 'Removed mailbox', $mailbox->address(), $mailbox->customer_id);

        return response()->json(['job' => new JobResource($job)], 202);
    }

    /** A mail domain that the user can see and that accepts a new address. */
    public static function usableDomain(Request $request, string $id): MailDomain
    {
        $ids = $request->user()->visibleCustomerIds();
        $domain = MailDomain::with(['server', 'customer.plan'])->when($ids !== null, fn (Builder $q) => $q->whereIn('customer_id', $ids))->find($id);
        if (! $domain) {
            throw ValidationException::withMessages(['domainId' => 'Select one of your mail domains.']);
        }
        if ($domain->status !== MailDomain::ACTIVE) {
            throw ValidationException::withMessages(['domainId' => $domain->status === MailDomain::PENDING
                ? 'The panel adds this mail domain. Wait until it is complete.'
                : 'This mail domain is not on the mail server.']);
        }

        return $domain;
    }

    private function domain(Request $request, string $id): MailDomain
    {
        return self::usableDomain($request, $id);
    }

    private static function payload(Mailbox $m): array
    {
        return ['mailboxId' => $m->id, 'domain' => $m->domain->name, 'localPart' => $m->local_part, 'quotaBytes' => $m->quota_bytes];
    }

    /** The user types a password, or the panel makes one. */
    private static function passwordRule(): array
    {
        return ['password' => ['sometimes', 'string', 'min:12', 'max:128', 'regex:/^[\x20-\x7e]+$/']];
    }

    private const MESSAGES = [
        'password.min' => 'Use a password of 12 characters or more.',
        'password.regex' => 'Use only letters, digits, spaces and the usual symbols in the password.',
        'quotaBytes.min' => 'A mailbox has 100 MB or more.',
        'publicKey.max' => 'The public key is larger than 64 KB.',
        'encrypt.prohibits' => 'Give a public key of your own, or let the panel make the keys. Do not do the two in one request.',
    ];

    public static function present(Mailbox $m): array
    {
        return [
            'id' => $m->id,
            'address' => $m->address(),
            'localPart' => $m->local_part,
            'domainId' => $m->mail_domain_id,
            'customerId' => $m->customer_id,
            'status' => $m->status,
            'quotaBytes' => $m->quota_bytes,
            // The last report of the agent.
            'usedBytes' => $m->used_bytes,
            // Not null: the mail server encrypts each new message with this OpenPGP key.
            'encryption' => $m->encryption,
            // Not null: the panel made the keys of the mailbox and has the private key.
            // "passwordCurrent" is false when the private key has an older password.
            'privateKey' => $m->private_key !== null ? ['passwordCurrent' => (bool) ($m->key_password_current ?? true)] : null,
            'createdAt' => $m->created_at?->toIso8601String(),
        ];
    }
}
