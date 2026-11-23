package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"time"
)

// exports manages the account exports (docs/account-export.md). An export is an archive of an
// account: the description of the account, the files of the websites, the dumps of the
// databases and the DNS zones.
func (a *app) exports(ctx context.Context, sub string, args []string) error {
	switch sub {
	case "list", "ls":
		fs := flag.NewFlagSet("list", flag.ContinueOnError)
		customer := fs.String("customer", "", "show the exports of this customer ID")
		if err := fs.Parse(args); err != nil {
			return err
		}
		q := url.Values{}
		if *customer != "" {
			q.Set("customerId", *customer)
		}
		var out struct {
			Data []map[string]any `json:"data"`
		}
		if err := a.api.Do(ctx, http.MethodGet, "/exports", q, nil, &out); err != nil {
			return err
		}
		if a.json {
			return a.printJSON(out.Data)
		}
		for _, e := range out.Data {
			e["size"] = exportSize(e)
		}
		a.table(out.Data, []string{"id", "customerName", "state", "size", "createdAt", "expiresAt"})
		return nil
	case "create":
		fs := flag.NewFlagSet("create", flag.ContinueOnError)
		customer := fs.String("customer", "", "the customer ID. With none, it is your own account.")
		noFiles := fs.Bool("no-files", false, "do not include the files of the websites")
		noDatabases := fs.Bool("no-databases", false, "do not include the dumps of the databases")
		if err := fs.Parse(args); err != nil {
			return err
		}
		body := map[string]any{"files": !*noFiles, "databases": !*noDatabases}
		if *customer != "" {
			body["customerId"] = *customer
		}
		var e map[string]any
		if err := a.api.Do(ctx, http.MethodPost, "/exports", nil, body, &e); err != nil {
			return err
		}
		if a.noWait {
			if a.json {
				return a.printJSON(e)
			}
			fmt.Fprintf(a.out, "The export %s is in progress. Run: cf exports get %s\n", cell(e["id"]), cell(e["id"]))
			return nil
		}
		return a.waitExport(ctx, fmt.Sprint(e["id"]))
	}
	if err := need(args, 1, "an export ID"); err != nil {
		return err
	}
	id, rest := args[0], args[1:]
	path := "/exports/" + url.PathEscape(id)
	switch sub {
	case "get":
		var e map[string]any
		if err := a.api.Do(ctx, http.MethodGet, path, nil, nil, &e); err != nil {
			return err
		}
		return a.printExport(e)
	case "download":
		fs := flag.NewFlagSet("download", flag.ContinueOnError)
		output := fs.String("output", "", "the file. With none, the name that the panel gives, in this directory.")
		if err := fs.Parse(rest); err != nil {
			return err
		}
		return a.downloadExport(ctx, path, *output)
	case "delete", "rm":
		if err := a.api.Do(ctx, http.MethodDelete, path, nil, nil, nil); err != nil {
			return err
		}
		fmt.Fprintf(a.out, "The export %s is removed.\n", id)
		return nil
	}
	return fmt.Errorf("unknown command exports %s", sub)
}

// waitExport shows the progress of an export until it is ready or failed.
func (a *app) waitExport(ctx context.Context, id string) error {
	last := ""
	for {
		var e map[string]any
		if err := a.api.Do(ctx, http.MethodGet, "/exports/"+url.PathEscape(id), nil, nil, &e); err != nil {
			return err
		}
		parts, _ := e["parts"].(map[string]any)
		switch e["state"] {
		case "ready":
			if a.json {
				return a.printJSON(e)
			}
			fmt.Fprintf(a.out, "The export %s is ready (%s). Run: cf exports download %s\n", id, exportSize(e), id)
			return nil
		case "failed":
			return fmt.Errorf("the export failed: %s", cell(e["error"]))
		}
		if line := fmt.Sprintf("%s of %s parts complete, %s received", cell(parts["complete"]), cell(parts["total"]), size(number(e["receivedBytes"]))); line != last && !a.json {
			fmt.Fprintln(a.out, line)
			last = line
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(max(a.poll, time.Millisecond)):
		}
	}
}

