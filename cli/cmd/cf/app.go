package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/itbm/cloudfactory/cli/internal/client"
)

const usage = `cf controls a Cloudfactory panel.

Usage:
  cf login --panel URL --email EMAIL [--totp CODE]     Sign in. The password is read from CF_PASSWORD or stdin.
  cf logout                                            Sign out. The panel deletes the session.
  cf use --panel URL --token TOKEN                     Use an API token.
  cf websites list [--status STATUS]
  cf websites get WEBSITE
  cf websites create NAME --image ID --plan ID [--customer ID] [--server ID] [--app NAME]
                     [--kind public|private|worker] [--command COMMAND] [--install COMMAND]
                                                       A Node.js or Python image needs no --command: the panel uses the
                                                       usual start command and install command of the image.
  cf websites set WEBSITE [--plan ID] [--app NAME] [--vcpus N] [--memory-mb N] [--disk-gb N] [--io-mb N] [--processes N]
  cf websites delete WEBSITE
  cf websites start|stop|restart|staging WEBSITE      staging shows the passwords of the new database users once.
  cf websites retry WEBSITE                            Make a website again after the first job failed.
  cf websites move WEBSITE --server SERVER
                                                       Move a website to a different server. Only an administrator. It keeps
                                                       its domains, its databases and its network name. It does not operate
                                                       while its files move through the tunnel between the two servers.
  cf domains add WEBSITE DOMAIN --kind redirect|alias  A new domain needs a DNS TXT record. The command shows it.
  cf domains verify WEBSITE DOMAIN                     Check the TXT record. Then the edge proxy serves the domain.
  cf domains remove WEBSITE DOMAIN
  cf domains primary WEBSITE DOMAIN                    Make a verified domain the primary domain.
  cf domains check WEBSITE DOMAIN                      Check the DNS of the domain again.
  cf runtime set WEBSITE [--image ID] [--worker-mode on|off] [--extensions a,b] [--ini key=value]...
                         [--document-root DIR] [--command COMMAND] [--install COMMAND]
                                                       --command: the start command of a worker or of a Node.js or
                                                       Python website. --install "": no install step.
  cf runtime install WEBSITE                           Run the install command, for example npm ci, then start the
                                                       Node.js or Python website again with no stop.
  cf env list WEBSITE                                  The names of the environment variables. The panel does not show values.
  cf env set WEBSITE NAME                              The value is read from CF_VALUE or stdin. The website starts again with no stop.
  cf env unset WEBSITE NAME...
  cf sftp get WEBSITE
  cf sftp set WEBSITE [--on|--off] [--key-file FILE]... [--no-keys]
  cf runtime revert WEBSITE
  cf files list WEBSITE [PATH]                         The file manager. A PATH starts at the directory of the website,
                                                       for example /public/index.php. Use SFTP for a file of more than 5 MB.
  cf files get WEBSITE PATH [--output FILE]
  cf files put WEBSITE PATH --from FILE [--create-only]   "--from -" reads the standard input.
  cf files mkdir WEBSITE PATH
  cf files move WEBSITE PATH NEW_PATH
  cf files delete WEBSITE PATH                         A directory goes with all that it contains.
  cf files chmod WEBSITE PATH MODE [--recursive --file-mode MODE]
                                                       MODE has 3 octal digits, for example 644. With --recursive, MODE is
                                                       for the directories and --file-mode for the files. Links do not change.
  cf files archive WEBSITE NEW_ARCHIVE PATH...         Make a .zip, .tar.gz or .tar archive of the files and directories.
  cf files extract WEBSITE ARCHIVE [--to DIR] [--overwrite]
                                                       Extract a .zip, .tar, .tar.gz or .tar.bz2 archive. The default DIR is the
                                                       directory of the archive. Files that exist do not change without --overwrite.
  cf logs WEBSITE [--lines N] [--type access|error]    The last lines of the output of the container.
  cf cron list WEBSITE
  cf cron add WEBSITE --schedule "M H DOM MON DOW" --command COMMAND [--off]
                                                       The command runs in the container of the website, in /app. The time is UTC.
  cf cron set WEBSITE CRON_ID [--schedule SCHEDULE] [--command COMMAND] [--on | --off]
  cf cron run WEBSITE CRON_ID                          Run the command now. The command fails when its exit code is not 0.
  cf cron delete WEBSITE CRON_ID
  cf uptime-checks list [WEBSITE] [--state up|down|pending|paused]
                                                       The checks that are down come first.
  cf uptime-checks get WEBSITE CHECK_ID                The state, the uptime and the incidents of a check.
  cf uptime-checks add WEBSITE [--domain DOMAIN] [--path PATH] [--http] [--interval SECONDS] [--timeout SECONDS]
                       [--status CODE] [--text TEXT] [--failures N] [--off]
                                                       The panel requests the address on a schedule, and sends an email
                                                       when it is down. The default is the primary domain, "/", every 300 seconds.
  cf uptime-checks set WEBSITE CHECK_ID [--domain DOMAIN] [--path PATH] [--http | --https] [--interval SECONDS]
                       [--timeout SECONDS] [--status CODE | --any-status] [--text TEXT | --no-text] [--failures N] [--on | --off]
  cf uptime-checks run WEBSITE CHECK_ID                Request the address now. The exit code is 1 when the request fails.
  cf uptime-checks remove WEBSITE CHECK_ID
  cf uptime-checks incidents [--website WEBSITE] [--open]
  cf commands run WEBSITE [--timeout SECONDS] -- COMMAND...
                                                       Run one command line now in the container of the website, in /app, as
                                                       the user of the website. cf waits and shows the output. The exit code
                                                       of cf is the exit code of the command. The panel logs each command.
  cf commands list WEBSITE [--search TEXT]             The command log: who ran each command, when, and the exit code.
  cf commands get WEBSITE COMMAND_ID                   One command of the log, with the end of its output.
  cf deploys show WEBSITE                              The Git repository, the deploy key, the webhook and the releases.
  cf deploys connect WEBSITE --repository URL --branch BRANCH [--web-directory DIR]
                             [--trigger manual|webhook|schedule] [--check-minutes N]
                             [--build COMMAND]... [--no-build] [--migration COMMAND | --no-migration]
                                                       Also changes the settings. For a private repository, add the deploy key
                                                       that the command shows to the repository. The trigger schedule checks
                                                       the branch each N minutes (default 5). The migration command runs
                                                       before the switch. After a migration, the panel does not go back
                                                       when the health check fails: it sends an email.
  cf deploys secret set WEBSITE NAME                   An environment variable of the build commands. The value is read
                                                       from CF_SECRET or stdin.
  cf deploys secret remove WEBSITE NAME
  cf deploys start WEBSITE [--forget-host-key]         Deploy the branch now. The website changes to the new release with no stop.
                                                       --forget-host-key: after the Git server got a new SSH host key.
  cf deploys webhook WEBSITE                           Make a new webhook address and secret. The old ones stop at once.
  cf deploys activate WEBSITE RELEASE_ID               Go back to a release that is on the server.
  cf deploys disconnect WEBSITE
  cf security show WEBSITE                             The protection of the website in the edge proxy.
  cf security firewall WEBSITE [--mode off|detect|block] [--exclude RULE_ID]... [--include RULE_ID]... [--no-exclusions]
                               [--skip PATH]... [--no-skip]
                                                       Use detect first, and look at the events before you select block.
  cf security events WEBSITE [--limit N]               The last requests that the firewall blocked or detected.
  cf security ip WEBSITE [--allow RANGE]... [--block RANGE]... [--remove RANGE]... [--no-allow] [--no-block]
                                                       With an allow list, the edge proxy refuses all other addresses.
  cf security sign-in-limit WEBSITE --per-minute N [--path PATH]... [--wordpress]
                                                       POST requests to the sign-in paths from one IP address. 0 is no limit.
  cf security protect WEBSITE PATH --user NAME         Add a user to a password-protected path, or change the password. The
                                                       password is read from CF_PATH_PASSWORD or stdin.
  cf security unprotect WEBSITE PATH [--user NAME]     Remove a user, or the password of the path.
  cf security scan WEBSITE                             Scan the files for malware now, and show the result. The exit status
                                                       is 1 when the scan finds malware.
  cf security scans WEBSITE                            The malware scan schedule and the last scans.
  cf security scan-schedule WEBSITE weekly|off
  cf previews list WEBSITE                             The settings and the preview websites of the pull requests.
  cf previews set WEBSITE [--on | --off] [--domain DOMAIN] [--limit N] [--copy-environment | --no-copy-environment]
                                                       Each preview is a website pr-NUMBER.DOMAIN. Use a sub-domain of a
                                                       verified domain. The webhook of the website gets the pull request events.
                                                       --off removes the preview websites.
  cf previews create WEBSITE --number N --branch BRANCH [--title TITLE] [--url URL]
                                                       Make the preview of a pull request, or deploy it again. For a CI pipeline.
  cf previews remove WEBSITE NUMBER                    Remove the preview website of a pull request.
  cf database-servers list [--customer ID] [--server SERVER] [--engine ENGINE]
  cf database-servers get DATABASE_SERVER
  cf database-servers engines                          The engines: mariadb, postgresql, valkey and the engines of database packs.
  cf database-servers create --engine ENGINE [--version VERSION] [--name NAME] [--customer ID] [--plan ID] [--server SERVER]
                             [--vcpus N --memory-mb N --disk-gb N]
                                                       A database server is one container with its own limits. The plan gives the
                                                       limits and the number of databases. Only an administrator sets limits.
                                                       --version: a version of a pack engine. The database server keeps it.
  cf database-servers set DATABASE_SERVER [--name NAME] [--plan ID] [--vcpus N] [--memory-mb N] [--disk-gb N]
                                                       New limits restart the database server.
  cf database-servers retry DATABASE_SERVER            Make a database server again after the first job failed.
  cf database-servers move DATABASE_SERVER --server SERVER
                                                       Move a database server to a different server. Only an administrator.
                                                       It keeps its name and its network name. It does not operate while its
                                                       data moves through the tunnel between the two servers.
  cf database-servers delete DATABASE_SERVER           Remove a database server that has no databases.
  cf databases list WEBSITE
  cf databases create WEBSITE --engine ENGINE --name NAME [--no-user] [--database-server DATABASE_SERVER]
                                                       A Valkey database has a database number, and always a user with the same name.
                                                       With no database server, the panel uses the first one of the engine with space.
  cf databases delete WEBSITE DATABASE
  cf databases adminer WEBSITE DATABASE [--read-only]  Open a MariaDB or PostgreSQL database in Adminer, with a temporary
                                                       database user that expires after 60 minutes. The command writes an
                                                       HTML file that signs you in: open it in your browser.
  cf database-users list WEBSITE
  cf database-users create WEBSITE --engine ENGINE --name NAME [--grant DATABASE=readonly|readwrite]...
                           [--database-server DATABASE_SERVER]
  cf database-users set WEBSITE USER [--grant DATABASE=readonly|readwrite]... [--no-grants]
                        [--access website|anywhere] [--reset-password]
  cf database-users delete WEBSITE USER
  cf mail domains list [--customer ID]
  cf mail domains add DOMAIN [--customer ID] [--server SERVER]
  cf mail domains get DOMAIN               Show the DNS records to add, and which of them the last check found.
  cf mail domains check DOMAIN             Read the DNS records of the domain now.
  cf mail domains retry DOMAIN             Add a mail domain again after the first job failed.
  cf mail domains remove DOMAIN            Remove a mail domain that has no mailboxes and no addresses.
  cf mail mailboxes list [--domain DOMAIN] [--customer ID]
  cf mail mailboxes create ADDRESS [--quota-mb N] [--encrypt]
                                           The panel shows the password one time. With --encrypt, the panel
                                           makes OpenPGP keys and the mail server encrypts new mail.
  cf mail mailboxes set ADDRESS [--quota-mb N] [--new-password] [--encrypt | --public-key FILE | --no-encryption]
  cf mail mailboxes private-key ADDRESS [--output FILE]
                                           The private key that the panel made. It needs the password of the mailbox.
  cf mail mailboxes autoresponder ADDRESS [--on | --off] [--subject TEXT] [--text-file FILE] [--from DAY] [--until DAY]
                                           Show or change the autoresponder. DAY is YYYY-MM-DD in the time zone
                                           of this computer, or an RFC 3339 time. "-" removes a subject or a day.
  cf mail mailboxes backups ADDRESS        List the backups of the mailbox, and the result of the last restore of each.
  cf mail mailboxes backup ADDRESS         Back up all folders and messages of the mailbox now.
  cf mail mailboxes restore ADDRESS BACKUP Add the messages of the backup that the mailbox does not have, in their
                                           folders. The restore does not change or remove a message of the mailbox.
  cf mail mailboxes delete ADDRESS         Remove a mailbox and its messages.
  cf mail aliases list [--domain DOMAIN] [--customer ID]
  cf mail aliases create ADDRESS --to ADDRESS... [--forward]
                                           An alias sends to mailboxes of the customer. With --forward, the
                                           destinations are addresses on other mail servers.
  cf mail aliases delete ADDRESS
  cf mail clusters list
  cf mail clusters create NAME --hostname NAME --member SERVER=HOSTNAME=ADDRESS[=REGION]...
                                           3 or 5 servers with the email role and no mail server. Each member
                                           has all the mail. ADDRESS is the public address of the server.
  cf mail clusters get CLUSTER             Show the step of the setup, the members and the DNS records.
  cf mail clusters retry CLUSTER           Start a step that failed again.
  cf dns list [--customer ID]                          The DNS zones, and the name servers of the panel.
  cf dns create DOMAIN [--customer ID] [--account ACCOUNT [--no-import]]
                                                       Then give the name servers to the registrar of the domain. With
                                                       --account, the zone is at an external DNS provider, and first gets
                                                       the records that the provider has for the domain.
  cf dns get ZONE                                      The state of the zone, and the DS record when DNSSEC is on.
  cf dns records ZONE [--type TYPE]
  cf dns add ZONE TYPE NAME VALUE [--ttl SECONDS] [--priority N]
                       TYPE is A, AAAA, CNAME, MX, TXT, SRV, CAA or NS. NAME is @ for the zone itself.
                       For example: cf dns add example.com MX @ mail.example.com --priority 10
  cf dns set ZONE RECORD_ID [--name NAME] [--value VALUE] [--ttl SECONDS] [--priority N]
  cf dns remove ZONE RECORD_ID
  cf dns dnssec ZONE on|off                            Before "off", remove the DS record at the registrar.
  cf dns check ZONE                                    Ask the public DNS for the name servers of the zone again.
  cf dns sync ZONE                                     Send the zone to each DNS server again.
  cf dns import ZONE FILE [--dry-run]                  Add the records of a zone file (the format of BIND), for example
                                                       the export of your current DNS provider. --dry-run makes no change.
  cf dns export ZONE [--file PATH]                     The records of the zone as a zone file.
  cf dns move ZONE ACCOUNT|panel [--no-import]        Move the zone to a different DNS provider, or back to the panel.
  cf dns delete ZONE [--keep-at-provider]              With --keep-at-provider, the zone stays at its external DNS provider.
  cf dns accounts list                                 The accounts at external DNS providers, and the providers of the plugins.
  cf dns accounts add PROVIDER NAME [--set FIELD=VALUE]... [--customer ID | --panel]
                                                       The plugin checks the credentials. Each secret field comes from its
                                                       environment variable (apiToken: CF_API_TOKEN) or stdin.
  cf dns accounts set ACCOUNT [--name NAME] [--set FIELD=VALUE]... [--credentials]
                                                       --credentials sends the secrets from their environment variables again.
  cf dns accounts remove ACCOUNT                       Only an account that no zone uses.
  cf certificates list [--customer ID]                 The certificates of the customers, and the domains that use them.
  cf certificates get CERTIFICATE [--pem]              CERTIFICATE is the ID or the first name. --pem shows only the chain.
  cf certificates upload --cert FILE --key FILE [--customer ID]
                       The chain and the private key in PEM format. "-" reads the standard input.
  cf certificates request DOMAIN [--wildcard] [--customer ID]
                       Let's Encrypt with DNS validation. The customer needs a DNS zone for the domain on the
                       panel, and the registrar must have the name servers of the zone. The command waits
                       until the order ends.
  cf certificates replace CERTIFICATE --cert FILE --key FILE   Replace an uploaded certificate.
  cf certificates renew CERTIFICATE                    Start a new order now. The panel also renews by itself.
  cf certificates delete CERTIFICATE                   Its domains get a different certificate.
  cf networks list [--customer ID]                     The private networks. Each customer has the network "default".
  cf networks create NAME [--customer ID] [--website WEBSITE]... [--database-server DATABASE_SERVER]...
                                                       The plan of the customer gives the number of named networks.
  cf networks get NETWORK                              The members, with the name and the port that a website connects to.
  cf networks add NETWORK [--website WEBSITE]... [--database-server DATABASE_SERVER]...
  cf networks remove NETWORK [--website WEBSITE]... [--database-server DATABASE_SERVER]...
  cf networks rename NETWORK NAME
  cf networks delete NETWORK                           The members stay. The network "default" cannot be removed.
  cf notifications list [--state STATE] [--event EVENT] [--limit N | --all]
                                                       The emails that the panel sent. STATE is queued, sent, failed or skipped.
  cf notifications get ID                              The text of one email.
  cf notifications retry ID                            Send a failed or skipped email again.
  cf notifications preferences                         The events that send an email to you.
  cf notifications on|off EVENT...                     For example: cf notifications off job.failed usage.alert
  cf mail-server get                                   The SMTP server (administrators: of the panel; resellers: their own).
  cf mail-server set --host HOST --from ADDRESS [--port 587] [--encryption tls|starttls|none] [--user USER]
                     [--from-name NAME] [--reply-to ADDRESS] [--keep-password] [--off]
                                                       The password comes from CF_SMTP_PASSWORD, or the prompt.
  cf mail-server test [--to ADDRESS]                   Send a test email at once.
  cf mail-server remove
  cf backups list|create WEBSITE
  cf backups restore WEBSITE SNAPSHOT [--scope website|database|file] [--item ITEM] [--remove-new-files]
                       --remove-new-files is for a full website. It deletes the files that are not in the backup.
  cf backups policy
  cf backups policy password   Show the restic repository and its password for each server. Keep a copy in a safe place.
  cf backups policy set [--daily N] [--weekly N] [--monthly N] [--time HH:MM] [--target REPOSITORY]
                        [--credentials-from-env | --remove-credentials]
                       The credentials come from AWS_ACCESS_KEY_ID, B2_ACCOUNT_ID and the other restic variables.
  cf exports list [--customer ID]
  cf exports create [--customer ID] [--no-files] [--no-databases]   Export an account: settings, files, databases and DNS zones.
  cf exports get|delete EXPORT
  cf exports download EXPORT [--output FILE]   Download the archive. Run it again to continue a download that stopped.
  cf jobs list [--website WEBSITE] [--limit N | --all]
  cf jobs get|wait JOB
  cf servers list
  cf servers get SERVER
  cf servers set SERVER [--update-policy security|all|manual] [--window HH:MM-HH:MM] [--gvisor-platform systrap|kvm]
                        [--roles web,database,dns] [--dns-name HOST | --no-dns-name]
                       A server with the role dns and a name server host name answers for all DNS zones.
  cf servers update SERVER [--policy security|all|tools]   "tools" updates the held container tools.
  cf servers mail SERVER --hostname NAME   Turn on email for a server, or change the host name of its mail server.
                                           The name needs an A or AAAA record with the address of the server.
  cf servers reboot SERVER
  cf servers update-agent SERVER [--version VERSION] [--allow-downgrade]
  cf servers join-token
  cf servers check SERVER                  Tests the connection from the panel to the server.
  cf servers remove SERVER                 Removes a server that has no websites from the panel.
  cf mesh show                             The encrypted tunnels between the servers, with their state.
  cf mesh connect SERVER SERVER            Makes a tunnel between two servers.
  cf mesh repair SERVER SERVER             Sends the tunnel state to the two servers again and tests the tunnel.
  cf mesh disconnect SERVER SERVER         Removes the tunnel between two servers.
  cf mesh rotate-key SERVER                Makes a new tunnel key on a server. Its tunnels stop for a short time.
  cf mesh set-address SERVER ADDRESS|auto  Sets the public IP address for the tunnel traffic of a server.
  cf mesh set [--range CIDR] [--port N]    Possible only while the mesh has no tunnel.
  cf panel-certificate show
  cf panel-certificate set internal|letsencrypt
                       "internal": a certificate from the panel CA. "letsencrypt" needs a public domain.
  cf panel-certificate ca      Print the root certificate of the panel CA in PEM format.
  cf images list
  cf images add --name NAME --reference IMAGE --tag TAG [--policy POLICY] [--port N] [--arg ARG]...
  cf images set IMAGE_ID [--policy POLICY] [--port N] [--arg ARG]... [--no-command]
  cf images check IMAGE_ID
  cf rollouts list
  cf rollouts start IMAGE_ID
  cf rollouts pause|resume|roll-back ROLLOUT_ID
  cf tokens list
  cf tokens create --name NAME --scope SCOPE[,SCOPE] --days DAYS [--totp CODE]
                       The password is read from CF_PASSWORD or stdin.
  cf tokens revoke ID
  cf customers list
  cf customers create --name NAME --email EMAIL --plan ID [--role owner|reseller] [--with-user]
                       --with-user also makes a user with the email of the customer, and shows a temporary password.
  cf customers set ID [--plan ID] [--suspend | --unsuspend]
  cf imports list
  cf imports create --customer CUSTOMER --server SERVER (--file FILE | --url URL)
                       Reads a cPanel, DirectAdmin, Plesk or HestiaCP backup. The command sends a FILE to the panel in parts,
                       then waits for the plan. Each server downloads a URL itself.
  cf imports upload IMPORT --file FILE     Continues an upload from the part that the panel has.
  cf imports get IMPORT                    Shows the plan, or the parts of a running or complete import.
  cf imports start IMPORT (--all | [--website NAME[=IMAGE_ID]]... [--mail DOMAIN]... [--zone ZONE]...) [--keep-wp-config]
                       Makes the selected parts for the customer, and waits until the import is complete.
  cf imports delete IMPORT                 Removes the import and its archive. The websites stay.
  cf users list
  cf users create --name NAME --email EMAIL (--customer ID | --administrator) [--totp CODE]
                       The command shows a temporary password once. The user sets a new password at the first sign-in.
  cf users reset-password USER [--remove-totp] [--totp CODE]   A USER is an email address or an ID.
  cf users delete USER [--totp CODE]
                       A change to an administrator needs a sign-in (cf login), your password from
                       CF_PASSWORD or stdin, and --totp when your TOTP is on.
  cf plans list
  cf audit list [--limit N | --all]
  cf account password [--totp CODE]   The passwords come from CF_PASSWORD and CF_NEW_PASSWORD, or stdin.
  cf account totp setup
  cf account totp enable|disable --totp CODE
  cf plugins list
  cf plugins get|enable|disable|test|deliveries|restart|remove PLUGIN    A PLUGIN is a slug (for example uptime) or an ID.
  cf plugins install --address URL [--manifest plugin.json] [--signature plugin.json.sig] [--totp CODE]
                       Shows the token and the webhook secret of the plugin one time.
  cf plugins install --hosted [--server ID] [--host NAME] [--manifest plugin.json] [--signature plugin.json.sig] [--totp CODE]
                       The panel runs the signed image of the plugin in a gVisor container.
  cf plugins upgrade PLUGIN [--manifest plugin.json] [--signature plugin.json.sig] [--totp CODE]
  cf plugins credentials PLUGIN [--totp CODE]    A new token and a new webhook secret. The old ones stop.
  cf plugins keys list                 The trusted keys of publishers. Plugins and packs use the same keys.
  cf plugins keys add --name NAME --file cosign.pub [--totp CODE]
  cf plugins keys remove ID            The panel refuses to remove a key that signed an installed pack.
  cf plugins developer-mode on|off [--totp CODE]
                       Changes that give a plugin access need your password from CF_PASSWORD or stdin.
  cf packs list                        The packs: signed manifests that add a website runtime or a database engine.
  cf packs get PACK                    A PACK is a slug (for example golang) or an ID. Shows the images and the state on each web server.
  cf packs preview [--manifest pack.json] [--signature pack.json.sig]
                       Checks the manifest and the signature, and shows what the pack adds. It changes nothing.
  cf packs install [--manifest pack.json] [--signature pack.json.sig] [--totp CODE]
  cf packs upgrade PACK [--manifest pack.json] [--signature pack.json.sig] [--totp CODE]
                       A new version. A new digest for an image that websites use waits for a rollout.
  cf packs remove PACK                 The panel refuses to remove a pack that websites or database servers use.
  cf packs sync                        Sends all packs to each web server again.
  cf packs keys list|add|remove        The same as cf plugins keys.
  cf packs developer-mode on|off [--totp CODE]
                       The developer mode accepts packs with no signature. Use it only to test your own pack.
                       Install, upgrade and the developer mode need your password from CF_PASSWORD or stdin.
  cf NAME [ARG]...     Runs the program cf-NAME from the PATH, for example a command of a plugin.
                       It gets CF_PANEL and CF_TOKEN of your sign-in.
  cf version

A WEBSITE is a name (for example acme.example) or an ID. A SERVER is a name (for example web-1) or an ID. A DATABASE or USER is a name, with or without
the prefix of the website (for example shop or u1a2b3c_shop), or an ID. A DATABASE_SERVER is a name (for example mariadb-1) or an ID.
Add --json to see the answer of the API. A change waits for its job; add --no-wait to return at once.
The settings CF_PANEL and CF_TOKEN replace the saved panel and token.
The panel address must start with https://. An address with http:// is accepted only for this computer (localhost).
`

