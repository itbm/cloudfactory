package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
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
  cf websites set WEBSITE [--plan ID] [--app NAME] [--vcpus N] [--memory-mb N] [--disk-gb N] [--io-mb N] [--processes N]
  cf websites delete WEBSITE
  cf websites start|stop|restart|staging WEBSITE      staging shows the passwords of the new database users once.
  cf websites retry WEBSITE                            Make a website again after the first job failed.
  cf domains add WEBSITE DOMAIN --kind redirect|alias  A new domain needs a DNS TXT record. The command shows it.
  cf domains verify WEBSITE DOMAIN                     Check the TXT record. Then the edge proxy serves the domain.
  cf domains remove WEBSITE DOMAIN
  cf domains primary WEBSITE DOMAIN                    Make a verified domain the primary domain.
  cf domains check WEBSITE DOMAIN                      Check the DNS of the domain again.
  cf runtime set WEBSITE [--image ID] [--worker-mode on|off] [--extensions a,b] [--ini key=value]...
                         [--document-root DIR]
  cf sftp get WEBSITE
  cf sftp set WEBSITE [--on|--off] [--key-file FILE]... [--no-keys]
  cf runtime revert WEBSITE
  cf databases list WEBSITE
  cf databases create WEBSITE --engine mariadb|postgresql|valkey --name NAME [--no-user]
                                                       A Valkey database has a database number, and always a user with the same name.
  cf databases delete WEBSITE DATABASE
  cf database-users list WEBSITE
  cf database-users create WEBSITE --engine mariadb|postgresql --name NAME [--grant DATABASE=readonly|readwrite]...
  cf database-users set WEBSITE USER [--grant DATABASE=readonly|readwrite]... [--no-grants]
                        [--access website|anywhere] [--reset-password]
  cf database-users delete WEBSITE USER
  cf backups list|create WEBSITE
  cf backups restore WEBSITE SNAPSHOT [--scope website|database|file] [--item ITEM] [--remove-new-files]
                       --remove-new-files is for a full website. It deletes the files that are not in the backup.
  cf backups policy
  cf backups policy password   Show the restic repository and its password for each server. Keep a copy in a safe place.
  cf backups policy set [--daily N] [--weekly N] [--monthly N] [--time HH:MM] [--target REPOSITORY]
                        [--credentials-from-env | --remove-credentials]
                       The credentials come from AWS_ACCESS_KEY_ID, B2_ACCOUNT_ID and the other restic variables.
  cf jobs list [--website WEBSITE] [--limit N | --all]
  cf jobs get|wait JOB
  cf servers list
  cf servers get SERVER
  cf servers set SERVER [--update-policy security|all|manual] [--window HH:MM-HH:MM] [--gvisor-platform systrap|kvm]
  cf servers update SERVER [--policy security|all|tools]   "tools" updates the held container tools.
  cf servers reboot SERVER
  cf servers update-agent SERVER [--version VERSION] [--allow-downgrade]
  cf servers join-token
  cf servers check SERVER                  Tests the connection from the panel to the server.
  cf servers move-databases SERVER         Moves the databases of the websites of an earlier release to the
                                           database containers of each customer. Each website stops during the copy.
  cf servers remove SERVER                 Removes a server that has no websites from the panel.
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
  cf users list
  cf users create --name NAME --email EMAIL (--customer ID | --administrator)
                       The command shows a temporary password once. The user sets a new password at the first sign-in.
  cf users reset-password USER [--remove-totp]         A USER is an email address or an ID.
  cf users delete USER
  cf plans list
  cf audit list [--limit N | --all]
  cf account password [--totp CODE]   The passwords come from CF_PASSWORD and CF_NEW_PASSWORD, or stdin.
  cf account totp setup
  cf account totp enable|disable --totp CODE
  cf version

