<?php

namespace App\Http\Controllers\Api;

use App\Enums\AgentAction;
use App\Enums\JobState;
use App\Http\Controllers\Controller;
use App\Http\Resources\JobResource;
use App\Models\AgentJob;
use App\Models\Mailbox;
use App\Models\MailboxSnapshot;
use App\Models\MailDomain;
use App\Models\Server;
use App\Services\Audit;
use App\Services\JobDispatcher;
use App\Services\Mail;
use Illuminate\Database\Eloquent\Builder;
use Illuminate\Http\JsonResponse;
use Illuminate\Http\Request;
use Illuminate\Validation\ValidationException;

/**
 * The backups of a mailbox. Each backup is a restic snapshot of all folders and messages of
 * the mailbox, in the repository of the server of the mail domain. The nightly job makes a
 * backup of each mailbox (RunBackups), with the retention policy of the websites. The owner
 * can also make a backup at any time.
 *
 * A restore adds the messages of the backup that the mailbox does not have, in their folders.
 * It does not change or remove a message of the mailbox.
 */
class MailBackupController extends Controller
{
    public function __construct(private JobDispatcher $jobs) {}

    public function index(Request $request, string $id): JsonResponse
    {
        $mailbox = $this->mailbox($request, $id);

        return response()->json(['data' => MailboxSnapshot::where('mailbox_id', $mailbox->id)->latest()->limit(100)->get()->map(fn (MailboxSnapshot $s) => self::present($s))]);
    }

    public function store(Request $request, string $id): JsonResponse
    {
        $mailbox = $this->mailbox($request, $id);
        BackupController::requireTarget();
        self::refuseUnusable($mailbox);
        self::refuseBusy($mailbox);
        $job = self::start($this->jobs, $mailbox, 'manual', "Back up mailbox {$mailbox->address()}");
        Audit::record($request, 'Backed up mailbox', $mailbox->address(), $mailbox->customer_id);

        return response()->json(['job' => new JobResource($job)], 202);
    }

    public function restore(Request $request, string $id, string $snapshotId): JsonResponse
    {
        $mailbox = $this->mailbox($request, $id);
        $snapshot = MailboxSnapshot::where('mailbox_id', $mailbox->id)->findOrFail($snapshotId);
        // A restore changes the mailbox.
        abort_unless($request->user()->tokenCan('mail:write'), 403, 'This restore needs a token with the scope mail:write.');
        BackupController::requireTarget();
        self::refuseUnusable($mailbox);
        // The agent of the server whose repository has the snapshot restores it. That server
        // must have the messages of the mailbox: it is the server of the mail domain, or a
        // member of its mail cluster.
        $server = Server::find($snapshot->server_id);
        $home = $mailbox->domain->server;
        if (! $server || ($server->id !== $home->id && ($home->mail_cluster_id === null || $server->mail_cluster_id !== $home->mail_cluster_id))) {
            throw ValidationException::withMessages(['snapshot' => 'This backup is in the repository of a server that does not have this mailbox now. The panel cannot restore it.']);
        }
        self::refuseBusy($mailbox);
        $job = $this->jobs->dispatch($server, AgentAction::RestoreMailbox, [
            ...self::address($mailbox),
            'backupId' => $snapshot->id,
            'snapshotId' => $snapshot->restic_id,
            'target' => BackupController::repository($server),
        ], "Restore mailbox {$mailbox->address()}", customerId: $mailbox->customer_id);
        Audit::record($request, 'Restored mailbox from backup', $mailbox->address().' '.$snapshot->created_at?->toIso8601String(), $mailbox->customer_id);

        return response()->json(['job' => new JobResource($job)], 202);
    }

    /**
     * Starts a backup of a mailbox on the server of its mail domain. A scheduled backup also
     * applies the retention policy (FR-902). The panel keeps the newest manual backups.
     */
    public static function start(JobDispatcher $jobs, Mailbox $mailbox, string $kind, string $title): AgentJob
    {
        $policy = BackupController::policy();
        $server = $mailbox->domain->server;

        return $jobs->dispatch($server, AgentAction::BackupMailbox, [
            ...self::address($mailbox),
            'kind' => $kind,
            'target' => BackupController::repository($server),
            'retention' => $kind === 'scheduled'
                ? ['daily' => (int) $policy['daily'], 'weekly' => (int) $policy['weekly'], 'monthly' => (int) $policy['monthly']]
                : ['last' => BackupController::manualBackups()],
        ], $title, customerId: $mailbox->customer_id);
    }

    /** @return array{mailboxId: string, domain: string, localPart: string} */
    private static function address(Mailbox $mailbox): array
    {
        return ['mailboxId' => $mailbox->id, 'domain' => $mailbox->domain->name, 'localPart' => $mailbox->local_part];
    }

    /**
     * A mailbox has one backup or restore at a time.
     *
     * @throws ValidationException
     */
    public static function refuseBusy(Mailbox $mailbox): void
    {
        if (self::busy($mailbox)) {
            throw ValidationException::withMessages(['mailboxId' => 'A backup or a restore of this mailbox is in progress. Wait until it is complete.']);
        }
    }

    /** True when a backup or a restore of the mailbox is queued or runs. */
    public static function busy(Mailbox $mailbox): bool
    {
        return isset(self::busyIds($mailbox->customer_id)[$mailbox->id]);
    }

    /**
     * The IDs of the mailboxes with a backup or a restore that is queued or runs, as keys.
     * The payload of a job is encrypted, so the panel reads each open job.
     *
     * @return array<string, true>
     */
    public static function busyIds(?string $customerId = null): array
    {
        return AgentJob::whereIn('state', [JobState::Queued->value, JobState::Running->value])
            ->whereIn('action', [AgentAction::BackupMailbox->value, AgentAction::RestoreMailbox->value])
            ->when($customerId !== null, fn (Builder $q) => $q->where('customer_id', $customerId))
            ->get(['payload'])->mapWithKeys(fn (AgentJob $job) => [(string) ($job->payload['mailboxId'] ?? '') => true])->all();
    }

    /** @throws ValidationException */
    private static function refuseUnusable(Mailbox $mailbox): void
    {
        Mail::refuseRemoved($mailbox, 'mailboxId', 'mailbox');
        if ($mailbox->status !== MailDomain::ACTIVE) {
            throw ValidationException::withMessages(['mailboxId' => $mailbox->status === MailDomain::PENDING
                ? 'The panel makes this mailbox. Wait until it is complete.'
                : 'The panel could not make this mailbox. Remove it and make it again.']);
        }
    }

    private function mailbox(Request $request, string $id): Mailbox
    {
        $ids = $request->user()->visibleCustomerIds();

        return Mailbox::query()->with('domain.server')->when($ids !== null, fn (Builder $q) => $q->whereIn('customer_id', $ids))->findOrFail($id);
    }

    public static function present(MailboxSnapshot $s): array
    {
        return [
            'id' => $s->id,
            'kind' => $s->kind,
            'sizeBytes' => $s->size_bytes,
            'messages' => $s->messages,
            'serverId' => $s->server_id,
            'createdAt' => $s->created_at?->toIso8601String(),
            // The result of the last restore of this backup, or null.
            'lastRestore' => $s->last_restore === null ? null : [
                'at' => $s->last_restore['at'] ?? null,
                'added' => (int) ($s->last_restore['added'] ?? 0),
                'present' => (int) ($s->last_restore['present'] ?? 0),
                'tooLarge' => (int) ($s->last_restore['tooLarge'] ?? 0),
                'failed' => (int) ($s->last_restore['failed'] ?? 0),
            ],
        ];
    }
}