type app struct {
	out, err io.Writer
	// raw is the output with no change: for the content of a file and for a plugin command.
	raw, rawErr io.Writer
	// in gives the password when CF_PASSWORD is not set.
	in      io.Reader
	lines   *bufio.Reader
	linesOf io.Reader
	api     *client.Client
	json    bool
	noWait  bool
	// poll is the time between two job checks.
	poll time.Duration
}

type settings struct {
	Panel string `json:"panel"`
	Token string `json:"token"`
	// Session is true when the token is from "cf login". "cf logout" then deletes it in the panel.
	Session bool `json:"session,omitempty"`
}

// checkPanel refuses a panel address that sends the token with no encryption (audit finding
// S12). HTTP is accepted only for this computer, for example for a test.
func checkPanel(address string) error {
	u, err := url.Parse(address)
	if err != nil || u.Host == "" {
		return fmt.Errorf("the panel address %q is not a URL, for example https://panel.example.com", address)
	}
	switch u.Scheme {
	case "https":
		return nil
	case "http":
		host := u.Hostname()
		if ip := net.ParseIP(host); host == "localhost" || (ip != nil && ip.IsLoopback()) {
			return nil
		}
		return fmt.Errorf("the panel address %q does not use HTTPS, so the token would go over the network with no encryption; use https://", address)
	}
	return fmt.Errorf("the panel address %q must start with https://", address)
}