A WEBSITE is a name (for example acme.example) or an ID. A SERVER is a name (for example web-1) or an ID. A DATABASE or USER is a name, with or without
the prefix of the website (for example shop or u1a2b3c_shop), or an ID.
Add --json to see the answer of the API. A change waits for its job; add --no-wait to return at once.
The settings CF_PANEL and CF_TOKEN replace the saved panel and token.
The panel address must start with https://. An address with http:// is accepted only for this computer (localhost).
`

type app struct {
	out, err io.Writer
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
	// The file holds a token, so only the user can read it.
	return os.WriteFile(p, b, 0o600)
}

// globalFlags removes --json and --no-wait from the arguments.
func (a *app) globalFlags(args []string) []string {
	var rest []string
	for _, arg := range args {
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
	case "databases", "database":
		return a.databases(ctx, sub, rest)
	case "database-users", "database-user", "db-users":
		return a.databaseUsers(ctx, sub, rest)
	case "backups", "backup":
		return a.backups(ctx, sub, rest)
	case "jobs", "job":
		return a.jobs(ctx, sub, rest)
	case "servers", "server":
		return a.servers(ctx, sub, rest)
	case "panel-certificate":
		return a.panelCertificate(ctx, sub, rest)
	case "images", "image":
		return a.images(ctx, sub, rest)
	case "sftp":
		return a.sftp(ctx, sub, rest)
	case "customers", "customer":
		return a.customers(ctx, sub, rest)
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
	if raw, ok := out["databaseCredentials"]; ok {
		var creds []struct {
			Engine, User, Password string
			Databases              []string
		}
		json.Unmarshal(raw, &creds)
		for _, c := range creds {
			fmt.Fprintf(a.out, "Database user %s (%s) for %s: %s\n", c.User, c.Engine, strings.Join(c.Databases, ", "), c.Password)
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
		return x
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
		status := fs.String("status", "", "starting, running, stopping, stopped or failed")
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
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		body := map[string]any{"name": args[0], "imageId": *image, "planId": *plan}
		for k, v := range map[string]string{"customerId": *customer, "serverId": *server, "application": *appName} {
			if v != "" {
				body[k] = v
			}
		}
		return a.change(ctx, http.MethodPost, "/websites", body)
	case "set":
		return a.websiteSet(ctx, args)
	case "delete", "start", "stop", "restart", "staging", "retry":
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
	case "set":
		fs := flag.NewFlagSet("set", flag.ContinueOnError)
		image := fs.String("image", "", "the image ID")
		worker := fs.String("worker-mode", "", "on or off")
		exts := fs.String("extensions", "", "the PHP extensions, separated by commas")
		root := fs.String("document-root", "", `the directory that the web server serves, for example web, or "." for the top directory`)
		var ini multi
		fs.Var(&ini, "ini", "a php.ini value as key=value")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		body := map[string]any{}
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
			return errors.New("give --image, --worker-mode, --extensions, --document-root or --ini")
		}
		return a.change(ctx, http.MethodPut, "/websites/"+w.ID+"/runtime", body)
	}
	return fmt.Errorf("unknown command runtime %s", sub)
}

type dbList struct {
	Prefix    string           `json:"prefix"`
	Databases []map[string]any `json:"databases"`
	Users     []map[string]any `json:"users"`
}

func (a *app) dbList(ctx context.Context, websiteID string) (dbList, error) {
	var out dbList
	err := a.api.Do(ctx, http.MethodGet, "/websites/"+websiteID+"/databases", nil, nil, &out)
	return out, err
}

// find gives the ID of a database or a user. The name can have the prefix of the website or not.
func (l dbList) find(rows []map[string]any, ref, what string) (map[string]any, error) {
	for _, r := range rows {
		if r["id"] == ref || r["name"] == ref || r["name"] == l.Prefix+ref {
			return r, nil
		}
	}
	return nil, fmt.Errorf("the website has no %s %q", what, ref)
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
		cols := []string{"name", "engine", "sizeBytes", "id"}
		for _, d := range out.Databases {
			if d["engine"] == "valkey" {
				// Valkey has no size for one database. It has a database number and a number of keys.
				cols = []string{"name", "engine", "sizeBytes", "number", "keys", "id"}
				d["sizeBytes"] = nil
			}
		}
		a.table(out.Databases, cols)
		return nil
	case "create":
		fs := flag.NewFlagSet("create", flag.ContinueOnError)
		engine := fs.String("engine", "mariadb", "mariadb, postgresql or valkey")
		name := fs.String("name", "", "the database name, with or without the prefix of the website")
		noUser := fs.Bool("no-user", false, "do not make a user with the same name")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if *engine == "valkey" && *noUser {
			return errors.New("a Valkey database always has a user with the same name: do not use --no-user")
		}
		if err := a.change(ctx, http.MethodPost, "/websites/"+w.ID+"/databases", map[string]any{"engine": *engine, "name": *name, "createUser": !*noUser}); err != nil {
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
	}
	return fmt.Errorf("unknown command databases %s", sub)
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
		fmt.Fprintf(a.out, "Host: valkey.internal\nPort: 6379\nUser: %v\n", d["name"])
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
			rows = append(rows, map[string]any{"name": u["name"], "engine": u["engine"], "access": u["access"], "grants": strings.Join(grants, " "), "id": u["id"]})
		}
		a.table(rows, []string{"name", "engine", "grants", "access", "id"})
		return nil
	case "create":
		fs := flag.NewFlagSet("create", flag.ContinueOnError)
		engine := fs.String("engine", "mariadb", "mariadb or postgresql. A Valkey database comes with its user.")
		name := fs.String("name", "", "the user name, with or without the prefix of the website")
		access := fs.String("access", "website", "website or anywhere")
		var grant multi
		fs.Var(&grant, "grant", "DATABASE=readonly or DATABASE=readwrite. You can give it more than one time.")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		grants, err := l.grants(grant)
		if err != nil {
			return err
		}
		return a.change(ctx, http.MethodPost, base, map[string]any{"engine": *engine, "name": *name, "access": *access, "grants": grants})
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
	case "get", "set", "update", "reboot", "update-agent", "check", "move-databases", "remove", "rm":
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
