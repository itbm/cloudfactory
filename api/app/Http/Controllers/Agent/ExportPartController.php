<?php

namespace App\Http\Controllers\Agent;

use App\Http\Controllers\Controller;
use App\Models\Server;
use App\Services\Exports\AccountExports;
use Illuminate\Http\JsonResponse;
use Illuminate\Http\Request;

/**
 * The agent sends a part of an account export in pieces (docs/account-export.md). Each piece
 * names its offset in the part. The panel keeps a piece only when the offset is 0 or the size
 * of the part that it has.
 */
class ExportPartController extends Controller
{
    public function __construct(private AccountExports $exports) {}

    public function __invoke(Request $request, string $id): JsonResponse
    {
        /** @var Server $server */
        $server = $request->attributes->get('server');
        $data = $request->validate(['offset' => ['required', 'integer', 'min:0']]);
        $size = $this->exports->receive($server, $id, (int) $data['offset'], $request->getContent(true));

        return response()->json(['sizeBytes' => $size]);
    }
}