func settingsPath() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		dir = "."
	}
	return filepath.Join(dir, "cloudfactory", "cli.json")
}

func loadSettings() settings {
	var s settings
	if b, err := os.ReadFile(settingsPath()); err == nil {
		json.Unmarshal(b, &s)
	}
	if v := os.Getenv("CF_PANEL"); v != "" {
		s.Panel = v
	}
	if v := os.Getenv("CF_TOKEN"); v != "" {
		// A token from the environment is not the saved session.
		s.Token, s.Session = v, false
	}
	return s
}

func saveSettings(s settings) error {
	p := settingsPath()
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(s, "", "  ")
	// The file holds a token, so only the user can read it. Adversarial review, finding C1-10:
	// an old file can have a different mode, thus cf writes a new file and puts it in place.
	tmp, err := os.CreateTemp(filepath.Dir(p), ".cli-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), p)
}

// globalFlags removes --json and --no-wait from the arguments.
func (a *app) globalFlags(args []string) []string {
	var rest []string
	for i, arg := range args {
		// Adversarial review, finding C1-11: the words after "--" are not options of cf.
		if arg == "--" {
			return append(rest, args[i:]...)
		}
		switch arg {
		case "--json":
			a.json = true
		case "--no-wait":
			a.noWait = true
		default:
			rest = append(rest, arg)
		}
	}
	return rest
}

func (a *app) dispatch(ctx context.Context, args []string) error {
	if a.raw == nil {
		a.raw, a.rawErr = a.out, a.err
		a.out, a.err = &cleanWriter{a.out}, &cleanWriter{a.err}
	}
	args = a.globalFlags(args)
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		fmt.Fprint(a.out, usage)
		return nil
	}
	if a.poll == 0 {
		a.poll = time.Second
	}
	switch args[0] {
	case "version":
		fmt.Fprintln(a.out, version)
		return nil
	case "login":
		return a.login(ctx, args[1:])
	case "use":
		return a.use(args[1:])
	case "logout":
		return a.logout(ctx)
	}
	// "cf NAME" runs "cf-NAME" from the PATH when cf has no command NAME (plugins).
	if found, err := a.runExternal(ctx, args); found {
		return err
	}
	if a.api == nil {
		s := loadSettings()
		if s.Panel == "" || s.Token == "" {
			return errors.New("sign in first: cf login --panel URL --email EMAIL")
		}
		if err := checkPanel(s.Panel); err != nil {
			return err
		}
		a.api = client.New(s.Panel, s.Token)
	}
	if len(args) < 2 && args[0] != "help" {
		return fmt.Errorf("give a command for %s; see cf help", args[0])
	}
	cmd, sub, rest := args[0], args[1], args[2:]
	switch cmd {
	case "websites", "website":
		return a.websites(ctx, sub, rest)
	case "domains", "domain":
		return a.domains(ctx, sub, rest)
	case "runtime":
		return a.runtime(ctx, sub, rest)
	case "env":
		return a.env(ctx, sub, rest)
	case "database-servers", "database-server", "db-servers":
		return a.databaseServers(ctx, sub, rest)
	case "databases", "database":
		return a.databases(ctx, sub, rest)
	case "database-users", "database-user", "db-users":
		return a.databaseUsers(ctx, sub, rest)
	case "dns":
		return a.dns(ctx, sub, rest)
	case "certificates", "certificate", "certs":
		return a.certificates(ctx, sub, rest)
	case "networks", "network":
		return a.networks(ctx, sub, rest)
	case "notifications", "notification":
		return a.notifications(ctx, sub, rest)
	case "mail-server":
		return a.mailServer(ctx, sub, rest)
	case "backups", "backup":
		return a.backups(ctx, sub, rest)
	case "exports", "export":
		return a.exports(ctx, sub, rest)
	case "jobs", "job":
		return a.jobs(ctx, sub, rest)
	case "servers", "server":
		return a.servers(ctx, sub, rest)
	case "mesh":
		return a.mesh(ctx, sub, rest)
	case "panel-certificate":
		return a.panelCertificate(ctx, sub, rest)
	case "images", "image":
		return a.images(ctx, sub, rest)
	case "sftp":
		return a.sftp(ctx, sub, rest)
	case "mail", "email":
		return a.mail(ctx, sub, rest)
	case "customers", "customer":
		return a.customers(ctx, sub, rest)
	case "imports", "import":
		return a.imports(ctx, sub, rest)
	case "users", "user":
		return a.users(ctx, sub, rest)
	case "plans", "plan":
		return a.plans(ctx, sub)
	case "audit":
		return a.audit(ctx, sub, rest)
	case "account":
		return a.account(ctx, sub, rest)
	case "rollouts", "rollout":
		return a.rollouts(ctx, sub, rest)
	case "tokens", "token":
		return a.tokens(ctx, sub, rest)
	case "files", "file":
		return a.files(ctx, sub, rest)
	case "logs":
		// "cf logs WEBSITE": the command has no second word.
		return a.logs(ctx, sub, rest)
	case "cron":
		return a.cron(ctx, sub, rest)
	case "uptime-checks", "uptime-check":
		return a.uptime(ctx, sub, rest)
	case "commands", "command":
		return a.commands(ctx, sub, rest)
	case "deploys", "deploy":
		return a.deploys(ctx, sub, rest)
	case "security":
		return a.security(ctx, sub, rest)
	case "previews", "preview":
		return a.previews(ctx, sub, rest)
	case "plugins", "plugin":
		return a.plugins(ctx, sub, rest)
	case "packs", "pack":
		return a.packs(ctx, sub, rest)
	}
	return fmt.Errorf("unknown command %q; see cf help", cmd)
}