func (a *app) printExport(e map[string]any) error {
	if a.json {
		return a.printJSON(e)
	}
	parts, _ := e["parts"].(map[string]any)
	fmt.Fprintf(a.out, "ID:        %s\nAccount:   %s (%s)\nState:     %s\nParts:     %s of %s complete\nSize:      %s\nStarted:   %s by %s\nExpires:   %s\n",
		cell(e["id"]), cell(e["customerName"]), cell(e["customerId"]), cell(e["state"]), cell(parts["complete"]), cell(parts["total"]), exportSize(e), cell(e["createdAt"]), cell(e["createdBy"]), cell(e["expiresAt"]))
	if e["error"] != nil {
		fmt.Fprintf(a.out, "Error:     %s\n", cell(e["error"]))
	}
	warnings, _ := e["warnings"].([]any)
	for _, w := range warnings {
		if m, ok := w.(map[string]any); ok {
			fmt.Fprintf(a.out, "Not in the archive: %s: %s\n", cell(m["item"]), cell(m["message"]))
		}
	}
	return nil
}

// downloadExport downloads the archive to a file. The panel gives a short link with a
// signature, and the download uses no token. The download writes to FILE.partial first. When
// that file exists, the download continues at its end.
func (a *app) downloadExport(ctx context.Context, path, output string) error {
	var link struct {
		URL       string `json:"url"`
		FileName  string `json:"fileName"`
		SizeBytes int64  `json:"sizeBytes"`
	}
	if err := a.api.Do(ctx, http.MethodPost, path+"/download-link", nil, nil, &link); err != nil {
		return err
	}
	if output == "" {
		output = link.FileName
	}
	partial := output + ".partial"
	f, err := os.OpenFile(partial, os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	start, err := f.Seek(0, io.SeekEnd)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.api.BaseURL+link.URL, nil)
	if err != nil {
		return err
	}
	if start > 0 && start < link.SizeBytes {
		req.Header.Set("Range", "bytes="+strconv.FormatInt(start, 10)+"-")
	}
	// The client of the API stops a request after 60 seconds. A large archive needs more time.
	hc := *a.api.HTTP
	hc.Timeout = 0
	resp, err := hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusPartialContent:
	case http.StatusOK:
		// The panel sends the full archive: the download starts again.
		if err := f.Truncate(0); err != nil {
			return err
		}
		if _, err := f.Seek(0, io.SeekStart); err != nil {
			return err
		}
		start = 0
	default:
		return fmt.Errorf("the panel answered the download with status %d", resp.StatusCode)
	}
	if start > 0 {
		fmt.Fprintf(a.out, "Continuing the download at %s.\n", size(float64(start)))
	}
	n, err := io.Copy(f, resp.Body)
	if err != nil {
		return fmt.Errorf("the download stopped after %s: %w. Run the command again to continue", size(float64(start+n)), err)
	}
	if err := f.Close(); err != nil {
		return err
	}
	if start+n != link.SizeBytes {
		return errors.New("the archive does not have the size that the panel gave. Run the command again")
	}
	if err := os.Rename(partial, output); err != nil {
		return err
	}
	fmt.Fprintf(a.out, "Saved %s (%s).\n", output, size(float64(link.SizeBytes)))
	return nil
}

func exportSize(e map[string]any) string {
	if e["sizeBytes"] == nil {
		return "-"
	}
	return size(number(e["sizeBytes"]))
}

func number(v any) float64 {
	f, _ := v.(float64)
	return f
}

// size gives a number of bytes for a person, for example "1.4 GiB".
func size(b float64) string {
	units := []string{"bytes", "KiB", "MiB", "GiB", "TiB"}
	i := 0
	for b >= 1024 && i < len(units)-1 {
		b /= 1024
		i++
	}
	if i == 0 {
		return fmt.Sprintf("%.0f bytes", b)
	}
	return fmt.Sprintf("%.1f %s", b, units[i])
}
