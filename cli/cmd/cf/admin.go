package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/itbm/cloudfactory/cli/internal/client"
)

// websiteSet changes the plan, the application or the limits of a website (FR-202).
func (a *app) websiteSet(ctx context.Context, args []string) error {
	if err := need(args, 1, "a website"); err != nil {
		return err
	}
	w, err := a.api.FindWebsite(ctx, args[0])
	if err != nil {
		return err
	}
	fs := flag.NewFlagSet("set", flag.ContinueOnError)
	plan := fs.String("plan", "", "a plan ID. The website gets the limits of the plan.")
	appName := fs.String("app", "", "the application, for example WordPress")
	vcpus := fs.Float64("vcpus", 0, "the CPU limit in vCPUs")
	memory := fs.Int64("memory-mb", 0, "the memory limit in MB")
	disk := fs.Int64("disk-gb", 0, "the disk limit in GB")
	io := fs.Int64("io-mb", 0, "the disk I/O limit in MB each second")
	processes := fs.Int64("processes", 0, "the largest number of processes")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	body := map[string]any{}
	if *plan != "" {
		body["planId"] = *plan
	}
	if *appName != "" {
		body["application"] = *appName
	}
	// The API needs all limits together. A limit that is not given keeps its value.
	if *vcpus > 0 || *memory > 0 || *disk > 0 || *io > 0 || *processes > 0 {
		limits := map[string]any{}
		for k, v := range w.Limits {
			limits[k] = v
		}
		set := func(key string, v float64) {
			if v > 0 {
				limits[key] = v
			}
		}
		set("vcpus", *vcpus)
		set("memoryBytes", float64(*memory<<20))
		set("diskBytes", float64(*disk<<30))
		set("diskIoBytesPerSecond", float64(*io<<20))
		set("processes", float64(*processes))
		body["limits"] = limits
	}
	if len(body) == 0 {
		return errors.New("give --plan, --app or a limit")
	}
	return a.change(ctx, http.MethodPatch, "/websites/"+w.ID, body)
}

// credentialNames are the restic environment variables that the panel accepts
// (BackupController::CREDENTIAL_NAMES).
var credentialNames = []string{
	"AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AWS_DEFAULT_REGION",
	"B2_ACCOUNT_ID", "B2_ACCOUNT_KEY",
	"AZURE_ACCOUNT_NAME", "AZURE_ACCOUNT_KEY", "AZURE_ACCOUNT_SAS",
	"GOOGLE_PROJECT_ID", "GOOGLE_ACCESS_TOKEN",
}