func (a *app) login(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("login", flag.ContinueOnError)
	fs.SetOutput(a.err)
	panel := fs.String("panel", loadSettings().Panel, "the address of the panel")
	email := fs.String("email", "", "your email address")
	totp := fs.String("totp", "", "the code from your authenticator app")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *panel == "" || *email == "" {
		return errors.New("login needs --panel and --email")
	}
	if err := checkPanel(*panel); err != nil {
		return err
	}
	password, err := a.password()
	if err != nil {
		return err
	}
	c := client.New(*panel, "")
	var out struct {
		Token     string `json:"token"`
		ExpiresAt string `json:"expiresAt"`
	}
	body := map[string]any{"email": *email, "password": password}
	if *totp != "" {
		body["totp"] = *totp
	}
	if err := c.Do(ctx, http.MethodPost, "/auth/login", nil, body, &out); err != nil {
		// The password is correct, but TOTP is on for the account and the command had no code.
		var answer *client.Error
		if *totp == "" && errors.As(err, &answer) && len(answer.Fields["totp"]) > 0 {
			return errors.New("TOTP is on for this account: run the command again with --totp CODE")
		}
		return err
	}
	if err := saveSettings(settings{Panel: *panel, Token: out.Token, Session: true}); err != nil {
		return err
	}
	fmt.Fprintf(a.out, "Signed in. The session expires at %s.\n", out.ExpiresAt)
	// Audit finding B5: with a temporary password, the panel refuses each other command.
	var me struct {
		MustChangePassword bool `json:"mustChangePassword"`
	}
	c.Token = out.Token
	if err := c.Do(ctx, http.MethodGet, "/me", nil, nil, &me); err == nil && me.MustChangePassword {
		fmt.Fprintln(a.out, "Your password is temporary. Set a new password before you use other commands: cf account password")
	}
	return nil
}

// logout removes the saved token. The panel also deletes a session from "cf login", so that
// a copy of the token does not work (audit finding S8). An API token from "cf use" stays in
// the panel: revoke it with "cf tokens revoke".
func (a *app) logout(ctx context.Context) error {
	var s settings
	if b, err := os.ReadFile(settingsPath()); err == nil {
		json.Unmarshal(b, &s)
	}
	if s.Token == "" {
		fmt.Fprintln(a.out, "You are not signed in.")
		return nil
	}
	var remote error
	if s.Session && checkPanel(s.Panel) == nil {
		remote = client.New(s.Panel, s.Token).Do(ctx, http.MethodPost, "/auth/logout", nil, nil, nil)
		// The session is not valid: it is deleted already or it is too old.
		var apiErr *client.Error
		if errors.As(remote, &apiErr) && apiErr.Status == http.StatusUnauthorized {
			remote = nil
		}
	}
	if err := saveSettings(settings{Panel: s.Panel}); err != nil {
		return err
	}
	if remote != nil {
		return fmt.Errorf("the token is removed from this computer, but the panel did not delete the session: %w", remote)
	}
	if s.Session {
		fmt.Fprintln(a.out, "Signed out.")
	} else {
		fmt.Fprintln(a.out, "The API token is removed from this computer. It stays valid in the panel: revoke it with cf tokens revoke.")
	}
	return nil
}

// password reads the password of the user from CF_PASSWORD or from stdin.
func (a *app) password() (string, error) {
	return a.secret("CF_PASSWORD", "Password: ")
}

// secret reads a secret from an environment variable, or else one line from stdin. Secrets
// are not given as arguments, because the shell history and the process list show arguments.
func (a *app) secret(env, prompt string) (string, error) {
	if v := os.Getenv(env); v != "" {
		return v, nil
	}
	in := a.in
	if in == nil {
		in = os.Stdin
	}
	// Keep one reader, so that a second secret reads the next line.
	if a.lines == nil || a.linesOf != in {
		a.lines, a.linesOf = bufio.NewReader(io.LimitReader(in, 16384)), in
	}
	fmt.Fprint(a.err, prompt)
	line, err := a.lines.ReadString('\n')
	if err != nil && err != io.EOF {
		return "", err
	}
	value := strings.TrimRight(line, "\r\n")
	if value == "" {
		return "", fmt.Errorf("give the value in %s or on stdin", env)
	}
	return value, nil
}

