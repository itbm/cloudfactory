<?php

namespace App\Services;

use App\Enums\AgentAction;
use App\Enums\JobState;
use App\Models\AgentJob;
use App\Models\Customer;
use App\Models\MailAlias;
use App\Models\Mailbox;
use App\Models\MailDomain;
use App\Models\Server;
use App\Models\User;
use App\Validation\Input;
use Illuminate\Database\Eloquent\Model;
use Illuminate\Support\Facades\DB;
use Illuminate\Support\Str;
use Illuminate\Validation\ValidationException;

/**
 * The rules of email hosting that the mail controllers share.
 *
 * All customers of a server use one mail server. The mail server does not keep customers apart:
 * the panel does. Thus each check of who owns a domain, a mailbox or an address is in the
 * panel, and the agent accepts only the typed mail actions.
 */
class Mail
{
    /**
     * The private keys of a mailbox for a SetMailbox job. The agent writes them to a file in
     * the files of the mailbox, and the PGP plugin of the webmail reads that file with the
     * password of the mailbox: the user does not upload the keys. The keys are not in the job
     * record. The panel adds the current keys each time that it sends the job.
     *
     * @return array<string, bool|string>
     */
    public static function keyFileFor(AgentJob $job): array
    {
        if ($job->action !== AgentAction::SetMailbox) {
            return [];
        }
        $mailbox = Mailbox::find($job->payload['mailboxId'] ?? '');
        if ($mailbox === null || ($mailbox->private_key === null && ! ($job->payload['setEncryption'] ?? false))) {
            return [];
        }

        return ['setKeyFile' => true, 'privateKeys' => (string) ($mailbox->private_key ?? '')];
    }

    /** The purposes of the DNS records that the panel shows and checks. */
    public const PURPOSES = ['mx', 'spf', 'dkim', 'dmarc'];

    /**
     * The server that gets a new mail domain. A customer has all its mail domains on one
     * server. An administrator can select the server of the first mail domain.
     *
     * @throws ValidationException
     */
    public static function host(User $user, Customer $customer, ?string $serverId): Server
    {
        $current = $customer->mailDomains()->value('server_id');
        if ($serverId !== null && $serverId !== $current) {
            if (! $user->isAdministrator()) {
                throw ValidationException::withMessages(['serverId' => 'Only an administrator can select the server.']);
            }
            if ($current !== null) {
                throw ValidationException::withMessages(['serverId' => 'This customer has mail domains on a different server. A customer has all its mail domains on one server.']);
            }
        }
        $server = Server::find($serverId ?? $current)
            ?? Server::whereNotNull('mail_hostname')->whereNotNull('mail_status')->orderBy('name')->get()->first(fn (Server $s) => $s->hasMail());
        if (! $server || ! $server->hasMail()) {
            throw ValidationException::withMessages(['serverId' => $serverId !== null
                ? 'Email is not turned on for this server.'
                : 'No server has email turned on. An administrator turns on email on the page of a server.']);
        }

        return $server;
    }

    /**
     * Refuses a mailbox that is not in the limits of the plan of the customer: the number of
     * mailboxes, and the total size of all mailboxes.
     *
     * @throws ValidationException
     */
    public static function checkPlan(Customer $customer, int $quotaBytes, ?Mailbox $except = null): void
    {
        $plan = $customer->plan;
        $others = $customer->mailboxes()->when($except, fn ($q) => $q->where('id', '!=', $except->id));
        if (! $except && $others->count() >= $plan->mailboxes) {
            throw ValidationException::withMessages(['localPart' => "The {$plan->name} plan allows {$plan->mailboxes} ".($plan->mailboxes === 1 ? 'mailbox' : 'mailboxes').'. Remove a mailbox, or ask for a larger plan.']);
        }
        $free = $plan->mail_storage_bytes - (int) $others->sum('quota_bytes');
        if ($quotaBytes > $free) {
            throw ValidationException::withMessages(['quotaBytes' => 'The '.$plan->name.' plan allows '.self::size($plan->mail_storage_bytes).' for all mailboxes. '.self::size(max(0, $free)).' is free.']);
        }
    }

    /** The number of private keys that a mailbox keeps: the current key and the keys before it. */
    public const MAX_KEYS = 8;

    /**
     * Runs the checks and the creation of an address in one transaction, with a lock on the
     * customer. The function gets the customer with its plan.
     *
     * @template T
     *
     * @param  callable(Customer): T  $make
     * @return T
     */
    public static function locked(Customer $customer, callable $make): mixed
    {
        return DB::transaction(fn () => $make(Customer::with('plan')->lockForUpdate()->findOrFail($customer->id)));
    }

    /** The newest private keys of a text of private key blocks. The newest block is first. */
    public static function newestKeys(string $keys): string
    {
        $blocks = preg_split('/(?=-----BEGIN PGP PRIVATE KEY BLOCK-----)/', $keys, -1, PREG_SPLIT_NO_EMPTY) ?: [];

        return implode('', array_slice($blocks, 0, self::MAX_KEYS));
    }

    /** A new mailbox password with letters and digits. */
    public static function password(): string
    {
        return Str::password(20, symbols: false);
    }

    /** The hash that the mail server keeps. The panel does not keep the password or the hash. */
    public static function hash(string $password): string
    {
        return password_hash($password, PASSWORD_BCRYPT, ['cost' => 12]);
    }

