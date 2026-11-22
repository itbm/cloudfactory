package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"strings"
)

// findPack gets an installed pack by its slug or its ID.
func (a *app) findPack(ctx context.Context, ref string) (map[string]any, error) {
	var out struct {
		Data []map[string]any `json:"data"`
	}
	if err := a.api.Do(ctx, http.MethodGet, "/packs", nil, nil, &out); err != nil {
		return nil, err
	}
	for _, p := range out.Data {
		if p["id"] == ref || p["slug"] == strings.ToLower(ref) {
			return p, nil
		}
	}
	return nil, fmt.Errorf("there is no pack %s; see cf packs list", ref)
}

// packSigner gives the text for the key that signed a pack or a manifest.
func packSigner(v any) string {
	k, ok := v.(map[string]any)
	if !ok {
		return "no signature (developer mode)"
	}
	if k["release"] == true {
		return "signed by the Cloudfactory release key"
	}
	return fmt.Sprintf("signed by %s (SHA-256 %s)", cell(k["name"]), cell(k["fingerprint"]))
}

// packVersions gives the versions of the images of a pack: "1.27, 1.26".
func packVersions(images any) string {
	list, _ := images.([]any)
	versions := make([]string, 0, len(list))
	for _, x := range list {
		i, _ := x.(map[string]any)
		versions = append(versions, cell(i["version"]))
	}
	return strings.Join(versions, ", ")
}

// packRuntime gives the lines of the runtime or the database engine of a pack. A cf-runtime/1
// pack has a type and commands. A cf-php/1 pack adds versions of PHP to a built-in PHP runtime.
// A database pack (cf-database/1) adds an engine with its port and its data directory.
func packRuntime(p map[string]any) string {
	if db, ok := p["database"].(map[string]any); ok {
		line := fmt.Sprintf("Database:      %s, port %s, data in %s\n", cell(db["contract"]), cell(db["port"]), cell(db["dataDir"]))
		if db["firstPort"] != nil {
			line += fmt.Sprintf("Host ports:    %s to %s, %s database servers\n", cell(db["firstPort"]), cell(db["lastPort"]), cell(db["databaseServers"]))
		}
		return line
	}
	rt, _ := p["runtime"].(map[string]any)
	if rt["contract"] == "cf-php/1" {
		return fmt.Sprintf("Runtime:       %s, adds PHP versions to %s\n", cell(rt["contract"]), cell(rt["family"]))
	}
	return fmt.Sprintf("Runtime:       %s, type %s\nStart command: %s\nInstall:       %s\n",
		cell(rt["contract"]), cell(rt["type"]), cell(rt["defaultCommand"]), cell(rt["defaultInstall"]))
}

func (a *app) printPack(p map[string]any) error {
	if a.json {
		return a.printJSON(p)
	}
	fmt.Fprintf(a.out, "Name:          %s %s (%s)\nID:            %s\nPublisher:     %s, %s\n%s",
		cell(p["name"]), cell(p["version"]), cell(p["slug"]), cell(p["id"]), cell(p["publisher"]), packSigner(p["signedBy"]), packRuntime(p))
	images, _ := p["images"].([]any)
	_, database := p["database"].(map[string]any)
	for _, x := range images {
		i, _ := x.(map[string]any)
		use := "websites " + cell(i["websites"])
		if database {
			use = "database servers " + cell(i["databaseServers"])
		}
		line := fmt.Sprintf("Image:         %s  %s:%s@%s  %s", cell(i["version"]), cell(i["reference"]), cell(i["tag"]), cell(i["digest"]), use)
		if i["supportEndsAt"] != nil {
			line += "  support ends " + cell(i["supportEndsAt"])
		}
		if i["availableDigest"] != nil {
			line += "  new digest waits for a rollout"
		}
		fmt.Fprintln(a.out, line)
	}
	servers, _ := p["servers"].([]any)
	for _, x := range servers {
		s, _ := x.(map[string]any)
		fmt.Fprintf(a.out, "Server:        %s (%s)\n", cell(s["name"]), cell(s["state"]))
	}
	return nil
}

// printPackPreview shows what a manifest adds, before the administrator installs it.
func (a *app) printPackPreview(out map[string]any) error {
	if a.json {
		return a.printJSON(out)
	}
	m, _ := out["manifest"].(map[string]any)
	fmt.Fprintf(a.out, "Pack:          %s %s (%s)\nPublisher:     %s, %s\n%s",
		cell(m["name"]), cell(m["version"]), cell(m["slug"]), cell(m["publisher"]), packSigner(out["signedBy"]), packRuntime(m))
	images, _ := m["images"].([]any)
	for _, x := range images {
		i, _ := x.(map[string]any)
		fmt.Fprintf(a.out, "Image:         %s  %s@%s\n", cell(i["version"]), cell(i["ref"]), cell(i["digest"]))
	}
	if in, ok := out["installed"].(map[string]any); ok {
		fmt.Fprintf(a.out, "Installed:     version %s. Use cf packs upgrade %s.\n", cell(in["version"]), cell(m["slug"]))
	}
	if out["signedBy"] == nil && out["developerMode"] != true {
		fmt.Fprintln(a.out, "The manifest has no signature. The panel installs it only in the developer mode for packs.")
	}
	return nil
}

