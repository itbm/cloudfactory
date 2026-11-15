<?php
// SPDX-License-Identifier: AGPL-3.0-only
//
// Adminer for the customers of a Cloudfactory panel.
//
// - The panel signs the user in. It makes a temporary database user for one database, and the
//   web panel sends the sign-in form of Adminer with that user and its password. The user types
//   no password. This file shows no sign-in form.
// - Adminer opens only MariaDB and PostgreSQL databases, on a name of a database server of the
//   customer ("<name>.internal" or the name of an engine, with a port). The firewall of the
//   server lets this container connect only to the database servers of its customer. This
//   check gives a clear message before the connection.
// - Adminer shows only the databases of the sign-in: the temporary database user has access to
//   those databases only.
// - Adminer does not ask adminer.org for a new version. The image has a fixed version, and the
//   browser of the user does not connect to a different site.

// Each sign-in has its own address, /<tool>/<random key>/, from the panel. Adminer keeps its
// session cookie on the path of the request. Thus a page on a different address of this domain,
// for example the Adminer of a different customer, does not get the cookie of this sign-in and
// cannot find the address. Adminer serves only this one path for each sign-in: its links change
// only the query.
if (! preg_match('~^/[a-z0-9]+/[0-9a-f]{32}/(\?|$)~', $_SERVER['REQUEST_URI'] ?? '')) {
    http_response_code(404);
    header('Content-Type: text/html; charset=utf-8');
    header('Cache-Control: no-store');
    header("Content-Security-Policy: default-src 'none'; style-src 'unsafe-inline'; frame-ancestors 'none'");
    echo '<!doctype html><html lang="en-GB"><meta charset="utf-8"><title>Adminer</title>';
    echo '<body style="font-family: system-ui, sans-serif; max-width: 40rem; margin: 4rem auto; padding: 0 1rem">';
    echo '<h1>Adminer</h1><p>To open a database, sign in to your hosting panel. Open <b>Databases</b>, and select <b>Open in Adminer</b> in the menu of the database.</p></body></html>';
    exit;
}

// Adminer asks adminer.org for its newest version when this cookie has no value. The value 0
// stops the request and shows no version notice.
$_COOKIE['adminer_version'] = '0';

function adminer_object()
{
    return new class extends Adminer\Adminer
    {
        /** The Adminer drivers for MariaDB and PostgreSQL. */
        private const DRIVERS = ['server', 'pgsql'];

        /** The error for a driver or a server that this Adminer does not open, or null. */
        private static function refusal(): ?string
        {
            if (! in_array(Adminer\DRIVER, self::DRIVERS, true)) {
                return 'This Adminer opens only MariaDB and PostgreSQL databases.';
            }
            if (! preg_match('~^[a-z0-9]([a-z0-9-]*[a-z0-9])?\.internal(:[0-9]{1,5})?$~', Adminer\SERVER)) {
                return 'This Adminer opens only the database servers of your account. Open the database from your hosting panel.';
            }

            return null;
        }

        public function credentials()
        {
            // Adminer connects before it calls login(). Thus the check is here, before the
            // connection.
            $refusal = self::refusal();
            if ($refusal !== null) {
                http_response_code(403);
                header('Content-Type: text/html; charset=utf-8');
                header('Cache-Control: no-store');
                echo '<!doctype html><html lang="en-GB"><meta charset="utf-8"><title>Adminer</title>';
                echo '<body style="font-family: system-ui, sans-serif; max-width: 40rem; margin: 4rem auto; padding: 0 1rem">';
                echo '<h1>Adminer</h1><p>'.htmlspecialchars($refusal).'</p></body></html>';
                exit;
            }

            return parent::credentials();
        }

        public function login($login, $password)
        {
            return self::refusal() ?? ($password !== '' ? true : 'Open the database from your hosting panel.');
        }

        public function loginForm()
        {
            echo '<p>To open a database, sign in to your hosting panel. Open <b>Databases</b>, and select <b>Open in Adminer</b> in the menu of the database.</p>';
            echo '<p>The panel signs you in with a temporary database user. You do not type a password here. The temporary user stops after a short time: then open the database from the panel again.</p>';
        }

        public function permanentLogin($create = false)
        {
            // No "permanent login": a temporary database user expires.
            return '';
        }

        public function databases($flush = true)
        {
            // The databases of the sign-in. Adminer records them in the session.
            $signedIn = $_SESSION['db'][Adminer\DRIVER][Adminer\SERVER][$_GET['username'] ?? ''] ?? [];

            return array_values(array_intersect(parent::databases($flush), array_map('strval', array_keys($signedIn))));
        }

        public function headers()
        {
            header('Referrer-Policy: no-referrer');
            header('X-Frame-Options: DENY');
            header('Strict-Transport-Security: max-age=31536000');
            header('Cache-Control: no-store');
        }

        public function csp(array $csp)
        {
            // No page of a different site can show Adminer in a frame.
            // The browser connects only to Adminer.
            foreach ($csp as &$policy) {
                $policy['frame-ancestors'] = "'none'";
                $policy['connect-src'] = "'self'";
                $policy['worker-src'] = "'none'";
            }

            return $csp;
        }
    };
}

require __DIR__.'/adminer.php';
