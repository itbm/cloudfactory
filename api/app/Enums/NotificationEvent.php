<?php

namespace App\Enums;

/**
 * The events that can give an email. Each event has a default template, the roles that can
 * receive it, and whether a new user receives it before they change their preferences.
 */
enum NotificationEvent: string
{
    case JobFailed = 'job.failed';
    case BackupFailed = 'backup.failed';
    case CertificateExpiring = 'certificate.expiring';
    case AccountSuspended = 'account.suspended';
    case AccountRestored = 'account.restored';
    case NewDeviceSignIn = 'signin.new_device';
    case UsageAlert = 'usage.alert';
    case ServerOffline = 'server.offline';
    case UptimeDown = 'uptime.down';
    case UptimeUp = 'uptime.up';
    case PasswordReset = 'password.reset';

    public function label(): string
    {
        return match ($this) {
            self::JobFailed => 'Failed jobs',
            self::BackupFailed => 'Failed backups',
            self::CertificateExpiring => 'Certificates that expire soon',
            self::AccountSuspended => 'Account suspended',
            self::AccountRestored => 'Suspension lifted',
            self::NewDeviceSignIn => 'Sign-in from a new device',
            self::UsageAlert => 'Usage alerts',
            self::ServerOffline => 'Servers that are offline',
            self::UptimeDown => 'Websites that are down',
            self::UptimeUp => 'Websites that are up again',
            self::PasswordReset => 'Password reset',
        };
    }

    public function description(): string
    {
        return match ($this) {
            self::JobFailed => 'A change to a website, a database or a server did not complete.',
            self::BackupFailed => 'A backup of a website did not complete.',
            self::CertificateExpiring => 'The TLS certificate of a domain expires in 14 days or less, or the panel could not get one.',
            self::AccountSuspended => 'The account is suspended, and its websites are stopped.',
            self::AccountRestored => 'The suspension of the account is lifted.',
            self::NewDeviceSignIn => 'Someone signed in to your user from a device or browser that the panel does not know.',
            self::UsageAlert => 'A website or the account uses almost all of a limit of its plan.',
            self::ServerOffline => 'A server did not report to the panel for 5 minutes.',
            self::UptimeDown => 'An uptime check of a website failed a number of times in a row.',
            self::UptimeUp => 'A website that was down answers its uptime check again.',
            self::PasswordReset => 'Someone asked for a link to set a new password for your user.',
        };
    }

    /** @return list<Role> */
    public function roles(): array
    {
        return match ($this) {
            self::ServerOffline => [Role::Administrator],
            self::AccountSuspended, self::AccountRestored => [Role::Reseller, Role::Owner],
            default => [Role::Administrator, Role::Reseller, Role::Owner],
        };
    }

    public function availableTo(Role $role): bool
    {
        return in_array($role, $this->roles(), true);
    }

    /**
     * A user can turn off each event, except the security events. A user must know about a
     * suspension of the account and about a sign-in that they did not make.
     */
    public function required(): bool
    {
        return in_array($this, [self::AccountSuspended, self::NewDeviceSignIn, self::PasswordReset], true);
    }

    /**
     * The words that each template of the event must have. An email to reset a password with
     * no link is of no use.
     *
     * @return list<string>
     */
    public function requiredVariables(): array
    {
        return match ($this) {
            self::PasswordReset => ['resetUrl'],
            default => [],
        };
    }

    /**
     * The words whose values are secret. The panel keeps a copy of each email (the "Sent
     * emails" list), and an administrator or a reseller can read it. The copy has a placeholder
     * in place of a secret value. Only the email that the SMTP server gets has the value.
     *
     * @return list<string>
     */
    public function secretVariables(): array
    {
        return $this->requiredVariables();
    }