// backupPolicy shows or changes the backup policy (FR-902). The credentials come from the
// environment, so that they are not in the shell history or in the process list.
func (a *app) backupPolicy(ctx context.Context, args []string) error {
	var policy map[string]any
	if err := a.api.Do(ctx, http.MethodGet, "/backup-policy", nil, nil, &policy); err != nil {
		return err
	}
	if len(args) > 0 && args[0] == "password" {
		// Audit finding S2: each server has its own restic repository and password. Keep a
		// copy in a safe place: without it, nobody can restore the backups of that server.
		var out struct {
			Repositories []map[string]any `json:"repositories"`
			Shared       map[string]any   `json:"shared"`
		}
		if err := a.api.Do(ctx, http.MethodGet, "/backup-policy/repository-password", nil, nil, &out); err != nil {
			return err
		}
		if a.json {
			return a.printJSON(out)
		}
		rows := out.Repositories
		if out.Shared != nil {
			out.Shared["server"] = "(all servers, earlier version)"
			rows = append(rows, out.Shared)
		}
		a.table(rows, []string{"server", "repository", "password"})
		return nil
	}
	if len(args) == 0 || args[0] != "set" {
		if a.json {
			return a.printJSON(policy)
		}
		fmt.Fprintf(a.out, "Keep:        %s daily, %s weekly, %s monthly\nTime (UTC):  %s\nTarget:      %s\nCredentials: %s\n",
			cell(policy["daily"]), cell(policy["weekly"]), cell(policy["monthly"]), cell(policy["time"]), cell(policy["target"]), cell(policy["credentialNames"]))
		return nil
	}
	fs := flag.NewFlagSet("policy set", flag.ContinueOnError)
	daily := fs.Int("daily", -1, "the number of daily snapshots to keep")
	weekly := fs.Int("weekly", -1, "the number of weekly snapshots to keep")
	monthly := fs.Int("monthly", -1, "the number of monthly snapshots to keep")
	at := fs.String("time", "", "the time of the nightly backups, in UTC, as HH:MM")
	target := fs.String("target", "", `the restic repository, for example "s3:s3.amazonaws.com/bucket/path"`)
	fromEnv := fs.Bool("credentials-from-env", false, "send the credentials from the environment, for example AWS_ACCESS_KEY_ID")
	remove := fs.Bool("remove-credentials", false, "remove the credentials")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	body := map[string]any{}
	for _, k := range []string{"daily", "weekly", "monthly", "time", "target"} {
		body[k] = policy[k]
	}
	for k, v := range map[string]int{"daily": *daily, "weekly": *weekly, "monthly": *monthly} {
		if v >= 0 {
			body[k] = v
		}
	}
	if *at != "" {
		body["time"] = *at
	}
	if *target != "" {
		body["target"] = *target
	}
	switch {
	case *remove:
		body["credentials"] = map[string]string{}
	case *fromEnv:
		creds := map[string]string{}
		for _, k := range credentialNames {
			if v := os.Getenv(k); v != "" {
				creds[k] = v
			}
		}
		if len(creds) == 0 {
			return fmt.Errorf("set the credentials in the environment first, for example AWS_ACCESS_KEY_ID; the names are %s", strings.Join(credentialNames, ", "))
		}
		body["credentials"] = creds
	}
	var out map[string]any
	if err := a.api.Do(ctx, http.MethodPut, "/backup-policy", nil, body, &out); err != nil {
		return err
	}
	if a.json {
		return a.printJSON(out)
	}
	fmt.Fprintln(a.out, "The backup policy is saved.")
	return nil
}

// findServer gets a server by its ID or by its name.
func (a *app) findServer(ctx context.Context, ref string) (map[string]any, error) {
	rows, err := client.All[map[string]any](ctx, a.api, "/servers", nil)
	if err != nil {
		return nil, err
	}
	for _, s := range rows {
		if s["id"] == ref || s["name"] == ref {
			return s, nil
		}
	}
	return nil, fmt.Errorf("there is no server %s", ref)
}

