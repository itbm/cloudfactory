<?php

namespace App\Services\Sso;

use RuntimeException;

/**
 * A sign-in with an identity provider did not work. The reason is a short code for the web
 * panel ("expired", "failed", ...). The message is for the log of the panel, not for the user.
 */
class SsoException extends RuntimeException
{
    public function __construct(public string $reason, string $message = '')
    {
        parent::__construct($message !== '' ? $message : $reason);
    }
}