    /**
     * A new user receives each event that their role can receive, until they turn it off. An
     * administrator can see the uptime checks of all customers, so the uptime emails are off
     * for an administrator until they turn them on.
     */
    public function defaultOn(Role $role): bool
    {
        return ! ($role === Role::Administrator && in_array($this, [self::UptimeDown, self::UptimeUp], true));
    }

    /**
     * The words in a template that the panel changes to a value, with an example value for
     * the preview.
     *
     * @return array<string, string>
     */
    public function variables(): array
    {
        $common = ['name' => 'Alex Smith', 'brand' => 'Cloudfactory', 'panelUrl' => 'https://panel.example.com'];

        return $common + match ($this) {
            self::JobFailed => ['job' => 'Create database shop_main', 'website' => 'shop.example.com', 'server' => 'web-1', 'error' => 'The database server did not answer.', 'time' => '15 Nov 2026 03:12 UTC'],
            self::BackupFailed => ['website' => 'shop.example.com', 'server' => 'web-1', 'error' => 'The object storage refused the key.', 'time' => '15 Nov 2026 03:00 UTC'],
            self::CertificateExpiring => ['domain' => 'shop.example.com', 'website' => 'shop.example.com', 'expiresAt' => '29 Nov 2026', 'days' => '14', 'state' => 'valid', 'advice' => self::certificateAdvice('shop.example.com', 'automatic')],
            self::AccountSuspended => ['account' => 'Acme Ltd', 'provider' => 'Cloudfactory', 'supportEmail' => 'support@example.com'],
            self::AccountRestored => ['account' => 'Acme Ltd', 'provider' => 'Cloudfactory'],
            self::NewDeviceSignIn => ['device' => 'Firefox on Linux', 'ip' => '203.0.113.7', 'time' => '15 Nov 2026 09:41 UTC'],
            self::UsageAlert => ['account' => 'Acme Ltd', 'resource' => 'Disk', 'website' => 'shop.example.com', 'used' => '9.1 GB', 'limit' => '10 GB', 'percent' => '91', 'level' => 'warning'],
            self::ServerOffline => ['server' => 'web-2', 'lastSeen' => '15 Nov 2026 02:55 UTC'],
            self::UptimeDown => ['website' => 'shop.example.com', 'url' => 'https://shop.example.com/', 'error' => 'HTTP status 503.', 'since' => '15 Nov 2026 03:12 UTC'],
            self::UptimeUp => ['website' => 'shop.example.com', 'url' => 'https://shop.example.com/', 'downtime' => '14 minutes', 'since' => '15 Nov 2026 03:12 UTC', 'until' => '15 Nov 2026 03:26 UTC'],
            self::PasswordReset => ['resetUrl' => 'https://panel.example.com/reset-password#token=4f1c…', 'minutes' => '60', 'ip' => '203.0.113.7'],
        };
    }

    /**
     * The value {{advice}} of the event certificate.expiring: what to do for the source of the
     * certificate that the domain uses (automatic, acme or upload).
     */
    public static function certificateAdvice(string $domain, string $source): string
    {
        return match ($source) {
            'upload' => 'You uploaded this certificate to the panel, so the panel cannot renew it. Get a new certificate from your certificate authority. Then open the certificate on the Certificates page of the panel and select "Replace":',
            'acme' => 'The panel renews a certificate with DNS validation when two thirds of its time is gone, but it could not renew this certificate. Make sure that the name servers of the domain are the name servers of the panel. Then open the certificate on the Certificates page of the panel and select "Renew now":',
            default => "The panel renews a certificate 30 days before it expires, but it could not renew this certificate. Make sure that the DNS record of {$domain} points to the server of the website. Then open the domains of the website in the panel and select \"Check DNS\":",
        };
    }

