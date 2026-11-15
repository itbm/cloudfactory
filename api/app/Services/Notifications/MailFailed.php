<?php

namespace App\Services\Notifications;

use RuntimeException;

/** The SMTP server did not accept an email. The message is safe to show to the user. */
class MailFailed extends RuntimeException {}