    /**
     * True when the address is free in the domain. A mailbox, an alias and a forwarder cannot
     * have the same address.
     */
    public static function addressFree(MailDomain $domain, string $localPart): bool
    {
        return ! $domain->mailboxes()->where('local_part', $localPart)->exists()
            && ! $domain->aliases()->where('local_part', $localPart)->exists();
    }

    /**
     * The DNS records that the mail server gives for a domain, from the result of a job. The
     * panel keeps only the records that it can show and check.
     *
     * @return list<array{type: string, name: string, value: string, purpose: string, found: null}>
     */
    public static function records(mixed $records): array
    {
        $kept = [];
        foreach (is_array($records) ? array_slice($records, 0, 20) : [] as $r) {
            if (! is_array($r) || ! in_array($r['purpose'] ?? null, self::PURPOSES, true) || ! in_array($r['type'] ?? null, ['MX', 'TXT'], true)) {
                continue;
            }
            $name = strtolower(rtrim((string) ($r['name'] ?? ''), '.'));
            $value = (string) ($r['value'] ?? '');
            if (! preg_match('/^[a-z0-9._-]{1,253}$/', $name) || $value === '' || strlen($value) > 4000 || preg_match('/[\x00-\x1f\x7f]/', $value)) {
                continue;
            }
            $kept[] = ['type' => $r['type'], 'name' => $name, 'value' => $value, 'purpose' => $r['purpose'], 'found' => null];
        }

        return $kept;
    }

    /**
     * Refuses a change while a change of the same record is with the agent. One change at a
     * time: the panel goes back to the state from before a change that failed.
     *
     * @throws ValidationException
     */
    public static function refuseOpenChange(Model $record, string $key, string $what): void
    {
        $open = AgentJob::whereIn('state', [JobState::Queued->value, JobState::Running->value])
            ->whereIn('action', array_map(fn (AgentAction $a) => $a->value, self::ACTIONS))
            ->get(['payload'])->contains(fn (AgentJob $job) => ($job->payload[$key] ?? null) === $record->getKey());
        if ($open) {
            throw ValidationException::withMessages([$key => "A change of this {$what} is in progress. Wait until it is complete."]);
        }
    }

    public const ACTIONS = [
        AgentAction::SetMailDomain, AgentAction::DeleteMailDomain, AgentAction::SetMailbox,
        AgentAction::DeleteMailbox, AgentAction::SetMailAlias, AgentAction::DeleteMailAlias,
    ];

    /** @throws ValidationException */
    public static function refuseRemoved(MailDomain|Mailbox|MailAlias $record, string $key, string $what): void
    {
        if ($record->status === MailDomain::REMOVING) {
            throw ValidationException::withMessages([$key => "The panel removes this {$what}."]);
        }
    }

    /**
     * Gives an OpenPGP public key block with LF line ends, or refuses text that is not one
     * public key block in ASCII armour. The mail server reads the key: it refuses a key that
     * cannot encrypt, for example a key that expired.
     *
     * @throws ValidationException
     */
    public static function publicKey(string $key): string
    {
        $key = trim(str_replace(["\r\n", "\r"], "\n", $key))."\n";
        $begin = '-----BEGIN PGP PUBLIC KEY BLOCK-----';
        $end = '-----END PGP PUBLIC KEY BLOCK-----';
        if (str_contains($key, 'PRIVATE KEY')) {
            throw ValidationException::withMessages(['publicKey' => 'This is a private key. Do not give a private key to a server. Export the public key from your mail program.']);
        }
        $body = substr($key, strlen($begin), -strlen($end) - 1);
        if (! str_starts_with($key, $begin."\n") || ! str_ends_with($key, "\n".$end."\n") || str_contains($body, '-----') || preg_match('/[^\x20-\x7e\n]/', $body)) {
            throw ValidationException::withMessages(['publicKey' => 'Give one OpenPGP public key. It starts with the line "'.$begin.'" and ends with the line "'.$end.'".']);
        }

        return $key;
    }

    /**
     * What the mail server found in a public key, from the result of a job.
     *
     * @return array{fingerprints: list<string>, keyType: string, expiresAt: ?string, warnings: list<string>}
     */
    public static function encryption(mixed $result): array
    {
        $r = is_array($result) ? $result : [];
        $texts = fn (mixed $list, string $pattern) => array_values(array_filter(is_array($list) ? array_slice($list, 0, 10) : [], fn ($v) => is_string($v) && preg_match($pattern, $v)));
        $expires = is_string($r['expiresAt'] ?? null) && strtotime($r['expiresAt']) ? date(DATE_ATOM, strtotime($r['expiresAt'])) : null;

        return [
            'fingerprints' => $texts($r['fingerprints'] ?? null, '/^[0-9A-Fa-f]{32,128}$/'),
            'keyType' => is_string($r['type'] ?? null) && preg_match('/^[\x20-\x7e]{1,200}$/', $r['type']) ? $r['type'] : 'OpenPGP',
            'expiresAt' => $expires,
            'warnings' => $texts($r['warnings'] ?? null, '/^[A-Za-z]{1,40}$/'),
        ];
    }

    public static function validLocalPart(mixed $value): bool
    {
        return Input::valid('mailLocalPart', $value);
    }

    /** For example "10 GB" or "500 MB". */
    public static function size(int $bytes): string
    {
        return $bytes >= 1024 ** 3
            ? rtrim(rtrim(number_format($bytes / 1024 ** 3, 1), '0'), '.').' GB'
            : (int) round($bytes / 1024 ** 2).' MB';
    }
}
