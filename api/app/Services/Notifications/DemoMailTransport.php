<?php

namespace App\Services\Notifications;

use App\Models\MailServer;
use Illuminate\Support\Facades\Log;

/**
 * The demo mode and the tests: the panel writes each email to the log, and sends nothing. A
 * server with "fail" in its host name refuses each email, so that you can see a failure.
 */
class DemoMailTransport implements MailTransport
{
    /** @var list<array{server: string, to: string, subject: string, text: string}> */
    public array $sent = [];

    public function send(MailServer $server, string $to, string $subject, string $text, string $html): void
    {
        if (str_contains($server->host, 'fail')) {
            throw new MailFailed("Connection could not be established with host \"{$server->host}:{$server->port}\".");
        }
        $this->sent[] = ['server' => $server->host, 'to' => $to, 'subject' => $subject, 'text' => $text];
        Log::info("Demo email to {$to}: {$subject}");
    }
}