func (a *app) use(args []string) error {
	fs := flag.NewFlagSet("use", flag.ContinueOnError)
	fs.SetOutput(a.err)
	panel := fs.String("panel", "", "the address of the panel")
	token := fs.String("token", "", "an API token")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *panel == "" || *token == "" {
		return errors.New("use needs --panel and --token")
	}
	if err := checkPanel(*panel); err != nil {
		return err
	}
	return saveSettings(settings{Panel: *panel, Token: *token})
}

// change sends a request that gives a job, then waits for the job unless --no-wait is set.
func (a *app) change(ctx context.Context, method, path string, body any) error {
	var out map[string]json.RawMessage
	if err := a.api.Do(ctx, method, path, nil, body, &out); err != nil {
		return err
	}
	if a.json {
		return a.printJSON(out)
	}
	var job client.Job
	if raw, ok := out["job"]; ok && string(raw) != "null" {
		json.Unmarshal(raw, &job)
	}
	if raw, ok := out["password"]; ok && string(raw) != "null" {
		var pw string
		json.Unmarshal(raw, &pw)
		fmt.Fprintf(a.out, "Password: %s\nCopy it now. The panel does not show it again.\n", pw)
	}
	if raw, ok := out["databaseServer"]; ok && string(raw) != "null" {
		var s struct {
			Name, Host string
			Port       int
		}
		json.Unmarshal(raw, &s)
		fmt.Fprintf(a.out, "Database server %s: the websites of the customer on the same server connect to %s:%d\n", s.Name, s.Host, s.Port)
	}
	if raw, ok := out["databaseCredentials"]; ok {
		var creds []struct {
			Engine, User, Password, Host string
			Port                         int
			Databases                    []string
		}
		json.Unmarshal(raw, &creds)
		for _, c := range creds {
			// The host name and the port of the database server of the user.
			at := ""
			if c.Host != "" && c.Port != 0 {
				at = fmt.Sprintf(" on %s:%d", c.Host, c.Port)
			}
			fmt.Fprintf(a.out, "Database user %s (%s%s) for %s: %s\n", c.User, c.Engine, at, strings.Join(c.Databases, ", "), c.Password)
		}
		if len(creds) > 0 {
			fmt.Fprintln(a.out, "Copy the passwords now. The panel does not show them again.")
		}
	}
	if raw, ok := out["domain"]; ok {
		var d struct {
			Name         string `json:"name"`
			Verification *struct {
				Type, Name, Value string
			} `json:"verification"`
		}
		json.Unmarshal(raw, &d)
		// CF-03: the edge proxy serves the domain only after the TXT record is correct.
		if d.Verification != nil {
			fmt.Fprintf(a.out, "Add this DNS record to prove that you control %s:\n  %s %s %q\nThen run: cf domains verify WEBSITE %s\n", d.Name, d.Verification.Name, d.Verification.Type, d.Verification.Value, d.Name)
		}
	}
	if job.ID == "" {
		fmt.Fprintln(a.out, "Done.")
		return nil
	}
	if a.noWait {
		fmt.Fprintf(a.out, "Job %s is in the queue: %s\n", job.ID, job.Title)
		return nil
	}
	return a.wait(ctx, job.ID)
}

func (a *app) wait(ctx context.Context, id string) error {
	job, err := a.api.WaitJob(ctx, id, a.poll, func(j client.Job) {
		if j.Step < len(j.Steps) && !j.Finished() {
			fmt.Fprintf(a.out, "[%d/%d] %s\n", j.Step+1, len(j.Steps), j.Steps[j.Step])
		}
	})
	if err != nil {
		return fmt.Errorf("%s: %w", job.Title, err)
	}
	fmt.Fprintf(a.out, "%s: complete.\n", job.Title)
	return nil
}

