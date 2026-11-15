<?php

namespace App\Http\Controllers\Api;

use App\Enums\Role;
use App\Http\Controllers\Controller;
use App\Models\Customer;
use App\Models\MailServer;
use App\Models\User;
use App\Services\Audit;
use App\Services\Notifications\MailFailed;
use App\Services\Notifications\MailTransport;
use App\Services\Notifications\Senders;
use App\Services\Notifications\SmtpHost;
use App\Services\Notifications\Templates;
use Illuminate\Http\JsonResponse;
use Illuminate\Http\Request;
use Illuminate\Validation\Rule;
use Illuminate\Validation\ValidationException;

/**
 * The SMTP server that sends the email notifications. An administrator sets the server of the
 * panel. A reseller can set its own server, so that its customers get emails from the address
 * and with the name of the reseller. With no server of its own, the emails to the customers of
 * a reseller go through the server of the panel, with the name of the reseller.
 */
class MailServerController extends Controller
{
    public function show(Request $request): JsonResponse
    {
        return response()->json(self::present(MailServer::of(self::scope($request)), self::scope($request)));
    }

    public function update(Request $request): JsonResponse
    {
        $scope = self::scope($request);
        $data = $request->validate([
            'enabled' => ['sometimes', 'boolean'],
            'host' => ['required', 'string', 'max:253', 'regex:'.SmtpHost::HOST],
            'port' => ['required', 'integer', Rule::in(SmtpHost::PORTS)],
            'encryption' => ['required', Rule::in(['tls', 'starttls', 'none'])],
            'username' => ['nullable', 'string', 'max:200'],
            // Leave it out to keep the password. An empty value removes it.
            'password' => ['sometimes', 'nullable', 'string', 'max:500'],
            'fromAddress' => ['required', 'email', 'max:200'],
            'fromName' => ['nullable', 'string', 'max:100', 'not_regex:/[\r\n]/'],
            'replyTo' => ['nullable', 'email', 'max:200'],
        ], [
            'port.in' => 'Use one of the SMTP ports: 25, 465, 587 or 2525.',
            'host.regex' => 'Type a host name, for example smtp.example.com.',
        ]);
        if ($scope && ($refusal = SmtpHost::refusalForReseller($data['host']))) {
            throw ValidationException::withMessages(['host' => $refusal]);
        }
        $server = MailServer::of($scope) ?? new MailServer(['customer_id' => $scope?->id]);
        $server->fill([
            'enabled' => $data['enabled'] ?? true,
            'host' => strtolower($data['host']),
            'port' => $data['port'],
            'encryption' => $data['encryption'],
            'username' => ($data['username'] ?? '') !== '' ? $data['username'] : null,
            'from_address' => $data['fromAddress'],
            'from_name' => ($data['fromName'] ?? '') !== '' ? $data['fromName'] : null,
            'reply_to' => ($data['replyTo'] ?? '') !== '' ? $data['replyTo'] : null,
        ]);
        if (array_key_exists('password', $data)) {
            $server->password = ($data['password'] ?? '') !== '' ? $data['password'] : null;
        }
        if (! $server->username) {
            $server->password = null;
        }
        // A changed server needs a new test.
        if ($server->isDirty(['host', 'port', 'encryption', 'username', 'password', 'from_address'])) {
            $server->tested_at = null;
            $server->test_error = null;
        }
        $server->save();
        Audit::record($request, 'Changed SMTP server', $server->host, $scope?->id);

        return response()->json(self::present($server, $scope));
    }

    public function destroy(Request $request): JsonResponse
    {
        $scope = self::scope($request);
        $server = MailServer::of($scope);
        if ($server) {
            $server->delete();
            Audit::record($request, 'Removed SMTP server', $server->host, $scope?->id);
        }

        return response()->json(null, 204);
    }

    /**
     * Sends a test email at once, and gives the answer of the server. An administrator can send
     * it to any address. A reseller sends it to its own address only, so that the panel does
     * not send emails for it to other people.
     */
    public function test(Request $request, MailTransport $transport): JsonResponse
    {
        $scope = self::scope($request);
        $user = $request->user();
        $data = $request->validate(['to' => ['nullable', 'email', 'max:200']]);
        $to = $user->isAdministrator() && ! empty($data['to']) ? $data['to'] : $user->email;
        $server = MailServer::of($scope);
        if (! $server) {
            return response()->json(['message' => 'Set up the SMTP server first.'], 409);
        }
        $brand = Senders::brand($scope);
        if (! $server->from_name) {
            $server->from_name = $brand['name'];
        }
        $text = "Hello {$user->name},\n\nThis is a test email from {$brand['name']}. The SMTP server {$server->host} sends the email notifications of the panel.\n\n{$brand['url']}";
        $error = null;
        try {
            if ($scope && ($refusal = SmtpHost::refusalForReseller($server->host))) {
                throw new MailFailed($refusal);
            }
            $transport->send($server, $to, "Test email from {$brand['name']}", $text, Templates::html($text, $brand));
        } catch (MailFailed $e) {
            $error = $e->getMessage();
        }
        $server->refresh()->update(['tested_at' => now(), 'test_error' => $error]);
        Audit::record($request, $error ? 'Test email failed' : 'Sent test email', $to, $scope?->id);

        return response()->json(['sent' => $error === null, 'to' => $to, 'error' => $error, 'server' => self::present($server, $scope)]);
    }

    /** The administrator: the panel (null). A reseller: its own customer. Others: no access. */
    public static function scope(Request $request): ?Customer
    {
        /** @var User $user */
        $user = $request->user();
        if ($user->isAdministrator()) {
            return null;
        }
        if ($user->role === Role::Reseller && $user->customer) {
            return $user->customer;
        }
        abort(response()->json(['message' => 'Only an administrator or a reseller can change the email settings.'], 403));
    }

    /** @return array<string, mixed> */
    private static function present(?MailServer $s, ?Customer $scope): array
    {
        $panel = $scope ? MailServer::of(null) : null;

        return [
            'configured' => $s !== null,
            'enabled' => (bool) $s?->enabled,
            'host' => $s?->host,
            'port' => $s?->port,
            'encryption' => $s?->encryption,
            'username' => $s?->username,
            // The panel never shows the password again.
            'hasPassword' => $s?->password !== null && $s?->password !== '',
            'fromAddress' => $s?->from_address,
            'fromName' => $s?->from_name,
            'replyTo' => $s?->reply_to,
            'testedAt' => $s?->tested_at?->toIso8601String(),
            'testError' => $s?->test_error,
            // For a reseller: the emails go through the server of the panel when it has none.
            'panelFallback' => $scope ? (bool) $panel?->enabled : null,
        ];
    }
}
