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
use Illuminate\Validation\ValidationException;

/**
 * Spec section 10: SFTP access to the files of a website. The owner signs in as the Unix user
 * of the website with an SSH key. The server refuses passwords, and allows only SFTP, in the
 * directory of the website.
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
            // SFTP accepts SSH keys only. An old client that asks for a password gets an error.
            'newPassword' => ['prohibited'],
            'removePassword' => ['prohibited'],
            'authorizedKeys' => ['sometimes', 'array', 'max:20'],
            'authorizedKeys.*' => ['string', 'max:8192', 'regex:'.self::SSH_KEY],
        ], [
            'newPassword.prohibited' => 'SFTP accepts SSH keys only. Add a public key.',
            'removePassword.prohibited' => 'SFTP accepts SSH keys only. Add a public key.',
        ]);
        $keys = array_values(array_unique(array_map('trim', $data['authorizedKeys'] ?? $website->sftp_keys ?? [])));
        if ($data['enabled'] && $keys === []) {
            throw ValidationException::withMessages(['authorizedKeys' => 'Add an SSH public key to turn on SFTP.']);
        }
        $job = $this->jobs->dispatch($website->server, AgentAction::SetSftp, [
            'websiteId' => $website->id,
            'unixUser' => $website->unix_user,
            'enabled' => $data['enabled'],
            'authorizedKeys' => $keys,
        ], $data['enabled'] ? 'Set SFTP access' : 'Turn off SFTP access', $website);
        Audit::record($request, $data['enabled'] ? 'Changed SFTP access' : 'Turned off SFTP access', $website->name, $website->customer_id);

        return response()->json(['sftp' => (new WebsiteResource($website))->toArray($request)['sftp'], 'job' => new JobResource($job)], 202);
    }
}