func (a *app) printJSON(v any) error {
	enc := json.NewEncoder(a.out)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// table prints rows of a JSON list. Each column is a field name.
func (a *app) table(rows []map[string]any, cols []string) {
	tw := tabwriter.NewWriter(a.out, 0, 4, 2, ' ', 0)
	header := make([]string, len(cols))
	for i, c := range cols {
		header[i] = strings.ToUpper(c)
	}
	fmt.Fprintln(tw, strings.Join(header, "\t"))
	for _, r := range rows {
		vals := make([]string, len(cols))
		for i, c := range cols {
			vals[i] = cell(r[c])
		}
		fmt.Fprintln(tw, strings.Join(vals, "\t"))
	}
	tw.Flush()
}

func cell(v any) string {
	switch x := v.(type) {
	case nil:
		return "-"
	case string:
		return oneLine(x)
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	case bool:
		if x {
			return "yes"
		}
		return "no"
	case []any:
		parts := make([]string, len(x))
		for i, p := range x {
			parts[i] = cell(p)
		}
		return strings.Join(parts, ",")
	}
	b, _ := json.Marshal(v)
	return string(b)
}

func (a *app) simpleList(ctx context.Context, path string, cols []string) error {
	return a.list(ctx, path, nil, cols)
}

func (a *app) list(ctx context.Context, path string, q url.Values, cols []string) error {
	return a.listLimit(ctx, path, q, cols, 0)
}

// listLimit prints the first limit rows of a list, or all rows when limit is 0. It reads each
// page of the list and says when it does not show all rows (audit batch 3, issue 4).
func (a *app) listLimit(ctx context.Context, path string, q url.Values, cols []string, limit int) error {
	rows, total, err := client.Pages[map[string]any](ctx, a.api, path, q, limit)
	if err != nil {
		return err
	}
	if a.json {
		return a.printJSON(rows)
	}
	a.table(rows, cols)
	if total > len(rows) {
		fmt.Fprintf(a.out, "Showing %d of %d. Add --all to see all.\n", len(rows), total)
	}
	return nil
}

// limitFlags adds --limit and --all to a list command. The list shows the newest rows first.
func limitFlags(fs *flag.FlagSet) func() int {
	limit := fs.Int("limit", 50, "the number of rows to show")
	all := fs.Bool("all", false, "show all rows")
	return func() int {
		if *all {
			return 0
		}
		return *limit
	}
}

func need(args []string, n int, what string) error {
	if len(args) < n {
		return fmt.Errorf("give %s", what)
	}
	return nil
}

func (a *app) websites(ctx context.Context, sub string, args []string) error {
	switch sub {
	case "list", "ls":
		fs := flag.NewFlagSet("list", flag.ContinueOnError)
		status := fs.String("status", "", "starting, running, stopping, stopped, failed or moving")
		if err := fs.Parse(args); err != nil {
			return err
		}
		q := url.Values{}
		if *status != "" {
			q.Set("status", *status)
		}
		return a.list(ctx, "/websites", q, []string{"name", "status", "application", "updateReady", "id"})
	case "get":
		if err := need(args, 1, "a website"); err != nil {
			return err
		}
		w, err := a.api.FindWebsite(ctx, args[0])
		if err != nil {
			return err
		}
		if a.json {
			return a.printJSON(w)
		}
		fmt.Fprintf(a.out, "Name:     %s\nID:       %s\nStatus:   %s\nRuntime:  %s\nUser:     %s\n", w.Name, w.ID, w.Status, w.Runtime.Image, w.UnixUser)
		if w.MovingToServerID != "" {
			fmt.Fprintf(a.out, "Moves to: server %s\n", w.MovingToServerID)
		}
		// A different website in a network with this website connects to this name.
		if w.Kind != "" && w.Kind != "public" {
			fmt.Fprintf(a.out, "Kind:     %s\n", w.Kind)
		}
		if w.Runtime.Version != nil {
			fmt.Fprintf(a.out, "Version:  %s\n", *w.Runtime.Version)
		}
		if w.Command != "" {
			fmt.Fprintf(a.out, "Command:  %s\n", w.Command)
		}
		if w.InstallCommand != "" {
			fmt.Fprintf(a.out, "Install:  %s\n", w.InstallCommand)
		}
		if len(w.Environment) > 0 {
			fmt.Fprintf(a.out, "Env:      %s\n", strings.Join(w.Environment, ", "))
		}
		if w.NetworkHost != "" {
			fmt.Fprintf(a.out, "Network:  %s:%d\n", w.NetworkHost, w.NetworkPort)
		}
		for _, d := range w.Domains {
			fmt.Fprintf(a.out, "Domain:   %s (%s)\n", d.Name, d.Kind)
		}
		if w.ManagedByTerraform {
			fmt.Fprintln(a.out, "Terraform manages this website. Change it in your Terraform files.")
		}
		return nil
	case "create":
		if err := need(args, 1, "the name of the website"); err != nil {
			return err
		}
		fs := flag.NewFlagSet("create", flag.ContinueOnError)
		image := fs.String("image", "", "the image ID")
		plan := fs.String("plan", "", "the plan ID")
		customer := fs.String("customer", "", "the customer ID")
		server := fs.String("server", "", "the server ID")
		appName := fs.String("app", "", "the application, for example WordPress")
		kind := fs.String("kind", "", "public (a website with a domain), private (no domain: only the websites of its networks connect to it) or worker (runs a command, no HTTP). For private and worker, the name is a short name, for example queue.")
		command := fs.String("command", "", "the start command of a worker or of a Node.js or Python website, for example \"php artisan queue:work\"")
		install := fs.String("install", "", "the install command of a Node.js or Python website, for example \"npm ci\". Give \"\" for no install step.")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		body := map[string]any{"name": args[0], "imageId": *image, "planId": *plan}
		if given(fs, "install") {
			body["installCommand"] = *install
		}
		for k, v := range map[string]string{"customerId": *customer, "serverId": *server, "application": *appName, "kind": *kind, "command": *command} {
			if v != "" {
				body[k] = v
			}
		}
		return a.change(ctx, http.MethodPost, "/websites", body)
	case "set":
		return a.websiteSet(ctx, args)
	case "delete", "start", "stop", "restart", "staging", "retry", "move":
		if err := need(args, 1, "a website"); err != nil {
			return err
		}
		w, err := a.api.FindWebsite(ctx, args[0])
		if err != nil {
			return err
		}
		switch sub {
		case "delete":
			return a.change(ctx, http.MethodDelete, "/websites/"+w.ID, nil)
		case "staging":
			return a.change(ctx, http.MethodPost, "/websites/"+w.ID+"/staging", nil)
		case "retry":
			return a.change(ctx, http.MethodPost, "/websites/"+w.ID+"/retry", nil)
		case "move":
			return a.moveWebsite(ctx, w, args[1:])
		}
		return a.change(ctx, http.MethodPost, "/websites/"+w.ID+"/power", map[string]string{"action": sub})
	}
	return fmt.Errorf("unknown command websites %s", sub)
}

func (a *app) domains(ctx context.Context, sub string, args []string) error {
	if err := need(args, 2, "a website and a domain"); err != nil {
		return err
	}
	w, err := a.api.FindWebsite(ctx, args[0])
	if err != nil {
		return err
	}
	switch sub {
	case "add":
		fs := flag.NewFlagSet("add", flag.ContinueOnError)
		kind := fs.String("kind", "redirect", "redirect or alias")
		if err := fs.Parse(args[2:]); err != nil {
			return err
		}
		return a.change(ctx, http.MethodPost, "/websites/"+w.ID+"/domains", map[string]string{"name": args[1], "kind": *kind})
	case "verify":
		return a.change(ctx, http.MethodPost, "/websites/"+w.ID+"/domains/"+url.PathEscape(args[1])+"/verify", nil)
	case "remove", "rm":
		return a.change(ctx, http.MethodDelete, "/websites/"+w.ID+"/domains/"+url.PathEscape(args[1]), nil)
	case "primary":
		return a.change(ctx, http.MethodPost, "/websites/"+w.ID+"/domains/"+url.PathEscape(args[1])+"/primary", nil)
	case "check":
		return a.change(ctx, http.MethodPost, "/websites/"+w.ID+"/domains/"+url.PathEscape(args[1])+"/check", nil)
	}
	return fmt.Errorf("unknown command domains %s", sub)
}

// multi is a flag that can occur more than one time.
type multi []string

func (m *multi) String() string     { return strings.Join(*m, ",") }
func (m *multi) Set(v string) error { *m = append(*m, v); return nil }

func (a *app) runtime(ctx context.Context, sub string, args []string) error {
	if err := need(args, 1, "a website"); err != nil {
		return err
	}
	w, err := a.api.FindWebsite(ctx, args[0])
	if err != nil {
		return err
	}
	switch sub {
	case "revert":
		return a.change(ctx, http.MethodPost, "/websites/"+w.ID+"/runtime/revert", nil)
	case "install":
		return a.runtimeInstall(ctx, w.ID)
	case "set":
		fs := flag.NewFlagSet("set", flag.ContinueOnError)
		image := fs.String("image", "", "the image ID")
		worker := fs.String("worker-mode", "", "on or off")
		exts := fs.String("extensions", "", "the PHP extensions, separated by commas")
		root := fs.String("document-root", "", `the directory that the web server serves, for example web, or "." for the top directory`)
		command := fs.String("command", "", "the start command of a worker or of a Node.js or Python website")
		install := fs.String("install", "", `the install command of a Node.js or Python website, or "" for no install step`)
		var ini multi
		fs.Var(&ini, "ini", "a php.ini value as key=value")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		body := map[string]any{}
		if *command != "" {
			body["command"] = *command
		}
		if given(fs, "install") {
			body["installCommand"] = *install
		}
		if *image != "" {
			body["imageId"] = *image
		}
		switch *worker {
		case "on":
			body["workerMode"] = true
		case "off":
			body["workerMode"] = false
		case "":
		default:
			return errors.New("--worker-mode must be on or off")
		}
		if *exts != "" {
			body["extensions"] = strings.Split(*exts, ",")
		}
		if *root != "" {
			body["documentRoot"] = *root
		}
		if len(ini) > 0 {
			values := map[string]string{}
			for _, kv := range ini {
				k, v, ok := strings.Cut(kv, "=")
				if !ok {
					return fmt.Errorf("--ini %q must be key=value", kv)
				}
				values[k] = v
			}
			body["phpIni"] = values
		}
		if len(body) == 0 {
			return errors.New("give --image, --worker-mode, --extensions, --document-root, --command, --install or --ini")
		}
		return a.change(ctx, http.MethodPut, "/websites/"+w.ID+"/runtime", body)
	}
	return fmt.Errorf("unknown command runtime %s", sub)
}

type dbList struct {
	Prefix    string           `json:"prefix"`
	Databases []map[string]any `json:"databases"`
	Users     []map[string]any `json:"users"`
	// The database servers that the website can use: those of its customer on its server.
	Servers []map[string]any `json:"servers"`
}

// server gives a database server that the website can use, from a name or an ID.
func (l dbList) server(ref string) (map[string]any, error) {
	for _, s := range l.Servers {
		if s["id"] == ref || s["name"] == ref {
			return s, nil
		}
	}
	return nil, fmt.Errorf("the website cannot use a database server %q. It can use the database servers of its customer on its server: cf database-servers list", ref)
}

// serverName gives the name of the database server of a database or a user. It gives the ID
// when the list does not have the database server.
func (l dbList) serverName(row map[string]any) any {
	for _, s := range l.Servers {
		if s["id"] == row["databaseServerId"] {
			return s["name"]
		}
	}
	return row["databaseServerId"]
}

func (a *app) dbList(ctx context.Context, websiteID string) (dbList, error) {
	var out dbList
	err := a.api.Do(ctx, http.MethodGet, "/websites/"+websiteID+"/databases", nil, nil, &out)
	return out, err
}

// find gives the ID of a database or a user. The name can have the prefix of the website or not.
func (l dbList) find(rows []map[string]any, ref, what string) (map[string]any, error) {
	// Adversarial review, finding C1-03: a MariaDB database and a PostgreSQL database of one
	// website can have the same name. An ID, or ENGINE:NAME, selects one of them.
	engine, name := "", ref
	if e, n, ok := strings.Cut(ref, ":"); ok && (e == "mariadb" || e == "postgresql" || e == "valkey") {
		engine, name = e, n
	}
	var found []map[string]any
	for _, r := range rows {
		if r["id"] == ref {
			return r, nil
		}
		if (r["name"] == name || r["name"] == l.Prefix+name) && (engine == "" || r["engine"] == engine) {
			found = append(found, r)
		}
	}
	switch len(found) {
	case 0:
		return nil, fmt.Errorf("the website has no %s %q", what, ref)
	case 1:
		return found[0], nil
	}
	var choices []string
	for _, r := range found {
		choices = append(choices, fmt.Sprintf("%v:%s", r["engine"], name))
	}
	return nil, fmt.Errorf("the website has more than one %s %q. Give one of: %s", what, ref, strings.Join(choices, ", "))
}

// grants reads --grant DATABASE=readonly|readwrite values.
func (l dbList) grants(values []string) ([]map[string]string, error) {
	out := []map[string]string{}
	for _, v := range values {
		name, privileges, ok := strings.Cut(v, "=")
		if !ok || (privileges != "readonly" && privileges != "readwrite") {
			return nil, fmt.Errorf("--grant %s: use DATABASE=readonly or DATABASE=readwrite", v)
		}
		db, err := l.find(l.Databases, name, "database")
		if err != nil {
			return nil, err
		}
		out = append(out, map[string]string{"databaseId": fmt.Sprint(db["id"]), "privileges": privileges})
	}
	return out, nil
}

func (a *app) databases(ctx context.Context, sub string, args []string) error {
	if err := need(args, 1, "a website"); err != nil {
		return err
	}
	w, err := a.api.FindWebsite(ctx, args[0])
	if err != nil {
		return err
	}
	switch sub {
	case "list", "ls":
		out, err := a.dbList(ctx, w.ID)
		if err != nil {
			return err
		}
		if a.json {
			return a.printJSON(out)
		}
		fmt.Fprintf(a.out, "Each name starts with %s\n", out.Prefix)
		// An application of the website connects to the host name and the port.
		cols := []string{"name", "engine", "databaseServer", "host", "port", "sizeBytes", "id"}
		for _, d := range out.Databases {
			d["databaseServer"] = out.serverName(d)
			if d["engine"] == "valkey" {
				// Valkey has no size for one database. It has a database number and a number of keys.
				cols = []string{"name", "engine", "databaseServer", "host", "port", "sizeBytes", "number", "keys", "id"}
				d["sizeBytes"] = nil
			}
		}
		a.table(out.Databases, cols)
		return nil
	case "create":
		fs := flag.NewFlagSet("create", flag.ContinueOnError)
		engine := fs.String("engine", "mariadb", "mariadb, postgresql, valkey or the engine of a database pack")
		name := fs.String("name", "", "the database name, with or without the prefix of the website")
		noUser := fs.Bool("no-user", false, "do not make a user with the same name")
		server := fs.String("database-server", "", "the database server for the database. With none, the panel uses the first one of the engine with space, or makes the first one.")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if *engine == "valkey" && *noUser {
			return errors.New("a Valkey database always has a user with the same name: do not use --no-user")
		}
		body := map[string]any{"engine": *engine, "name": *name, "createUser": !*noUser}
		if *server != "" {
			l, err := a.dbList(ctx, w.ID)
			if err != nil {
				return err
			}
			s, err := l.server(*server)
			if err != nil {
				return err
			}
			body["databaseServerId"] = s["id"]
		}
		if err := a.change(ctx, http.MethodPost, "/websites/"+w.ID+"/databases", body); err != nil {
			return err
		}
		if *engine == "valkey" && !a.json {
			return a.valkeyConnection(ctx, w.ID, *name)
		}
		return nil
	case "delete", "rm":
		if err := need(args, 2, "a website and a database"); err != nil {
			return err
		}
		l, err := a.dbList(ctx, w.ID)
		if err != nil {
			return err
		}
		db, err := l.find(l.Databases, args[1], "database")
		if err != nil {
			return err
		}
		return a.change(ctx, http.MethodDelete, fmt.Sprintf("/websites/%s/databases/%v", w.ID, db["id"]), nil)
	case "adminer":
		if err := need(args, 2, "a website and a database"); err != nil {
			return err
		}
		fs := flag.NewFlagSet("adminer", flag.ContinueOnError)
		readOnly := fs.Bool("read-only", false, "the temporary database user can only read")
		if err := fs.Parse(args[2:]); err != nil {
			return err
		}
		l, err := a.dbList(ctx, w.ID)
		if err != nil {
			return err
		}
		db, err := l.find(l.Databases, args[1], "database")
		if err != nil {
			return err
		}
		privileges := "readwrite"
		if *readOnly {
			privileges = "readonly"
		}
		return a.adminer(ctx, w.ID, cell(db["id"]), privileges)
	}
	return fmt.Errorf("unknown command databases %s", sub)
}

// adminerLogin is a sign-in to Adminer (POST /websites/{id}/databases/{id}/adminer).
type adminerLogin struct {
	ID        string `json:"id"`
	Status    string `json:"status"`
	URL       string `json:"url"`
	Username  string `json:"username"`
	ExpiresAt string `json:"expiresAt"`
}

// adminer opens a database in Adminer. The panel makes a temporary database user for the
// database. Adminer has no sign-in form for a password: the browser sends the user and its
// password with a POST request. Thus the command writes a small HTML file that does this, and
// the user opens the file in a browser.
func (a *app) adminer(ctx context.Context, websiteID, databaseID, privileges string) error {
	var out struct {
		Login adminerLogin      `json:"login"`
		Form  map[string]string `json:"form"`
		Jobs  []client.Job      `json:"jobs"`
	}
	if err := a.api.Do(ctx, http.MethodPost, "/websites/"+websiteID+"/databases/"+databaseID+"/adminer", nil, map[string]any{"privileges": privileges}, &out); err != nil {
		return err
	}
	if a.json {
		return a.printJSON(out)
	}
	if a.noWait {
		fmt.Fprintf(a.out, "Sign-in %s waits for its jobs. The password of the temporary database user is in the answer with --json only.\n", out.Login.ID)
		return nil
	}
	for _, job := range out.Jobs {
		if err := a.wait(ctx, job.ID); err != nil {
			return err
		}
	}
	var state struct {
		Login adminerLogin `json:"login"`
	}
	if err := a.api.Do(ctx, http.MethodGet, "/adminer-logins/"+out.Login.ID, nil, nil, &state); err != nil {
		return err
	}
	if state.Login.Status != "ready" {
		return fmt.Errorf("adminer cannot sign in: the status of the sign-in is %s", state.Login.Status)
	}
	path, err := writeAdminerLauncher(out.Login.URL, out.Form)
	if err != nil {
		return err
	}
	fmt.Fprintf(a.out, "Adminer: %s\nTemporary database user: %s (expires %s)\n", out.Login.URL, out.Login.Username, out.Login.ExpiresAt)
	fmt.Fprintf(a.out, "Open this file in your browser to sign in: %s\nThe file has the password of the temporary database user. Remove it after you open it.\n", path)
	return nil
}

// writeAdminerLauncher writes an HTML file that sends the sign-in form to Adminer. Only the
// user can read the file.
func writeAdminerLauncher(address string, form map[string]string) (string, error) {
	var b strings.Builder
	b.WriteString("<!doctype html>\n<html lang=\"en-GB\"><meta charset=\"utf-8\"><title>Open Adminer</title>\n")
	b.WriteString("<body onload=\"document.forms[0].submit()\"><form method=\"post\" action=\"" + html.EscapeString(address) + "\">\n")
	keys := make([]string, 0, len(form))
	for k := range form {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(&b, "<input type=\"hidden\" name=\"auth[%s]\" value=\"%s\">\n", html.EscapeString(k), html.EscapeString(form[k]))
	}
	b.WriteString("<noscript><button>Open Adminer</button></noscript></form></body></html>\n")
	f, err := os.CreateTemp("", "cf-adminer-*.html")
	if err != nil {
		return "", err
	}
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		return "", err
	}
	if _, err := f.WriteString(b.String()); err != nil {
		f.Close()
		return "", err
	}
	return f.Name(), f.Close()
}

