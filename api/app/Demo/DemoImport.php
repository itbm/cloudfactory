<?php

namespace App\Demo;

/**
 * The account that the demo agent reads from the archive of an import: an example of the
 * result of FetchImport (agent/internal/migrate). The demo agent does not read the archive.
 * The name of the file or the address selects the panel: a name with "directadmin" or a name
 * that starts with "user." is a DirectAdmin backup. Each other name is a cPanel backup.
 */
final class DemoImport
{
    /** @return array<string, mixed> */
    public static function account(string $name): array
    {
        $base = strtolower(basename((string) parse_url($name, PHP_URL_PATH)));
        $panel = str_contains($base, 'directadmin') || str_starts_with($base, 'user.') ? 'directadmin' : 'cpanel';
        $home = $panel === 'cpanel' ? 'cpmove-bakery/homedir' : 'domains';
        $docroot = fn (string $domain, string $dir) => $panel === 'cpanel' ? "{$home}/{$dir}" : "{$home}/{$domain}/public_html";
        $dump = fn (string $db) => $panel === 'cpanel' ? "cpmove-bakery/mysql/{$db}.sql" : "backup/{$db}.sql.gz";
        $maildir = fn (string $local) => $panel === 'cpanel' ? "{$home}/mail/bakeryclub.example.org/{$local}" : "imap/bakeryclub.example.org/{$local}/Maildir";

        return [
            'format' => 'cf-account/1',
            'source' => ['panel' => $panel, 'version' => $panel === 'cpanel' ? '11.130.0.15' : '1.680', 'account' => 'bakery'],
            'customer' => ['name' => 'Corner Bakery', 'email' => 'hello@cornerbakery.example.org'],
            'websites' => [
                self::website('bakeryclub.example.org', '8.1', $docroot('bakeryclub.example.org', 'public_html'), 1_240 * 1024 ** 2, ['orders'], [
                    ['name' => 'www.bakeryclub.example.org', 'kind' => 'alias'],
                    ['name' => 'bakery-club.example.net', 'kind' => 'redirect'],
                ], [['bakery_wp', 48 * 1024 ** 2]], $dump, 'bakery_wp', [['schedule' => '*/15 * * * *', 'command' => 'php /home/bakery/public_html/wp-cron.php']]),
                self::website('orders.bakeryclub.example.org', '7.4', $docroot('orders.bakeryclub.example.org', 'public_html/orders'), 220 * 1024 ** 2, [], [], [['bakery_orders', 12 * 1024 ** 2]], $dump),
                // The panel has a website with this name: the plan shows the conflict.
                self::website('cornerbakery.example.org', '8.2', $docroot('cornerbakery.example.org', 'cornerbakery'), 310 * 1024 ** 2, [], [], [], $dump),
            ],
            'dnsZones' => [
                ['name' => 'bakeryclub.example.org', 'zoneFile' => self::zone('bakeryclub.example.org')],
                // The customer has this zone: the import adds the records that it does not have.
                ['name' => 'cornerbakery.example.org', 'zoneFile' => self::zone('cornerbakery.example.org')],
            ],
            'mail' => ['domains' => [[
                'name' => 'bakeryclub.example.org',
                'mailboxes' => [
                    self::mailbox('hello', 1024 ** 3, 2_904, 512 * 1024 ** 2, $maildir),
                    self::mailbox('orders', 512 * 1024 ** 2, 811, 96 * 1024 ** 2, $maildir),
                    self::mailbox('bea', 0, 97, 32 * 1024 ** 2, $maildir),
                    // The old panel had a hash that the mail server cannot use: a new password.
                    ['localPart' => 'jobs', 'quotaBytes' => 256 * 1024 ** 2, 'passwordHash' => '', 'messages' => 0],
                ],
                'aliases' => [
                    ['localPart' => 'info', 'kind' => 'alias', 'destinations' => ['hello@bakeryclub.example.org']],
                    ['localPart' => 'owner', 'kind' => 'forwarder', 'destinations' => ['bea.baker@mail.example.com']],
                ],
            ]]],
            'warnings' => [
                ['item' => 'files', 'message' => 'The import copies the document root of each website. Copy other files of the home directory with SFTP.'],
                ['item' => 'certificates', 'message' => 'The import does not copy the TLS certificates. The panel gets a certificate for each domain when its DNS points to the new server.'],
                ['item' => 'ftp', 'message' => 'The panel has no FTP accounts. Use SFTP with an SSH key in place of FTP.'],
            ],
        ];
    }

    /**
     * @param  list<string>  $exclude
     * @param  list<array{name: string, kind: string}>  $domains
     * @param  list<array{0: string, 1: int}>  $databases
     * @param  list<array{schedule: string, command: string}>  $cron
     * @return array<string, mixed>
     */
    private static function website(string $name, string $php, string $path, int $bytes, array $exclude, array $domains, array $databases, callable $dump, ?string $wordpress = null, array $cron = []): array
    {
        return [
            'name' => $name,
            'kind' => 'public',
            'runtime' => ['family' => 'php', 'phpVersion' => $php, 'documentRoot' => '.'],
            'domains' => [['name' => $name, 'kind' => 'primary'], ...$domains],
            'cronJobs' => array_map(fn ($c) => $c + ['enabled' => true], $cron),
            'databases' => array_map(fn ($db) => ['engine' => 'mariadb', 'name' => $db[0], 'content' => ['path' => $dump($db[0]), 'bytes' => $db[1]]], $databases),
            'databaseUsers' => array_map(fn ($db) => ['engine' => 'mariadb', 'name' => $db[0], 'access' => 'website', 'grants' => [['database' => $db[0], 'privileges' => 'readwrite']]], $databases),
            'content' => ['files' => ['path' => $path, 'bytes' => $bytes, 'exclude' => $exclude]],
            'diskBytes' => $bytes,
            'appConfig' => $wordpress ? ['kind' => 'wordpress', 'file' => 'wp-config.php', 'database' => $wordpress] : null,
        ];
    }

    /** @return array<string, mixed> */
    private static function mailbox(string $local, int $quota, int $messages, int $bytes, callable $maildir): array
    {
        // A SHA-512-crypt hash with the form of a real hash. It is not the hash of a password.
        $hash = '$6$'.substr(md5($local), 0, 16).'$'.substr(strtr(base64_encode(hash('sha512', $local, true)), '+', '.'), 0, 86);

        return ['localPart' => $local, 'quotaBytes' => $quota, 'passwordHash' => $hash, 'content' => ['path' => $maildir($local), 'bytes' => $bytes], 'messages' => $messages];
    }

    private static function zone(string $name): string
    {
        return <<<ZONE
            \$ORIGIN {$name}.
            \$TTL 14400
            @	86400	IN	SOA	ns1.oldhost.example. hostmaster.oldhost.example. 2026110401 3600 1800 1209600 86400
            @	86400	IN	NS	ns1.oldhost.example.
            @	86400	IN	NS	ns2.oldhost.example.
            @	IN	A	198.51.100.24
            www	IN	CNAME	{$name}.
            @	IN	MX	0 {$name}.
            @	IN	TXT	"v=spf1 +a +mx ~all"
            _dmarc	IN	TXT	"v=DMARC1; p=none"
            google._domainkey	IN	TXT	"v=DKIM1; k=rsa; p=MIGfMA0GCSqGSIb3DQEBAQUAA4GNADCBiQKBgQC3demo"
            cpanel	IN	A	198.51.100.24
            webmail	IN	A	198.51.100.24

            ZONE;
    }
}