// server shows or changes one server (FR-1111 to FR-1113).
func (a *app) server(ctx context.Context, sub string, args []string) error {
	if err := need(args, 1, "a server"); err != nil {
		return err
	}
	s, err := a.findServer(ctx, args[0])
	if err != nil {
		return err
	}
	path := fmt.Sprintf("/servers/%v", s["id"])
	fs := flag.NewFlagSet(sub, flag.ContinueOnError)
	switch sub {
	case "get":
		if a.json {
			return a.printJSON(s)
		}
		for _, k := range []string{"name", "id", "state", "roles", "agentAddress", "os", "kernel", "agentVersion", "podmanVersion", "gvisorVersion", "gvisorPlatform", "updatePolicy", "maintenanceWindow", "rebootRequired"} {
			fmt.Fprintf(a.out, "%-18s %s\n", k+":", cell(s[k]))
		}
		a.connection(s)
		if updates, ok := s["pendingUpdates"].([]any); ok && len(updates) > 0 {
			fmt.Fprintln(a.out, "Pending updates:")
			for _, u := range updates {
				m, _ := u.(map[string]any)
				note := ""
				if m["security"] == true {
					note = " (security)"
				}
				if m["containerTool"] == true {
					note = " (container tool: use cf servers update SERVER --policy tools)"
				}
				fmt.Fprintf(a.out, "  %s %s -> %s%s\n", cell(m["name"]), cell(m["from"]), cell(m["to"]), note)
			}
		}
		return nil
	case "set":
		policy := fs.String("update-policy", "", "security, all or manual")
		window := fs.String("window", "", "the maintenance window, for example 02:00-05:00")
		platform := fs.String("gvisor-platform", "", "systrap or kvm")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		body := map[string]any{}
		for k, v := range map[string]string{"updatePolicy": *policy, "maintenanceWindow": *window, "gvisorPlatform": *platform} {
			if v != "" {
				body[k] = v
			}
		}
		if len(body) == 0 {
			return errors.New("give --update-policy, --window or --gvisor-platform")
		}
		return a.change(ctx, http.MethodPatch, path, body)
	case "update":
		policy := fs.String("policy", "", `security, all, or tools for the held container tools. The default is the policy of the server.`)
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		var body any
		if *policy != "" {
			body = map[string]string{"policy": *policy}
		}
		return a.change(ctx, http.MethodPost, path+"/install-updates", body)
	case "reboot":
		return a.change(ctx, http.MethodPost, path+"/reboot", nil)
	case "update-agent":
		version := fs.String("version", "", "the agent version. The default is the newest release.")
		downgrade := fs.Bool("allow-downgrade", false, "allow an older version")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		body := map[string]any{}
		if *version != "" {
			body["version"] = *version
		}
		if *downgrade {
			body["allowDowngrade"] = true
		}
		return a.change(ctx, http.MethodPost, path+"/update-agent", body)
	case "check":
		// The panel connects to the agent now and gives the server with the result.
		var out map[string]any
		if err := a.api.Do(ctx, http.MethodPost, path+"/check-connection", nil, nil, &out); err != nil {
			return err
		}
		if a.json {
			return a.printJSON(out)
		}
		if reason, _ := out["connectionError"].(string); reason != "" {
			a.connection(out)
			return fmt.Errorf("the panel cannot connect to %s", cell(out["name"]))
		}
		fmt.Fprintf(a.out, "The panel can connect to %s at %s.\n", cell(out["name"]), cell(out["agentAddress"]))
		return nil
	case "remove", "rm":
		// The panel refuses a server that has websites, and the server of the panel.
		if err := a.api.Do(ctx, http.MethodDelete, path, nil, nil, nil); err != nil {
			return err
		}
		fmt.Fprintf(a.out, "The server %s is removed from the panel. The panel did not change the server itself.\n", cell(s["name"]))
		return nil
	}
	return fmt.Errorf("unknown command servers %s", sub)
}

// connection prints the reason when the panel cannot connect to the agent of a server. Then
// the jobs of the server fail.
func (a *app) connection(s map[string]any) {
	reason, _ := s["connectionError"].(string)
	if reason == "" {
		return
	}
	fmt.Fprintf(a.out, "The panel cannot connect to this server at %s: %s\n", cell(s["agentAddress"]), reason)
	fmt.Fprintln(a.out, "Make sure that the panel can find this address and that port 7443 of the server is open for the panel.")
	fmt.Fprintln(a.out, "If the address is wrong, remove the server (cf servers remove SERVER) and join it again with --address.")
}