// valkeyConnection tells how an application connects to a new Valkey database. The agent gives
// the database number, so the number is known only when the job is complete.
func (a *app) valkeyConnection(ctx context.Context, websiteID, name string) error {
	l, err := a.dbList(ctx, websiteID)
	if err != nil {
		return err
	}
	for _, d := range l.Databases {
		if d["engine"] != "valkey" || (d["name"] != name && d["name"] != l.Prefix+name) {
			continue
		}
		// A panel of an earlier release does not send the host name and the port.
		host, port := "valkey.internal", "6379"
		if d["host"] != nil && d["port"] != nil {
			host, port = cell(d["host"]), cell(d["port"])
		}
		fmt.Fprintf(a.out, "Host: %s\nPort: %s\nUser: %v\n", host, port, d["name"])
		if d["number"] == nil {
			fmt.Fprintln(a.out, "The database number comes when the job is complete. Then run: cf databases list WEBSITE")
		} else {
			fmt.Fprintf(a.out, "Database number: %s\n", cell(d["number"]))
		}
		return nil
	}
	return nil
}

// databaseUsers manages database users. CF-01: a user can have a different permission on
// each database of the website, for example readonly on shop and readwrite on staging.
func (a *app) databaseUsers(ctx context.Context, sub string, args []string) error {
	if err := need(args, 1, "a website"); err != nil {
		return err
	}
	w, err := a.api.FindWebsite(ctx, args[0])
	if err != nil {
		return err
	}
	l, err := a.dbList(ctx, w.ID)
	if err != nil {
		return err
	}
	base := "/websites/" + w.ID + "/database-users"
	switch sub {
	case "list", "ls":
		if a.json {
			return a.printJSON(l.Users)
		}
		rows := make([]map[string]any, 0, len(l.Users))
		for _, u := range l.Users {
			var grants []string
			if gs, ok := u["grants"].([]any); ok {
				for _, g := range gs {
					if m, ok := g.(map[string]any); ok {
						grants = append(grants, fmt.Sprintf("%v=%v", m["database"], m["privileges"]))
					}
				}
			}
			rows = append(rows, map[string]any{"name": u["name"], "engine": u["engine"], "databaseServer": l.serverName(u), "access": u["access"], "grants": strings.Join(grants, " "), "id": u["id"]})
		}
		a.table(rows, []string{"name", "engine", "databaseServer", "grants", "access", "id"})
		return nil
	case "create":
		fs := flag.NewFlagSet("create", flag.ContinueOnError)
		engine := fs.String("engine", "mariadb", "mariadb, postgresql or the engine of a database pack. A Valkey database comes with its user.")
		name := fs.String("name", "", "the user name, with or without the prefix of the website")
		access := fs.String("access", "website", "website or anywhere")
		server := fs.String("database-server", "", "the database server of the user. It is necessary only for a user with no database, when the customer has more than one database server of the engine.")
		var grant multi
		fs.Var(&grant, "grant", "DATABASE=readonly or DATABASE=readwrite. You can give it more than one time.")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		grants, err := l.grants(grant)
		if err != nil {
			return err
		}
		body := map[string]any{"engine": *engine, "name": *name, "access": *access, "grants": grants}
		if *server != "" {
			s, err := l.server(*server)
			if err != nil {
				return err
			}
			body["databaseServerId"] = s["id"]
		}
		return a.change(ctx, http.MethodPost, base, body)
	case "set":
		if err := need(args, 2, "a website and a user"); err != nil {
			return err
		}
		u, err := l.find(l.Users, args[1], "database user")
		if err != nil {
			return err
		}
		fs := flag.NewFlagSet("set", flag.ContinueOnError)
		access := fs.String("access", "", "website or anywhere")
		reset := fs.Bool("reset-password", false, "make a new password")
		noGrants := fs.Bool("no-grants", false, "remove access to all databases")
		var grant multi
		fs.Var(&grant, "grant", "DATABASE=readonly or DATABASE=readwrite. The list replaces the permissions of the user.")
		if err := fs.Parse(args[2:]); err != nil {
			return err
		}
		body := map[string]any{}
		if *access != "" {
			body["access"] = *access
		}
		if *reset {
			body["resetPassword"] = true
		}
		if len(grant) > 0 || *noGrants {
			grants, err := l.grants(grant)
			if err != nil {
				return err
			}
			body["grants"] = grants
		}
		if len(body) == 0 {
			return errors.New("set needs --grant, --no-grants, --access or --reset-password")
		}
		return a.change(ctx, http.MethodPatch, fmt.Sprintf("%s/%v", base, u["id"]), body)
	case "delete", "rm":
		if err := need(args, 2, "a website and a user"); err != nil {
			return err
		}
		u, err := l.find(l.Users, args[1], "database user")
		if err != nil {
			return err
		}
		return a.change(ctx, http.MethodDelete, fmt.Sprintf("%s/%v", base, u["id"]), nil)
	}
	return fmt.Errorf("unknown command database-users %s", sub)
}