// manifestFlags reads --manifest and --signature, and --totp when withTOTP is true.
func (a *app) manifestFlags(name string, args []string, withTOTP bool) (manifest, signature, totp string, err error) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(a.err)
	m := fs.String("manifest", "pack.json", "the manifest file")
	s := fs.String("signature", "", "the signature file. Default: the manifest file with .sig")
	t := new(string)
	if withTOTP {
		t = fs.String("totp", "", "the code from your authenticator app")
	}
	if err := fs.Parse(args); err != nil {
		return "", "", "", err
	}
	// readManifest keeps the exact bytes of the manifest: the signature is for these bytes.
	manifest, signature, err = readManifest(*m, *s)
	return manifest, signature, *t, err
}

// packs manages the packs: signed manifests that add a website runtime. Only an administrator
// can use these commands.
func (a *app) packs(ctx context.Context, sub string, args []string) error {
	switch sub {
	case "list", "ls":
		var out struct {
			Data []map[string]any `json:"data"`
		}
		if err := a.api.Do(ctx, http.MethodGet, "/packs", nil, nil, &out); err != nil {
			return err
		}
		if a.json {
			return a.printJSON(out.Data)
		}
		for _, p := range out.Data {
			p["versions"] = packVersions(p["images"])
		}
		a.table(out.Data, []string{"slug", "kind", "name", "version", "publisher", "signed", "versions", "id"})
		return nil
	case "preview", "check":
		m, s, _, err := a.manifestFlags(sub, args, false)
		if err != nil {
			return err
		}
		body := map[string]any{"manifest": m}
		if s != "" {
			body["signature"] = s
		}
		var out map[string]any
		if err := a.api.Do(ctx, http.MethodPost, "/packs/preview", nil, body, &out); err != nil {
			return err
		}
		return a.printPackPreview(out)
	case "install":
		m, s, totp, err := a.manifestFlags("install", args, true)
		if err != nil {
			return err
		}
		body := map[string]any{"manifest": m}
		if s != "" {
			body["signature"] = s
		}
		if err := a.confirmPlugin(body, totp); err != nil {
			return err
		}
		var out map[string]any
		if err := a.api.Do(ctx, http.MethodPost, "/packs", nil, body, &out); err != nil {
			return err
		}
		if !a.json {
			fmt.Fprintf(a.out, "The pack %s %s is installed. The panel sends it to each web server.\n", cell(out["name"]), cell(out["version"]))
		}
		return a.printPack(out)
	case "sync":
		var out map[string]any
		if err := a.api.Do(ctx, http.MethodPost, "/packs/sync", nil, nil, &out); err != nil {
			return err
		}
		fmt.Fprintf(a.out, "The panel sends the packs to %s web servers again.\n", cell(out["servers"]))
		return nil
	case "keys", "key":
		// The panel has one list of trusted keys for the plugins and the packs.
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
		if err := a.api.Do(ctx, http.MethodPut, "/pack-settings", nil, body, nil); err != nil {
			return err
		}
		fmt.Fprintf(a.out, "The developer mode for packs is %s.\n", args[0])
		return nil
	}

	if err := need(args, 1, "a pack"); err != nil {
		return err
	}
	p, err := a.findPack(ctx, args[0])
	if err != nil {
		return err
	}
	path := fmt.Sprintf("/packs/%v", p["id"])
	switch sub {
	case "get":
		return a.printPack(p)
	case "upgrade", "update":
		m, s, totp, err := a.manifestFlags(sub, args[1:], true)
		if err != nil {
			return err
		}
		body := map[string]any{"manifest": m}
		if s != "" {
			body["signature"] = s
		}
		if err := a.confirmPlugin(body, totp); err != nil {
			return err
		}
		var out map[string]any
		if err := a.api.Do(ctx, http.MethodPut, path, nil, body, &out); err != nil {
			return err
		}
		return a.printPack(out)
	case "remove", "rm":
		if err := a.api.Do(ctx, http.MethodDelete, path, nil, nil, nil); err != nil {
			return err
		}
		fmt.Fprintf(a.out, "The pack %s is removed. The panel removes it from each web server.\n", cell(p["name"]))
		return nil
	}
	return fmt.Errorf("unknown command packs %s", sub)
}
