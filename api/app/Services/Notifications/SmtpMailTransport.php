<?php

namespace App\Services\Notifications;

use App\Models\MailServer;
use Symfony\Component\Mailer\Exception\TransportExceptionInterface;
use Symfony\Component\Mailer\Mailer;
use Symfony\Component\Mailer\Transport\Smtp\EsmtpTransport;
use Symfony\Component\Mailer\Transport\Smtp\Stream\SocketStream;
use Symfony\Component\Mime\Address;
use Symfony\Component\Mime\Email;
use Throwable;

/** Sends with the SMTP server of the panel or of a reseller. */
class SmtpMailTransport implements MailTransport
{
    public function send(MailServer $server, string $to, string $subject, string $text, string $html): void
    {
        // "tls": TLS from the start. "starttls": the server must give STARTTLS. "none": no TLS.
        // The server of a reseller: the panel connects to the address that it checked. The
        // TLS certificate is for the host name.
        $address = null;
        if ($server->customer_id) {
            [$address, $refusal] = SmtpHost::checked($server->host);
            if ($refusal !== null) {
                throw new MailFailed($refusal);
            }
        }
        try {
            $this->deliver($server, $address, $to, $subject, $text, $html);
        } catch (MailFailed $e) {
            throw $e;
        } catch (TransportExceptionInterface $e) {
            throw new MailFailed(self::reason($e->getMessage()), 0, $e);
        } catch (Throwable $e) {
            // Adversarial review, finding A8-06: for example an address that the mail library
            // refuses. The delivery fails with a reason, and does not stay in the queue.
            throw new MailFailed(self::reason('The panel could not make the email: '.$e->getMessage()), 0, $e);
        }
    }

    private function deliver(MailServer $server, ?string $address, string $to, string $subject, string $text, string $html): void
    {
        $transport = new EsmtpTransport($address ?? $server->host, $server->port, $server->encryption === 'tls');
        $stream = $transport->getStream();
        if ($stream instanceof SocketStream) {
            $stream->setTimeout(15);
            if ($address !== null && $address !== $server->host) {
                $stream->setStreamOptions(['ssl' => ['peer_name' => $server->host, 'SNI_enabled' => true]]);
            }
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
        (new Mailer($transport))->send($email);
    }

    /** The first line of the error, with no password in it. */
    private static function reason(string $message): string
    {
        $line = trim(strtok($message, "\n") ?: $message);

        return mb_substr($line !== '' ? $line : 'The SMTP server did not accept the email.', 0, 500);
    }
}