func (a *app) backups(ctx context.Context, sub string, args []string) error {
	if sub == "policy" {
		return a.backupPolicy(ctx, args)
	}
	if err := need(args, 1, "a website"); err != nil {
		return err
	}
	w, err := a.api.FindWebsite(ctx, args[0])
	if err != nil {
		return err
	}
	switch sub {
	case "list", "ls":
		return a.list(ctx, "/websites/"+w.ID+"/snapshots", nil, []string{"createdAt", "kind", "sizeBytes", "id"})
	case "create", "now":
		return a.change(ctx, http.MethodPost, "/websites/"+w.ID+"/snapshots", nil)
	case "restore":
		if err := need(args, 2, "a website and a snapshot ID"); err != nil {
			return err
		}
		fs := flag.NewFlagSet("restore", flag.ContinueOnError)
		scope := fs.String("scope", "website", "website, database or file")
		item := fs.String("item", "", `the database (for example "mariadb:shop") or the file path`)
		// Audit finding R8: without this option, a file that an attacker added stays.
		removeNew := fs.Bool("remove-new-files", false, "for a full website: delete the files that are not in the backup")
		if err := fs.Parse(args[2:]); err != nil {
			return err
		}
		body := map[string]any{"scope": *scope}
		if *item != "" {
			body["item"] = *item
		}
		if *removeNew {
			if *scope != "website" {
				return errors.New("--remove-new-files is only for --scope website")
			}
			body["removeNewFiles"] = true
		}
		return a.change(ctx, http.MethodPost, "/websites/"+w.ID+"/snapshots/"+url.PathEscape(args[1])+"/restore", body)
	}
	return fmt.Errorf("unknown command backups %s", sub)
}

func (a *app) jobs(ctx context.Context, sub string, args []string) error {
	switch sub {
	case "list", "ls":
		fs := flag.NewFlagSet("list", flag.ContinueOnError)
		website := fs.String("website", "", "only the jobs of this website")
		limit := limitFlags(fs)
		if err := fs.Parse(args); err != nil {
			return err
		}
		q := url.Values{}
		if *website != "" {
			w, err := a.api.FindWebsite(ctx, *website)
			if err != nil {
				return err
			}
			q.Set("websiteId", w.ID)
		}
		return a.listLimit(ctx, "/jobs", q, []string{"createdAt", "title", "state", "id"}, limit())
	case "get":
		if err := need(args, 1, "a job ID"); err != nil {
			return err
		}
		job, err := a.api.Job(ctx, args[0])
		if err != nil {
			return err
		}
		if a.json {
			return a.printJSON(job)
		}
		fmt.Fprintf(a.out, "%s\nState: %s\n", job.Title, job.State)
		for i, s := range job.Steps {
			mark := "○"
			if i < job.Step {
				mark = "✓"
			} else if i == job.Step && !job.Finished() {
				mark = "…"
			} else if i == job.Step && job.State == "failed" {
				mark = "✗"
			}
			fmt.Fprintf(a.out, "  %s %s\n", mark, s)
		}
		if job.Error != nil {
			fmt.Fprintf(a.out, "Error: %s\n", *job.Error)
		}
		return nil
	case "wait":
		if err := need(args, 1, "a job ID"); err != nil {
			return err
		}
		return a.wait(ctx, args[0])
	}
	return fmt.Errorf("unknown command jobs %s", sub)
}

func (a *app) servers(ctx context.Context, sub string, args []string) error {
	switch sub {
	case "list", "ls":
		return a.simpleList(ctx, "/servers", []string{"name", "state", "roles", "agentVersion", "rebootRequired", "id"})
	case "get", "set", "mail", "update", "reboot", "update-agent", "check", "remove", "rm":
		return a.server(ctx, sub, args)
	case "join-token":
		var out struct {
			Command   string `json:"command"`
			ExpiresAt string `json:"expiresAt"`
		}
		if err := a.api.Do(ctx, http.MethodPost, "/servers/join-tokens", nil, nil, &out); err != nil {
			return err
		}
		if a.json {
			return a.printJSON(out)
		}
		fmt.Fprintf(a.out, "Run this command on the new server. The token works once and expires at %s.\n\n  %s\n", out.ExpiresAt, out.Command)
		return nil
	}
	return fmt.Errorf("unknown command servers %s", sub)
}

func (a *app) rollouts(ctx context.Context, sub string, args []string) error {
	show := func(method, path string, body any) error {
		var out map[string]any
		if err := a.api.Do(ctx, method, path, nil, body, &out); err != nil {
			return err
		}
		if a.json {
			return a.printJSON(out)
		}
		fmt.Fprintf(a.out, "%s: %s (%s healthy, %s rolled back of %s websites)\n", cell(out["title"]), cell(out["state"]), cell(out["healthy"]), cell(out["rolledBack"]), cell(out["websites"]))
		return nil
	}
	switch sub {
	case "list", "ls":
		return a.simpleList(ctx, "/rollouts", []string{"title", "state", "healthy", "rolledBack", "websites", "id"})
	case "start":
		if err := need(args, 1, "an image ID"); err != nil {
			return err
		}
		return show(http.MethodPost, "/rollouts", map[string]string{"imageId": args[0]})
	case "pause", "resume", "roll-back":
		if err := need(args, 1, "a rollout ID"); err != nil {
			return err
		}
		return show(http.MethodPost, "/rollouts/"+url.PathEscape(args[0])+"/"+sub, nil)
	}
	return fmt.Errorf("unknown command rollouts %s", sub)
}

func (a *app) tokens(ctx context.Context, sub string, args []string) error {
	switch sub {
	case "list", "ls":
		return a.simpleList(ctx, "/tokens", []string{"id", "name", "scopes", "expiresAt", "lastUsedAt"})
	case "create":
		fs := flag.NewFlagSet("create", flag.ContinueOnError)
		name := fs.String("name", "", "a name for the token")
		scope := fs.String("scope", "", "the scopes, separated by commas, for example websites:read")
		days := fs.Int("days", 90, "the number of days until the token expires")
		totp := fs.String("totp", "", "the code from your authenticator app")
		if err := fs.Parse(args); err != nil {
			return err
		}
		// CF-02: a new token needs the password of the user.
		password, err := a.password()
		if err != nil {
			return err
		}
		body := map[string]any{"name": *name, "scopes": strings.Split(*scope, ","), "expiresInDays": *days, "password": password}
		if *totp != "" {
			body["totp"] = *totp
		}
		var out struct {
			ID        int    `json:"id"`
			Token     string `json:"token"`
			ExpiresAt string `json:"expiresAt"`
		}
		if err := a.api.Do(ctx, http.MethodPost, "/tokens", nil, body, &out); err != nil {
			return err
		}
		if a.json {
			return a.printJSON(out)
		}
		fmt.Fprintf(a.out, "Token: %s\nIt expires at %s. Copy it now. The panel does not show it again.\n", out.Token, out.ExpiresAt)
		return nil
	case "revoke":
		if err := need(args, 1, "a token ID"); err != nil {
			return err
		}
		if err := a.api.Do(ctx, http.MethodDelete, "/tokens/"+url.PathEscape(args[0]), nil, nil, nil); err != nil {
			return err
		}
		fmt.Fprintln(a.out, "The token is revoked.")
		return nil
	}
	return fmt.Errorf("unknown command tokens %s", sub)
}
