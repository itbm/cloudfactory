<?php

namespace App\Http\Controllers\Api;

use App\Enums\AgentAction;
use App\Http\Controllers\Controller;
use App\Http\Resources\JobResource;
use App\Http\Resources\WebsiteResource;
use App\Services\Audit;
use App\Services\JobDispatcher;
use Illuminate\Http\JsonResponse;
use Illuminate\Http\Request;
use Illuminate\Support\Str;
use Illuminate\Validation\ValidationException;

/**
 * Spec section 10: SFTP access to the files of a website. The owner signs in as the Unix user
 * of the website, with a password or an SSH key. The server allows only SFTP, in the directory
 * of the website.
 */
class SftpController extends Controller
{
    use Concerns;

    /** The same format as validate.SSHKey in the agent: one public key with no options. */
    public const SSH_KEY = '#^(ssh-ed25519|ssh-rsa|ecdsa-sha2-nistp256|ecdsa-sha2-nistp384|ecdsa-sha2-nistp521|sk-ssh-ed25519@openssh\.com|sk-ecdsa-sha2-nistp256@openssh\.com) [A-Za-z0-9+/]+={0,3}( [ -~]{0,200})?$#';

    public function __construct(private JobDispatcher $jobs) {}

    public function update(Request $request, string $websiteId): JsonResponse
    {
        $website = $this->findWebsite($request, $websiteId);
        $data = $request->validate([
            'enabled' => ['required', 'boolean'],
            // True: the panel makes a new password and shows it once.
            'newPassword' => ['sometimes', 'boolean'],
            // True: only the SSH keys can sign in.
            'removePassword' => ['sometimes', 'boolean'],
            'authorizedKeys' => ['sometimes', 'array', 'max:20'],
            'authorizedKeys.*' => ['string', 'max:8192', 'regex:'.self::SSH_KEY],
        ]);
        if (($data['newPassword'] ?? false) && ($data['removePassword'] ?? false)) {
            throw ValidationException::withMessages(['newPassword' => 'Make a new password or remove the password, not both.']);
        }
        $keys = array_values(array_unique(array_map('trim', $data['authorizedKeys'] ?? $website->sftp_keys ?? [])));
        $password = $data['enabled'] && ($data['newPassword'] ?? false) ? Str::password(24, symbols: false) : null;
        $job = $this->jobs->dispatch($website->server, AgentAction::SetSftp, array_filter([
            'websiteId' => $website->id,
            'unixUser' => $website->unix_user,
            'enabled' => $data['enabled'],
            // The agent gets the password over mutual TLS. The panel removes it when the job ends.
            'password' => $password,
            'setsPassword' => $password !== null,
            'removePassword' => $data['removePassword'] ?? false,
            'authorizedKeys' => $keys,
        ], fn ($v) => $v !== null), $data['enabled'] ? 'Set SFTP access' : 'Turn off SFTP access', $website);
        Audit::record($request, $data['enabled'] ? 'Changed SFTP access' : 'Turned off SFTP access', $website->name, $website->customer_id);

        return response()->json(['sftp' => (new WebsiteResource($website))->toArray($request)['sftp'], 'password' => $password, 'job' => new JobResource($job)], 202);
    }
}
