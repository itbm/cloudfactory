package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
)

// The environment variables of a website, and the install command of a Node.js or Python
// website.

// env runs "cf env list|set|unset". The panel never gives the values back, so list shows the
// names only. A value is not an argument: it comes from CF_VALUE or stdin, because the shell
// history and the process list show arguments.
func (a *app) env(ctx context.Context, sub string, args []string) error {
	if err := need(args, 1, "a website"); err != nil {
		return err
	}
	w, err := a.api.FindWebsite(ctx, args[0])
	if err != nil {
		return err
	}
	path := "/websites/" + w.ID + "/environment"
	switch sub {
	case "list":
		if a.json {
			return a.printJSON(map[string]any{"environment": w.Environment})
		}
		if len(w.Environment) == 0 {
			fmt.Fprintln(a.out, "The website has no environment variables.")
		}
		for _, name := range w.Environment {
			fmt.Fprintln(a.out, name)
		}
		return nil
	case "set":
		if err := need(args, 2, "the name of the variable"); err != nil {
			return err
		}
		value, err := a.secret("CF_VALUE", "Value of "+args[1]+": ")
		if err != nil {
			return err
		}
		return a.change(ctx, http.MethodPatch, path, map[string]any{"variables": map[string]any{args[1]: value}})
	case "unset":
		if err := need(args, 2, "the name of the variable"); err != nil {
			return err
		}
		vars := map[string]any{}
		for _, name := range args[1:] {
			vars[name] = nil
		}
		return a.change(ctx, http.MethodPatch, path, map[string]any{"variables": vars})
	}
	return fmt.Errorf("unknown command env %s", sub)
}

// runtimeInstall runs "cf runtime install WEBSITE": the install command in a build container,
// then a new container with no stop.
func (a *app) runtimeInstall(ctx context.Context, id string) error {
	return a.change(ctx, http.MethodPost, "/websites/"+id+"/runtime/install", nil)
}

// given tells if the flag is on the command line, also with an empty value.
func given(fs *flag.FlagSet, name string) bool {
	found := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == name {
			found = true
		}
	})
	return found
}