// images lists, adds and changes runtime images (FR-307, FR-1110).
func (a *app) images(ctx context.Context, sub string, args []string) error {
	fs := flag.NewFlagSet(sub, flag.ContinueOnError)
	var command multi
	port := fs.Int("port", 0, "custom image: the HTTP port in the container. The default is 8080.")
	fs.Var(&command, "arg", "custom image: one argument of the start command. Give it one time for each argument.")
	policy := fs.String("policy", "", "the update policy: automatic, security_automatic or manual")
	switch sub {
	case "list", "ls":
		return a.list(ctx, "/images", nil, []string{"id", "name", "tag", "availableTag", "policy", "signatureVerified", "containerPort"})
	case "add":
		name := fs.String("name", "", "a name for the image")
		reference := fs.String("reference", "", `the image, for example "registry.example.com/team/app"`)
		tag := fs.String("tag", "latest", "the tag")
		if err := fs.Parse(args); err != nil {
			return err
		}
		if *policy == "" {
			*policy = "manual"
		}
		body := map[string]any{"name": *name, "reference": *reference, "tag": *tag, "policy": *policy}
		if *port > 0 {
			body["containerPort"] = *port
		}
		if len(command) > 0 {
			body["command"] = []string(command)
		}
		return a.show(ctx, http.MethodPost, "/images", body)
	case "set":
		if err := need(args, 1, "an image ID"); err != nil {
			return err
		}
		noCommand := fs.Bool("no-command", false, "custom image: use the command of the image")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		body := map[string]any{}
		if *policy != "" {
			body["policy"] = *policy
		}
		if *port > 0 {
			body["containerPort"] = *port
		}
		if len(command) > 0 {
			body["command"] = []string(command)
		}
		if *noCommand {
			body["command"] = nil
		}
		if len(body) == 0 {
			return errors.New("give --policy, --port, --arg or --no-command")
		}
		return a.show(ctx, http.MethodPatch, "/images/"+url.PathEscape(args[0]), body)
	case "check":
		if err := need(args, 1, "an image ID"); err != nil {
			return err
		}
		return a.show(ctx, http.MethodPost, "/images/"+url.PathEscape(args[0])+"/check", nil)
	}
	return fmt.Errorf("unknown command images %s", sub)
}

// show sends a request and prints the main fields of the answer.
func (a *app) show(ctx context.Context, method, path string, body any) error {
	var out map[string]any
	if err := a.api.Do(ctx, method, path, nil, body, &out); err != nil {
		return err
	}
	if data, ok := out["data"].(map[string]any); ok {
		out = data
	}
	if a.json {
		return a.printJSON(out)
	}
	for _, k := range []string{"id", "name", "tag", "availableTag", "policy", "containerPort", "command", "checkError"} {
		if v, ok := out[k]; ok {
			fmt.Fprintf(a.out, "%-14s %s\n", k+":", cell(v))
		}
	}
	return nil
}

// sftp shows or sets the SFTP access of a website (spec section 10).
func (a *app) sftp(ctx context.Context, sub string, args []string) error {
	if err := need(args, 1, "a website"); err != nil {
		return err
	}
	w, err := a.api.FindWebsite(ctx, args[0])
	if err != nil {
		return err
	}
	if w.Sftp == nil {
		return errors.New("this panel has no SFTP")
	}
	switch sub {
	case "get":
		if a.json {
			return a.printJSON(w.Sftp)
		}
		host := "-"
		if w.Sftp.Host != nil {
			host = *w.Sftp.Host
		}
		fmt.Fprintf(a.out, "SFTP:     %s\nAddress:  sftp://%s@%s:%d\nSSH keys: %d (SFTP accepts keys only)\n", map[bool]string{true: "on", false: "off"}[w.Sftp.Enabled], w.Sftp.Username, host, w.Sftp.Port, len(w.Sftp.AuthorizedKeys))
		for _, k := range w.Sftp.AuthorizedKeys {
			fmt.Fprintf(a.out, "  %s\n", k)
		}
		return nil
	case "set":
		fs := flag.NewFlagSet("set", flag.ContinueOnError)
		on := fs.Bool("on", false, "turn SFTP on")
		off := fs.Bool("off", false, "turn SFTP off")
		noKeys := fs.Bool("no-keys", false, "remove all SSH keys")
		var keyFiles multi
		fs.Var(&keyFiles, "key-file", "a file with one or more OpenSSH public keys, for example ~/.ssh/id_ed25519.pub. The keys replace the old keys.")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if *on && *off {
			return errors.New("give --on or --off, not both")
		}
		body := map[string]any{"enabled": w.Sftp.Enabled || *on}
		if *off {
			body["enabled"] = false
		}
		if *noKeys {
			body["authorizedKeys"] = []string{}
		}
		if len(keyFiles) > 0 {
			var keys []string
			for _, f := range keyFiles {
				b, err := os.ReadFile(f)
				if err != nil {
					return err
				}
				for _, line := range strings.Split(string(b), "\n") {
					if line = strings.TrimSpace(line); line != "" && !strings.HasPrefix(line, "#") {
						keys = append(keys, line)
					}
				}
			}
			body["authorizedKeys"] = keys
		}
		return a.change(ctx, http.MethodPut, "/websites/"+w.ID+"/sftp", body)
	}
	return fmt.Errorf("unknown command sftp %s", sub)
}

