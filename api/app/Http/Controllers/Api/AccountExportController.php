<?php

namespace App\Http\Controllers\Api;

use App\Http\Controllers\Controller;
use App\Models\AccountExport;
use App\Models\AccountExportPart;
use App\Models\Customer;
use App\Services\Audit;
use App\Services\Exports\AccountExports;
use Illuminate\Database\Eloquent\Builder;
use Illuminate\Http\JsonResponse;
use Illuminate\Http\Request;
use Illuminate\Support\Facades\URL;
use Illuminate\Validation\ValidationException;
use Symfony\Component\HttpFoundation\HeaderUtils;
use Symfony\Component\HttpFoundation\StreamedResponse;

/**
 * Account exports (docs/account-export.md). A user exports the account of a customer as one
 * archive: the description of the account, the files of the websites, the dumps of the
 * databases and the DNS zones. An owner exports its own account. A reseller exports its own
 * account or the account of one of its customers. An administrator exports the account of any
 * customer.
 *
 * The archive has the environment variables of the websites. Thus only a token with the scope
 * "exports" can make or download an export, and a plugin cannot have that scope.
 */
class AccountExportController extends Controller
{
    public function __construct(private AccountExports $exports) {}

    public function index(Request $request): JsonResponse
    {
        $request->validate(['customerId' => ['nullable', 'string']]);
        $exports = $this->visible($request)
            ->when($request->query('customerId'), fn (Builder $q, string $id) => $q->where('customer_id', $id))
            ->with(['customer', 'user', 'parts'])->latest()->orderByDesc('id')->limit(100)->get();

        return response()->json(['data' => $exports->map(fn (AccountExport $e) => self::present($e))->values()]);
    }

    public function show(Request $request, string $id): JsonResponse
    {
        return response()->json(self::present($this->find($request, $id)));
    }

    public function store(Request $request): JsonResponse
    {
        $user = $request->user();
        abort_if($user->isPlugin(), 403, 'A plugin cannot export an account.');
        $data = $request->validate([
            'customerId' => ['nullable', 'string'],
            'files' => ['boolean'],
            'databases' => ['boolean'],
        ]);
        $customerId = $data['customerId'] ?? $user->customer_id;
        if (! $customerId) {
            throw ValidationException::withMessages(['customerId' => 'Choose the customer of the account.']);
        }
        abort_unless($user->canSeeCustomer($customerId), 404, 'Not found.');
        $customer = Customer::findOrFail($customerId);
        $export = $this->exports->start($customer, $user, (bool) ($data['files'] ?? true), (bool) ($data['databases'] ?? true));
        Audit::record($request, 'Started account export', $customer->name, $customer->id);

        return response()->json(self::present($export->load(['customer', 'user', 'parts'])), 202);
    }

    /**
     * Gives a link that downloads the archive. The link has a signature and is valid for some
     * minutes (exports.link_minutes in the settings), so that the browser can download a large
     * file with no token.
     */
    public function downloadLink(Request $request, string $id): JsonResponse
    {
        $export = $this->find($request, $id);
        abort_if($export->state !== AccountExport::READY, 409, 'The archive of this export is not ready.');
        $expires = now()->addMinutes(max(1, (int) config('cloudfactory.exports.link_minutes', 10)));
        Audit::record($request, 'Downloaded account export', $export->customer?->name ?? '', $export->customer_id);

        return response()->json([
            'url' => URL::temporarySignedRoute('export-download', $expires, ['id' => $export->id], absolute: false),
            'fileName' => AccountExports::downloadName($export),
            'sizeBytes' => $export->size_bytes,
            'expiresAt' => $expires->toIso8601String(),
        ]);
    }

    /**
     * The download of the archive with a link from downloadLink. The signature is the only
     * credential. A request with a range ("Range: bytes=N-") continues a download that stopped.
     */
    public function download(Request $request, string $id): StreamedResponse|JsonResponse
    {
        $export = AccountExport::with('customer')->findOrFail($id);
        abort_if($export->state !== AccountExport::READY, 410, 'The archive of this export is not available.');
        $archive = AccountExports::archive($export);
        abort_unless($archive->available(), 410, 'The archive of this export is not available.');
        $size = $archive->size();
        [$start, $end] = [0, $size - 1];
        $status = 200;
        $headers = [
            'Content-Type' => 'application/x-tar',
            'Content-Disposition' => HeaderUtils::makeDisposition(HeaderUtils::DISPOSITION_ATTACHMENT, AccountExports::downloadName($export)),
            'Accept-Ranges' => 'bytes',
            'Cache-Control' => 'no-store',
        ];
        if ($range = $request->header('Range')) {
            if (! preg_match('/^bytes=(\d*)-(\d*)$/', trim($range), $m) || ($m[1] === '' && $m[2] === '')) {
                return response()->json(['message' => 'The panel accepts one range of bytes only.'], 416, ['Content-Range' => "bytes */{$size}"]);
            }
            // "bytes=-N": the last N bytes.
            [$start, $end] = $m[1] === '' ? [max(0, $size - (int) $m[2]), $size - 1] : [(int) $m[1], $m[2] === '' ? $size - 1 : min((int) $m[2], $size - 1)];
            if ($start > $end || $start >= $size) {
                return response()->json(['message' => 'The range is not in the archive.'], 416, ['Content-Range' => "bytes */{$size}"]);
            }
            $status = 206;
            $headers['Content-Range'] = "bytes {$start}-{$end}/{$size}";
        }
        $headers['Content-Length'] = (string) ($end - $start + 1);

        return response()->stream(function () use ($archive, $start, $end) {
            // A large archive takes more time than the time limit of a request.
            set_time_limit(0);
            $out = fopen('php://output', 'wb');
            $archive->send($out, $start, $end);
            fclose($out);
        }, $status, $headers);
    }

    /** Removes an export and its archive. An export in progress stops. */
    public function destroy(Request $request, string $id): JsonResponse
    {
        $export = $this->find($request, $id);
        AccountExports::remove($export);
        Audit::record($request, 'Removed account export', $export->customer?->name ?? '', $export->customer_id);

        return response()->json(null, 204);
    }

    /** @return array<string, mixed> */
    public static function present(AccountExport $e): array
    {
        $parts = $e->parts;

        return [
            'id' => $e->id,
            'customerId' => $e->customer_id,
            'customerName' => $e->customer?->name,
            'state' => $e->state,
            'files' => $e->files,
            'databases' => $e->databases,
            // The progress: the parts that the servers sent completely, and all parts.
            'parts' => ['complete' => $parts->filter(fn (AccountExportPart $p) => $p->complete())->count(), 'total' => $parts->count()],
            'receivedBytes' => (int) $parts->sum('received_bytes'),
            // The size of the archive, when it is ready.
            'sizeBytes' => $e->state === AccountExport::READY ? $e->size_bytes : null,
            // The items that are not in the archive, for example the messages of the mailboxes.
            'warnings' => array_values($e->description['warnings'] ?? []),
            'error' => $e->error,
            'createdBy' => $e->user?->name,
            'createdAt' => $e->created_at?->toIso8601String(),
            'finishedAt' => $e->finished_at?->toIso8601String(),
            'expiresAt' => $e->expires_at?->toIso8601String(),
        ];
    }

    private function visible(Request $request): Builder
    {
        $ids = $request->user()->visibleCustomerIds();

        return AccountExport::query()->when($ids !== null, fn (Builder $q) => $q->whereIn('customer_id', $ids));
    }

    private function find(Request $request, string $id): AccountExport
    {
        return $this->visible($request)->with(['customer', 'user', 'parts'])->findOrFail($id);
    }
}
