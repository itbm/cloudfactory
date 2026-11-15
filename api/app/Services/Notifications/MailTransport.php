<?php

namespace App\Services\Notifications;

use App\Models\MailServer;

/**
 * Sends one email through one SMTP server. The demo mode and the tests use a different
 * implementation (DemoMailTransport), so that no email leaves the computer.
 */
interface MailTransport
{
    /** @throws MailFailed when the server does not accept the email. */
    public function send(MailServer $server, string $to, string $subject, string $text, string $html): void;
}
