<?php

namespace App\Http\Controllers\Api;

use App\Http\Controllers\Controller;
use App\Http\Resources\JobResource;
use App\Models\DatabaseToolLogin;
use App\Services\Audit;
use App\Services\DatabaseTools;
use Illuminate\Http\JsonResponse;
use Illuminate\Http\Request;
use Illuminate\Validation\Rule;

/**
 * The database tool (Adminer). A user opens a MariaDB or PostgreSQL database of a website in
 * the Adminer of its customer, with single sign-on: the panel makes a temporary database user
 * for the database, and gives its password to the browser one time. The web panel sends the
 * sign-in form to Adminer, so the user types no password.
 */
class DatabaseToolController extends Controller
{
    use Concerns;

    public function __construct(private DatabaseTools $tools) {}

    public function store(Request $request, string $websiteId, string $databaseId): JsonResponse
    {
        $website = $this->findWebsite($request, $websiteId);
        $database = $website->databases()->findOrFail($databaseId);
        $data = $request->validate([
            'privileges' => ['sometimes', Rule::in(DatabaseUserController::PRIVILEGES)],
        ]);
        abort_if($website->customer?->isSuspended() ?? false, 409, 'This customer is suspended. Lift the suspension before you open a database.');
        $open = $this->tools->open($request->user(), $database, $data['privileges'] ?? 'readwrite');
        Audit::record($request, 'Opened database in Adminer', "{$website->name} {$database->name}", $website->customer_id);

        return response()->json([
            'login' => self::present($open['login']),
            // The fields of the Adminer sign-in form. The panel shows the password only here.
            'form' => $open['form'],
            'jobs' => JobResource::collection($open['jobs']),
        ], 202);
    }

    /** The state of a login, so that the web panel knows when Adminer can sign in. */
    public function show(Request $request, string $id): JsonResponse
    {
        // Only the user who opened the database sees the login.
        $login = DatabaseToolLogin::with('tool')->where('user_id', $request->user()->id)->findOrFail($id);

        return response()->json(['login' => self::present($login)]);
    }

    public static function present(DatabaseToolLogin $login): array
    {
        return [
            'id' => $login->id,
            'status' => DatabaseTools::state($login),
            'url' => DatabaseTools::url($login->tool),
            'websiteId' => $login->website_id,
            'databaseId' => $login->database_id,
            'databaseName' => $login->database_name,
            'privileges' => $login->privileges,
            'username' => $login->name,
            'expiresAt' => $login->expires_at->toIso8601String(),
        ];
    }
}