    public function defaultSubject(): string
    {
        return match ($this) {
            self::JobFailed => 'Failed: {{job}}',
            self::BackupFailed => 'The backup of {{website}} failed',
            self::CertificateExpiring => 'Check the certificate of {{domain}}',
            self::AccountSuspended => 'Your account {{account}} is suspended',
            self::AccountRestored => 'Your account {{account}} is active again',
            self::NewDeviceSignIn => 'New sign-in to {{brand}}',
            self::UsageAlert => '{{account}} uses {{percent}}% of its {{resource}} limit',
            self::ServerOffline => 'The server {{server}} is offline',
            self::UptimeDown => '{{website}} is down',
            self::UptimeUp => '{{website}} is up again',
            self::PasswordReset => 'Set a new password for {{brand}}',
        };
    }

    public function defaultBody(): string
    {
        return match ($this) {
            self::JobFailed => "Hello {{name}},\n\nThis job did not complete:\n\n{{job}}\nWebsite: {{website}}\nServer: {{server}}\nTime: {{time}}\n\nThe error: {{error}}\n\nOpen the panel to see the steps of the job, and to try again:\n{{panelUrl}}/jobs",
            self::BackupFailed => "Hello {{name}},\n\nThe backup of {{website}} did not complete at {{time}}.\n\nThe error: {{error}}\n\nThe earlier backups are not changed. Open the panel to make a backup now:\n{{panelUrl}}/backups",
            self::CertificateExpiring => "Hello {{name}},\n\nThe TLS certificate of {{domain}} (website {{website}}) needs your attention.\n\nExpiry: {{expiresAt}} ({{days}} days)\n\nWhen the certificate expires, browsers show a warning for the website.\n\n{{advice}}\n{{panelUrl}}",
            self::AccountSuspended => "Hello {{name}},\n\nYour account {{account}} is suspended. Your websites are stopped, and SFTP access is off. Your files, databases and backups are not deleted.\n\nTo lift the suspension, speak to {{provider}}: {{supportEmail}}",
            self::AccountRestored => "Hello {{name}},\n\nThe suspension of your account {{account}} is lifted. The panel starts your websites again, and turns SFTP access on again.\n\n{{panelUrl}}",
            self::NewDeviceSignIn => "Hello {{name}},\n\nSomeone signed in to your user on {{brand}} from a new device.\n\nDevice: {{device}}\nIP address: {{ip}}\nTime: {{time}}\n\nIf this was you, you need not do anything. If not, change your password now, and turn on the authenticator app:\n{{panelUrl}}/settings",
            self::UsageAlert => "Hello {{name}},\n\n{{account}} uses {{used}} of {{limit}} ({{percent}}%) of its {{resource}} limit. Website: {{website}}\n\nWhen it uses all of the limit, changes can fail. Remove data that you do not need, or ask for a larger plan:\n{{panelUrl}}/account",
            self::ServerOffline => "Hello {{name}},\n\nThe server {{server}} did not report to the panel since {{lastSeen}}. The panel keeps its jobs until it reports again.\n\nMake sure that the server is on and that the agent service runs:\n{{panelUrl}}/servers",
            self::UptimeDown => "Hello {{name}},\n\nThe uptime check of {{website}} fails since {{since}}.\n\nAddress: {{url}}\nError: {{error}}\n\nThe panel checks the address again, and sends an email when the website answers again. Open the panel to see the checks:\n{{panelUrl}}/uptime",
            self::UptimeUp => "Hello {{name}},\n\n{{website}} answers its uptime check again. It was down for {{downtime}}, from {{since}} to {{until}}.\n\nAddress: {{url}}\n\nOpen the panel to see the incidents:\n{{panelUrl}}/uptime",
            self::PasswordReset => "Hello {{name}},\n\nSomeone asked for a link to set a new password for your user on {{brand}}. The request came from the IP address {{ip}}.\n\nTo set a new password, open this link. It works one time only, for {{minutes}} minutes:\n{{resetUrl}}\n\nA new password does not turn off your authenticator app or remove your passkeys.\n\nIf you did not ask for this link, you need not do anything. Your password does not change.",
        };
    }
}
