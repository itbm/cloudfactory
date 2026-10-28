package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"net/url"
	"os"
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
		for _, k := range []string{"name", "id", "state", "roles", "os", "kernel", "agentVersion", "podmanVersion", "gvisorVersion", "gvisorPlatform", "updatePolicy", "maintenanceWindow", "rebootRequired"} {
			fmt.Fprintf(a.out, "%-18s %s\n", k+":", cell(s[k]))
		}
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
	}
	return fmt.Errorf("unknown command servers %s", sub)
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
		if err := fs.Parse(args); err != nil {
			return err
		}
		var out map[string]any
		if err := a.api.Do(ctx, http.MethodPost, "/customers", nil, map[string]string{"name": *name, "email": *email, "planId": *plan, "role": *role}, &out); err != nil {
			return err
		}
		if a.json {
			return a.printJSON(out)
		}
		fmt.Fprintf(a.out, "Added customer %s (%s).\n", cell(out["name"]), cell(out["id"]))
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