// customers lists, adds and changes customers (FR-102).
func (a *app) customers(ctx context.Context, sub string, args []string) error {
	switch sub {
	case "list", "ls":
		return a.list(ctx, "/customers", nil, []string{"name", "email", "role", "planId", "suspended", "id"})
	case "create":
		fs := flag.NewFlagSet("create", flag.ContinueOnError)
		name := fs.String("name", "", "the name of the customer")
		email := fs.String("email", "", "the email address")
		plan := fs.String("plan", "", "the plan ID")
		role := fs.String("role", "owner", "owner or reseller")
		// Audit finding B5: the panel sends no email, so it makes the first user here.
		withUser := fs.Bool("with-user", false, "also make a user with the email of the customer")
		if err := fs.Parse(args); err != nil {
			return err
		}
		body := map[string]any{"name": *name, "email": *email, "planId": *plan, "role": *role}
		if *withUser {
			body["createUser"] = true
		}
		var out map[string]any
		if err := a.api.Do(ctx, http.MethodPost, "/customers", nil, body, &out); err != nil {
			return err
		}
		if a.json {
			return a.printJSON(out)
		}
		fmt.Fprintf(a.out, "Added customer %s (%s).\n", cell(out["name"]), cell(out["id"]))
		if user, ok := out["user"].(map[string]any); ok {
			a.temporaryPassword(user)
		} else {
			fmt.Fprintln(a.out, "The customer has no user and cannot sign in. Add one with: cf users create")
		}
		return nil
	case "set":
		if err := need(args, 1, "a customer ID"); err != nil {
			return err
		}
		fs := flag.NewFlagSet("set", flag.ContinueOnError)
		plan := fs.String("plan", "", "a plan ID")
		suspend := fs.Bool("suspend", false, "suspend the customer. The panel stops its websites.")
		unsuspend := fs.Bool("unsuspend", false, "lift the suspension")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		body := map[string]any{}
		if *plan != "" {
			body["planId"] = *plan
		}
		if *suspend || *unsuspend {
			if *suspend && *unsuspend {
				return errors.New("give --suspend or --unsuspend, not both")
			}
			body["suspended"] = *suspend
		}
		if len(body) == 0 {
			return errors.New("give --plan, --suspend or --unsuspend")
		}
		if err := a.api.Do(ctx, http.MethodPatch, "/customers/"+url.PathEscape(args[0]), nil, body, nil); err != nil {
			return err
		}
		fmt.Fprintln(a.out, "The customer is changed.")
		return nil
	}
	return fmt.Errorf("unknown command customers %s", sub)
}

// temporaryPassword shows the temporary password of a new user or of a reset, one time.
func (a *app) temporaryPassword(user map[string]any) {
	fmt.Fprintf(a.out, "Temporary password for %s: %s\nCopy it now. The panel does not show it again and sends no email. The user sets a new password at the first sign-in.\n", cell(user["email"]), cell(user["temporaryPassword"]))
}

