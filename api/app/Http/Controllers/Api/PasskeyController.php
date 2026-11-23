<?php

namespace App\Http\Controllers\Api;

use App\Http\Controllers\Controller;
use App\Models\Passkey;
use App\Services\Audit;
use App\Services\Branding;
use App\Services\Passkeys;
use Illuminate\Http\JsonResponse;
use Illuminate\Http\Request;

/**
 * FR-103: passkeys. A user adds a passkey in the settings, and then signs in with it in place
 * of the password and the TOTP code.
 *
 * Only a sign-in session can add, rename or remove a passkey, as for TOTP. To add or remove a
 * passkey, the user gives the password and, when TOTP is on, a code: a passkey is a full
 * sign-in, so a person with a stolen session alone must not add one.
 */
class PasskeyController extends Controller
{
    public function __construct(private Passkeys $passkeys) {}

    /** The passkeys of the user. An API token can read the list, but it cannot change it. */
    public function index(Request $request): JsonResponse
    {
        $user = $request->user();

        return response()->json(['data' => $user->passkeys()->orderBy('created_at')->get()->map(fn (Passkey $p) => self::present($p))]);
    }

    /** The first step to add a passkey: the options for the browser. */
    public function options(Request $request): JsonResponse
    {
        $user = AuthController::sessionUser($request);
        $rpId = self::rpId($request);
        AuthController::confirmIdentity($request);

        return response()->json($this->passkeys->startCreation($user, $rpId, Branding::forHost($rpId)['name']));
    }

    /** The second step to add a passkey: the answer of the browser. */
    public function store(Request $request): JsonResponse
    {
        $user = AuthController::sessionUser($request);
        $data = $request->validate([
            'ceremony' => ['required', 'string', 'max:100'],
            'name' => ['required', 'string', 'max:100'],
            'credential' => ['required', 'array'],
        ]);
        $passkey = $this->passkeys->finishCreation($user, $data['ceremony'], $data['credential'], trim($data['name']));
        Audit::record($request, 'Added passkey', "{$passkey->name} ({$user->email})");

        return response()->json(self::present($passkey), 201);
    }

    public function update(Request $request, string $id): JsonResponse
    {
        $user = AuthController::sessionUser($request);
        $data = $request->validate(['name' => ['required', 'string', 'max:100']]);
        $passkey = $user->passkeys()->findOrFail($id);
        $passkey->update(['name' => trim($data['name'])]);
        Audit::record($request, 'Renamed passkey', "{$passkey->name} ({$user->email})");

        return response()->json(self::present($passkey));
    }

    /** Removes a passkey. The user gives the password and, when TOTP is on, a code. */
    public function destroy(Request $request, string $id): JsonResponse
    {
        $user = AuthController::sessionUser($request);
        $passkey = $user->passkeys()->findOrFail($id);
        AuthController::confirmIdentity($request);
        $passkey->delete();
        Audit::record($request, 'Removed passkey', "{$passkey->name} ({$user->email})");

        return response()->json(null, 204);
    }

    /** The first step to sign in with a passkey. A person who has not signed in can use it. */
    public function signInOptions(Request $request): JsonResponse
    {
        return response()->json($this->passkeys->startSignIn(self::rpId($request)));
    }

    /**
     * The second step to sign in with a passkey. The answer has a session token, as for a
     * sign-in with a password. The panel does not ask for a TOTP code: the passkey verified the
     * user (a fingerprint, a face or a PIN).
     */
    public function signIn(Request $request): JsonResponse
    {
        $data = $request->validate([
            'ceremony' => ['required', 'string', 'max:100'],
            'credential' => ['required', 'array'],
            'deviceId' => ['nullable', 'string', 'max:100'],
        ]);
        $passkey = $this->passkeys->finishSignIn($data['ceremony'], $data['credential']);
        $user = $passkey->user;
        // Nobody signs in as the user of a plugin.
        if ($user->isPlugin()) {
            return response()->json(['message' => 'This user cannot sign in.'], 403);
        }
        // The passkey shows who the person is, so the answer can give the reason.
        if ($user->isSuspended()) {
            return response()->json(['message' => 'This account is suspended. Contact your provider.'], 403);
        }

        return AuthController::startSession($request, $user, 'Signed in with passkey');
    }

    /** The panel domain of the request. A passkey works only on a panel domain. */
    private static function rpId(Request $request): string
    {
        $rpId = Passkeys::rpIdFor($request->getHost());
        abort_if($rpId === null, 409, 'Passkeys work only on the domain of the panel. Open the panel at its own domain.');

        return $rpId;
    }

    /** @return array<string, mixed> */
    private static function present(Passkey $p): array
    {
        return [
            'id' => $p->id,
            'name' => $p->name,
            'domain' => $p->rp_id,
            // The passkey is in a password manager or a cloud account, so it is on more than one device.
            'synced' => $p->backed_up,
            'createdAt' => $p->created_at?->toIso8601String(),
            'lastUsedAt' => $p->last_used_at?->toIso8601String(),
        ];
    }
}
