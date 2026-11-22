package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strings"
)

// The name of a command of a plugin: lower-case letters, digits and "-". "cf NAME" runs the
// program "cf-NAME" from the PATH.
var pluginCommandName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,39}$`)

// builtin has the first word of each command of cf. A plugin cannot replace one of them.
var builtin = map[string]bool{}

func init() {
	for _, c := range []string{
		"help", "version", "login", "use", "logout", "websites", "website", "domains", "domain", "runtime", "database-servers",
		"database-server", "db-servers", "databases", "database", "database-users", "database-user", "db-users", "dns", "networks",
		"network", "backups", "backup", "jobs", "job", "servers", "server", "mesh", "panel-certificate", "images", "image", "sftp",
		"customers", "customer", "users", "user", "plans", "plan", "audit", "account", "rollouts", "rollout", "tokens", "token",
		"files", "file", "logs", "cron", "deploys", "deploy", "plugins", "plugin", "packs", "pack", "wordpress", "wp",
		// Adversarial review, finding C1-02: these commands were not in the list.
		"env", "mail", "email", "mail-server", "notifications", "notification",
		"imports", "import",
	} {
		builtin[c] = true
	}
}

// runExternal runs "cf-NAME" from the PATH for a command that cf does not have, as Git does.
// The program gets the other arguments, and CF_PANEL and CF_TOKEN of the saved sign-in. The
// program runs as your user, so it can already read the saved sign-in; the variables only
// make it easier. found is false when there is no such program.
func (a *app) runExternal(ctx context.Context, args []string) (found bool, err error) {
	if !pluginCommandName.MatchString(args[0]) || builtin[args[0]] {
		return false, nil
	}
	path, err := exec.LookPath("cf-" + args[0])
	if err != nil {
		return false, nil
	}
	cmd := exec.CommandContext(ctx, path, args[1:]...)
	cmd.Stdout, cmd.Stderr = a.raw, a.rawErr
	cmd.Stdin = a.in
	if cmd.Stdin == nil {
		cmd.Stdin = os.Stdin
	}
	env := os.Environ()
	if s := loadSettings(); s.Panel != "" && s.Token != "" {
		env = append(env, "CF_PANEL="+s.Panel, "CF_TOKEN="+s.Token)
	}
	cmd.Env = env
	if err := cmd.Run(); err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			// The exit status of the program becomes the exit status of cf.
			return true, &exitError{code: exit.ExitCode(), text: fmt.Sprintf("cf-%s stopped with exit code %d", args[0], exit.ExitCode())}
		}
		return true, err
	}
	return true, nil
}

// exitError is an error with the exit status that cf gives.
type exitError struct {
	code int
	text string
}

func (e *exitError) Error() string { return e.text }

// findPlugin gets an installed plugin by its slug or its ID.
func (a *app) findPlugin(ctx context.Context, ref string) (map[string]any, error) {
	var out struct {
		Data []map[string]any `json:"data"`
	}
	if err := a.api.Do(ctx, http.MethodGet, "/plugins", nil, nil, &out); err != nil {
		return nil, err
	}
	for _, p := range out.Data {
		if p["id"] == ref || p["slug"] == strings.ToLower(ref) {
			return p, nil
		}
	}
	return nil, fmt.Errorf("there is no plugin %s; see cf plugins list", ref)
}

// readManifest reads plugin.json and its signature. With no --signature, the signature is the
// file with the same name and ".sig" at the end, when it exists.
func readManifest(manifestPath, signaturePath string) (manifest, signature string, err error) {
	b, err := os.ReadFile(manifestPath)
	if err != nil {
		return "", "", fmt.Errorf("read the manifest: %w", err)
	}
	if signaturePath == "" {
		if _, err := os.Stat(manifestPath + ".sig"); err == nil {
			signaturePath = manifestPath + ".sig"
		}
	}
	if signaturePath != "" {
		s, err := os.ReadFile(signaturePath)
		if err != nil {
			return "", "", fmt.Errorf("read the signature: %w", err)
		}
		signature = strings.TrimSpace(string(s))
	}
	return string(b), signature, nil
}

// confirmPlugin adds the password, and the TOTP code when there is one, to a change that gives
// a plugin access. The password comes from CF_PASSWORD or stdin.
func (a *app) confirmPlugin(body map[string]any, totp string) error {
	fmt.Fprintln(a.err, "This change needs your password, and your TOTP code (--totp) when TOTP is on.")
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

func (a *app) printPlugin(p map[string]any) error {
	if a.json {
		return a.printJSON(p)
	}
	signed := "no signature (developer mode)"
	if k, ok := p["signedBy"].(map[string]any); ok {
		signed = "signed by " + cell(k["name"])
	}
	state := "on"
	if p["enabled"] != true {
		state = "off"
	}
	fmt.Fprintf(a.out, "Name:          %s %s (%s)\nID:            %s\nPublisher:     %s, %s\nState:         %s\nAddress:       %s\nScopes:        %s\nEvents:        %s\nWebhook:       %s\nToken expires: %s\n",
		cell(p["name"]), cell(p["version"]), cell(p["slug"]), cell(p["id"]), cell(p["publisher"]), signed, state, cell(p["baseUrl"]),
		cell(p["scopes"]), cell(p["events"]), cell(p["webhookUrl"]), cell(p["tokenExpiresAt"]))
	if h, ok := p["hosting"].(map[string]any); ok {
		image, _ := p["image"].(map[string]any)
		fmt.Fprintf(a.out, "Container:     %s on %s (%s)\n", cell(image["ref"]), cell(h["serverName"]), cell(h["status"]))
	}
	if pages, _ := p["pages"].([]any); len(pages) > 0 {
		for _, x := range pages {
			pg, _ := x.(map[string]any)
			fmt.Fprintf(a.out, "Page:          %s (%s) %s\n", cell(pg["title"]), cell(pg["roles"]), cell(pg["url"]))
		}
	}
	if actions, _ := p["actions"].([]any); len(actions) > 0 {
		for _, x := range actions {
			ac, _ := x.(map[string]any)
			fmt.Fprintf(a.out, "Action:        %s (on each %s, for %s)\n", cell(ac["title"]), cell(ac["target"]), cell(ac["roles"]))
		}
	}
	if hooks, _ := p["hooks"].([]any); len(hooks) > 0 {
		for _, x := range hooks {
			h, _ := x.(map[string]any)
			fmt.Fprintf(a.out, "Before hook:   %s (when the plugin does not answer: %s)\n", cell(h["event"]), cell(h["onError"]))
		}
	}
	if cmds, _ := p["commands"].([]any); len(cmds) > 0 {
		for _, x := range cmds {
			c, _ := x.(map[string]any)
			fmt.Fprintf(a.out, "Command:       cf %s  %s (install cf-%s from the publisher)\n", cell(c["name"]), cell(c["description"]), cell(c["name"]))
		}
	}
	return nil
}

func (a *app) printCredentials(out map[string]any) {
	if a.json {
		a.printJSON(out)
		return
	}
	p, _ := out["plugin"].(map[string]any)
	if out["token"] == nil {
		fmt.Fprintf(a.out, "Plugin %s is ready. The panel gives the new token and webhook secret to its container.\n", cell(p["name"]))
		return
	}
	fmt.Fprintf(a.out, "Plugin %s is ready.\nCF_TOKEN=%s\nCF_WEBHOOK_SECRET=%s\nGive these values to the plugin now. The panel does not show them again.\n",
		cell(p["name"]), cell(out["token"]), cell(out["webhookSecret"]))
}

// plugins manages the plugins: programs outside the panel with their own token. Only an
// administrator can use these commands.
func (a *app) plugins(ctx context.Context, sub string, args []string) error {
	switch sub {
	case "list", "ls":
		var out struct {
			Data []map[string]any `json:"data"`
		}
		if err := a.api.Do(ctx, http.MethodGet, "/plugins", nil, nil, &out); err != nil {
			return err
		}
		if a.json {
			return a.printJSON(out.Data)
		}
		for _, p := range out.Data {
			p["signed"] = p["signedBy"] != nil
		}
		a.table(out.Data, []string{"slug", "name", "version", "enabled", "signed", "scopes", "id"})
		return nil
	case "install":
		fs := flag.NewFlagSet("install", flag.ContinueOnError)
		fs.SetOutput(a.err)
		manifest := fs.String("manifest", "plugin.json", "the manifest file")
		signature := fs.String("signature", "", "the signature file. Default: the manifest file with .sig")
		address := fs.String("address", "", "where you run the plugin, for example https://uptime.example.net")
		hosted := fs.Bool("hosted", false, "the panel runs the image of the plugin in a gVisor container")
		server := fs.String("server", "", "with --hosted: the ID of the server. Default: the panel server")
		host := fs.String("host", "", "with --hosted: the host name. Default: SLUG.plugins and the panel domain")
		totp := fs.String("totp", "", "the code from your authenticator app")
		if err := fs.Parse(args); err != nil {
			return err
		}
		if *hosted == (*address != "") {
			return errors.New("give --address, the address where you run the plugin, or --hosted")
		}
		if !*hosted && (*server != "" || *host != "") {
			return errors.New("--server and --host need --hosted")
		}
		m, s, err := readManifest(*manifest, *signature)
		if err != nil {
			return err
		}
		body := map[string]any{"manifest": m, "baseUrl": *address}
		if *hosted {
			body = map[string]any{"manifest": m, "hosted": true, "serverId": nullable(*server), "host": nullable(*host)}
		}
		if s != "" {
			body["signature"] = s
		}
		if err := a.confirmPlugin(body, *totp); err != nil {
			return err
		}
		var out map[string]any
		if err := a.api.Do(ctx, http.MethodPost, "/plugins", nil, body, &out); err != nil {
			return err
		}
		a.printCredentials(out)
		return nil
	case "keys", "key":
		return a.pluginKeys(ctx, args)
	case "developer-mode":
		if err := need(args, 1, "on or off"); err != nil {
			return err
		}
		fs := flag.NewFlagSet("developer-mode", flag.ContinueOnError)
		fs.SetOutput(a.err)
		totp := fs.String("totp", "", "the code from your authenticator app")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if args[0] != "on" && args[0] != "off" {
			return errors.New("give on or off")
		}
		body := map[string]any{"developerMode": args[0] == "on"}
		if args[0] == "on" {
			if err := a.confirmPlugin(body, *totp); err != nil {
				return err
			}
		}
		if err := a.api.Do(ctx, http.MethodPut, "/plugin-settings", nil, body, nil); err != nil {
			return err
		}
		fmt.Fprintf(a.out, "The developer mode is %s.\n", args[0])
		return nil
	}

	if err := need(args, 1, "a plugin"); err != nil {
		return err
	}
	p, err := a.findPlugin(ctx, args[0])
	if err != nil {
		return err
	}
	path := fmt.Sprintf("/plugins/%v", p["id"])
	rest := args[1:]
	switch sub {
	case "get":
		return a.printPlugin(p)
	case "enable", "disable":
		var out map[string]any
		if err := a.api.Do(ctx, http.MethodPatch, path, nil, map[string]any{"enabled": sub == "enable"}, &out); err != nil {
			return err
		}
		return a.printPlugin(out)
	case "upgrade":
		fs := flag.NewFlagSet("upgrade", flag.ContinueOnError)
		fs.SetOutput(a.err)
		manifest := fs.String("manifest", "plugin.json", "the manifest file of the new version")
		signature := fs.String("signature", "", "the signature file. Default: the manifest file with .sig")
		totp := fs.String("totp", "", "the code from your authenticator app")
		if err := fs.Parse(rest); err != nil {
			return err
		}
		m, s, err := readManifest(*manifest, *signature)
		if err != nil {
			return err
		}
		body := map[string]any{"manifest": m}
		if s != "" {
			body["signature"] = s
		}
		if err := a.confirmPlugin(body, *totp); err != nil {
			return err
		}
		var out map[string]any
		if err := a.api.Do(ctx, http.MethodPut, path, nil, body, &out); err != nil {
			return err
		}
		return a.printPlugin(out)
	case "credentials":
		fs := flag.NewFlagSet("credentials", flag.ContinueOnError)
		fs.SetOutput(a.err)
		totp := fs.String("totp", "", "the code from your authenticator app")
		if err := fs.Parse(rest); err != nil {
			return err
		}
		body := map[string]any{}
		if err := a.confirmPlugin(body, *totp); err != nil {
			return err
		}
		var out map[string]any
		if err := a.api.Do(ctx, http.MethodPost, path+"/credentials", nil, body, &out); err != nil {
			return err
		}
		a.printCredentials(out)
		return nil
	case "restart":
		if err := a.api.Do(ctx, http.MethodPost, path+"/container", nil, nil, nil); err != nil {
			return err
		}
		fmt.Fprintf(a.out, "The panel starts the container of %s again.\n", cell(p["slug"]))
		return nil
	case "test":
		var out map[string]any
		if err := a.api.Do(ctx, http.MethodPost, path+"/test", nil, nil, &out); err != nil {
			return err
		}
		fmt.Fprintf(a.out, "The test event %s is in the queue. See the result with cf plugins deliveries %s.\n", cell(out["id"]), cell(p["slug"]))
		return nil
	case "deliveries":
		var out struct {
			Data []map[string]any `json:"data"`
		}
		if err := a.api.Do(ctx, http.MethodGet, path+"/deliveries", nil, nil, &out); err != nil {
			return err
		}
		if a.json {
			return a.printJSON(out.Data)
		}
		a.table(out.Data, []string{"createdAt", "event", "state", "attempts", "responseStatus", "error", "id"})
		return nil
	case "remove", "rm":
		if err := a.api.Do(ctx, http.MethodDelete, path, nil, nil, nil); err != nil {
			return err
		}
		fmt.Fprintf(a.out, "The plugin %s is removed. Its token stopped.\n", cell(p["name"]))
		return nil
	}
	return fmt.Errorf("unknown command plugins %s", sub)
}

func (a *app) pluginKeys(ctx context.Context, args []string) error {
	if len(args) == 0 || args[0] == "list" || args[0] == "ls" {
		var out struct {
			Data []map[string]any `json:"data"`
		}
		if err := a.api.Do(ctx, http.MethodGet, "/trusted-keys", nil, nil, &out); err != nil {
			return err
		}
		if a.json {
			return a.printJSON(out.Data)
		}
		a.table(out.Data, []string{"name", "fingerprint", "id"})
		return nil
	}
	switch args[0] {
	case "add":
		fs := flag.NewFlagSet("keys add", flag.ContinueOnError)
		fs.SetOutput(a.err)
		name := fs.String("name", "", "the name of the publisher")
		file := fs.String("file", "", "the public key in PEM format, for example cosign.pub. - reads stdin.")
		totp := fs.String("totp", "", "the code from your authenticator app")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if *name == "" || *file == "" {
			return errors.New("give --name and --file")
		}
		var key []byte
		var err error
		if *file == "-" {
			key, err = io.ReadAll(io.LimitReader(os.Stdin, 8192))
		} else {
			key, err = os.ReadFile(*file)
		}
		if err != nil {
			return err
		}
		body := map[string]any{"name": *name, "publicKey": string(key)}
		if err := a.confirmPlugin(body, *totp); err != nil {
			return err
		}
		var out map[string]any
		if err := a.api.Do(ctx, http.MethodPost, "/trusted-keys", nil, body, &out); err != nil {
			return err
		}
		fmt.Fprintf(a.out, "The panel trusts the key of %s (SHA-256 %s).\n", cell(out["name"]), cell(out["fingerprint"]))
		return nil
	case "remove", "rm":
		if err := need(args, 2, "the ID of the key"); err != nil {
			return err
		}
		if err := a.api.Do(ctx, http.MethodDelete, "/trusted-keys/"+args[1], nil, nil, nil); err != nil {
			return err
		}
		fmt.Fprintln(a.out, "The key is removed. The installed plugins stay.")
		return nil
	}
	return fmt.Errorf("unknown command plugins keys %s", args[0])
}

// nullable gives nil for an empty flag, so that the panel uses its default.
func nullable(v string) any {
	if v == "" {
		return nil
	}
	return v
}