// users lists, adds, resets and deletes the users who can sign in (audit finding B5).
func (a *app) users(ctx context.Context, sub string, args []string) error {
	switch sub {
	case "list", "ls":
		return a.simpleList(ctx, "/users", []string{"name", "email", "role", "customerId", "totpEnabled", "mustChangePassword", "id"})
	case "create":
		fs := flag.NewFlagSet("create", flag.ContinueOnError)
		name := fs.String("name", "", "the name of the user")
		email := fs.String("email", "", "the email address. The user signs in with it.")
		customer := fs.String("customer", "", "the ID of the customer of the user")
		admin := fs.Bool("administrator", false, "make an administrator, who has no customer")
		totp := fs.String("totp", "", "the code from your authenticator app, for a new administrator")
		if err := fs.Parse(args); err != nil {
			return err
		}
		if (*customer == "") != *admin {
			return errors.New("give --customer ID or --administrator")
		}
		// customerId is null for an administrator.
		body := map[string]any{"name": *name, "email": *email, "customerId": nil}
		if *customer != "" {
			body["customerId"] = *customer
		} else if err := a.confirm(body, *totp); err != nil {
			return err
		}
		var out map[string]any
		if err := a.api.Do(ctx, http.MethodPost, "/users", nil, body, &out); err != nil {
			return err
		}
		if a.json {
			return a.printJSON(out)
		}
		fmt.Fprintf(a.out, "Added user %s (%s, ID %s).\n", cell(out["email"]), cell(out["role"]), cell(out["id"]))
		a.temporaryPassword(out)
		return nil
	case "reset-password", "delete", "rm":
		if err := need(args, 1, "the email address or the ID of a user"); err != nil {
			return err
		}
		fs := flag.NewFlagSet(sub, flag.ContinueOnError)
		removeTotp := fs.Bool("remove-totp", false, "also remove TOTP, for a user who lost the authenticator app")
		totp := fs.String("totp", "", "the code from your authenticator app, for a change to an administrator")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		id, administrator, err := a.userRef(ctx, args[0])
		if err != nil {
			return err
		}
		body := map[string]any{}
		if administrator {
			if err := a.confirm(body, *totp); err != nil {
				return err
			}
		}
		if sub != "reset-password" {
			// A request with no values has no body.
			var send any
			if len(body) > 0 {
				send = body
			}
			if err := a.api.Do(ctx, http.MethodDelete, "/users/"+id, nil, send, nil); err != nil {
				return err
			}
			fmt.Fprintln(a.out, "The user is deleted.")
			return nil
		}
		if *removeTotp {
			body["removeTotp"] = true
		}
		var out map[string]any
		if err := a.api.Do(ctx, http.MethodPost, "/users/"+id+"/reset-password", nil, body, &out); err != nil {
			return err
		}
		if a.json {
			return a.printJSON(out)
		}
		fmt.Fprintln(a.out, "The password is reset. The panel signed the user out and deleted the API tokens of the user.")
		a.temporaryPassword(out)
		return nil
	}
	return fmt.Errorf("unknown command users %s", sub)
}

// confirm adds the password of the signed-in user, and the TOTP code when there is one, to the
// body of a change to an administrator. The panel refuses the change with no password (audit
// finding F09). The password comes from CF_PASSWORD or stdin.
func (a *app) confirm(body map[string]any, totp string) error {
	fmt.Fprintln(a.err, "A change to an administrator needs your password, and your TOTP code (--totp) when TOTP is on.")
	password, err := a.password()
	if err != nil {
		return err
	}
	body["password"] = password
	if totp != "" {
		body["totp"] = totp
	}
	return nil
}

// userRef gives the ID of a user from an ID or an email address, and reports whether the user
// is an administrator. A user with an ID that the list does not have is not an administrator
// for this command: the panel gives the error.
func (a *app) userRef(ctx context.Context, ref string) (id string, administrator bool, err error) {
	byEmail := strings.Contains(ref, "@")
	if !byEmail {
		if _, err := strconv.Atoi(ref); err != nil {
			return "", false, fmt.Errorf("%q is not an email address or the ID of a user", ref)
		}
	}
	var out struct {
		Data []struct {
			ID    int    `json:"id"`
			Email string `json:"email"`
			Role  string `json:"role"`
		} `json:"data"`
	}
	if err := a.api.Do(ctx, http.MethodGet, "/users", nil, nil, &out); err != nil {
		return "", false, err
	}
	for _, u := range out.Data {
		if (byEmail && strings.EqualFold(u.Email, ref)) || (!byEmail && strconv.Itoa(u.ID) == ref) {
			return strconv.Itoa(u.ID), u.Role == "administrator", nil
		}
	}
	if byEmail {
		return "", false, fmt.Errorf("there is no user %s", ref)
	}
	return ref, false, nil
}

