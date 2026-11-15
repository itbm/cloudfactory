<?php

namespace App\Services\Notifications;

use App\Models\MailServer;
use Symfony\Component\Mailer\Exception\TransportExceptionInterface;
use Symfony\Component\Mailer\Mailer;
use Symfony\Component\Mailer\Transport\Smtp\EsmtpTransport;
use Symfony\Component\Mailer\Transport\Smtp\Stream\SocketStream;
use Symfony\Component\Mime\Address;
use Symfony\Component\Mime\Email;

/** Sends with the SMTP server of the panel or of a reseller. */
class SmtpMailTransport implements MailTransport
{
    public function send(MailServer $server, string $to, string $subject, string $text, string $html): void
    {
        // "tls": TLS from the start. "starttls": the server must give STARTTLS. "none": no TLS.
        $transport = new EsmtpTransport($server->host, $server->port, $server->encryption === 'tls');
        $stream = $transport->getStream();
        if ($stream instanceof SocketStream) {
            $stream->setTimeout(15);
        }
        if ($server->encryption === 'starttls') {
            $transport->setRequireTls(true);
        } elseif ($server->encryption === 'none') {
            $transport->setAutoTls(false);
        }
        if ($server->username) {
            $transport->setUsername($server->username);
            $transport->setPassword((string) $server->password);
        }
        $email = (new Email)
            ->from(new Address($server->from_address, (string) $server->from_name))
            ->to($to)
            ->subject($subject)
            ->text($text)
            ->html($html);
        if ($server->reply_to) {
            $email->replyTo($server->reply_to);
        }
        try {
            (new Mailer($transport))->send($email);
        } catch (TransportExceptionInterface $e) {
            throw new MailFailed(self::reason($e->getMessage()), 0, $e);
        }
    }

    /** The first line of the error, with no password in it. */
    private static function reason(string $message): string
    {
        $line = trim(strtok($message, "\n") ?: $message);

        return mb_substr($line !== '' ? $line : 'The SMTP server did not accept the email.', 0, 500);
    }
}