func (a *app) plans(ctx context.Context, sub string) error {
	if sub != "list" && sub != "ls" {
		return fmt.Errorf("unknown command plans %s", sub)
	}
	return a.list(ctx, "/plans", nil, []string{"name", "websites", "databases", "mailboxes", "id"})
}

func (a *app) audit(ctx context.Context, sub string, args []string) error {
	if sub != "list" && sub != "ls" {
		return fmt.Errorf("unknown command audit %s", sub)
	}
	fs := flag.NewFlagSet("list", flag.ContinueOnError)
	limit := limitFlags(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	return a.listLimit(ctx, "/audit", nil, []string{"at", "actor", "ip", "action", "target"}, limit())
}

// account changes the password and TOTP of the signed-in user (R-01). The passwords come from
// CF_PASSWORD and CF_NEW_PASSWORD, or from stdin, one on each line.
func (a *app) account(ctx context.Context, sub string, args []string) error {
	fs := flag.NewFlagSet(sub, flag.ContinueOnError)
	code := fs.String("totp", "", "the code from your authenticator app")
	switch sub {
	case "password":
		if err := fs.Parse(args); err != nil {
			return err
		}
		current, err := a.password()
		if err != nil {
			return err
		}
		next, err := a.secret("CF_NEW_PASSWORD", "New password: ")
		if err != nil {
			return err
		}
		body := map[string]any{"currentPassword": current, "password": next, "password_confirmation": next}
		if *code != "" {
			body["totp"] = *code
		}
		if err := a.api.Do(ctx, http.MethodPut, "/me/password", nil, body, nil); err != nil {
			return err
		}
		fmt.Fprintln(a.out, "The password is changed.")
		return nil
	case "totp":
		if err := need(args, 1, "setup, enable or disable"); err != nil {
			return err
		}
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		switch args[0] {
		case "setup":
			var out struct{ Secret, URI string }
			if err := a.api.Do(ctx, http.MethodPost, "/me/totp/setup", nil, nil, &out); err != nil {
				return err
			}
			fmt.Fprintf(a.out, "Add this key to your authenticator app:\n  %s\n  %s\nThen run: cf account totp enable --totp CODE\n", out.Secret, out.URI)
			return nil
		case "enable", "disable":
			if *code == "" {
				return errors.New("give --totp CODE")
			}
			password, err := a.password()
			if err != nil {
				return err
			}
			if err := a.api.Do(ctx, http.MethodPost, "/me/totp/"+args[0], nil, map[string]string{"password": password, "code": *code}, nil); err != nil {
				return err
			}
			fmt.Fprintf(a.out, "TOTP is %s.\n", map[string]string{"enable": "on", "disable": "off"}[args[0]])
			return nil
		}
	}
	return fmt.Errorf("unknown command account %s", sub)
}

// panelCertificate shows or changes the TLS certificate of the panel domain.
func (a *app) panelCertificate(ctx context.Context, sub string, args []string) error {
	switch sub {
	case "show", "get":
		var out map[string]any
		if err := a.api.Do(ctx, http.MethodGet, "/panel-certificate", nil, nil, &out); err != nil {
			return err
		}
		if a.json {
			return a.printJSON(out)
		}
		for _, k := range []string{"domain", "method", "state", "expiresAt", "caSha256", "letsEncryptAvailable", "serversWithoutCa"} {
			fmt.Fprintf(a.out, "%-21s %s\n", k+":", cell(out[k]))
		}
		return nil
	case "ca":
		var out struct {
			CACertificate *string `json:"caCertificate"`
		}
		if err := a.api.Do(ctx, http.MethodGet, "/panel-certificate", nil, nil, &out); err != nil {
			return err
		}
		if out.CACertificate == nil {
			return errors.New("the panel has no CA yet")
		}
		fmt.Fprint(a.out, *out.CACertificate)
		return nil
	case "set":
		if err := need(args, 1, "a method: internal or letsencrypt"); err != nil {
			return err
		}
		return a.change(ctx, http.MethodPut, "/panel-certificate", map[string]string{"method": args[0]})
	}
	return fmt.Errorf("unknown command panel-certificate %s", sub)
}
