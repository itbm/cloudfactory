package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/itbm/cloudfactory/cli/internal/client"
)

// importChunk is the size of one part of an upload. The panel accepts no larger part.
const importChunk = 4 << 20

// importStatus gives the text of the status of an import, as the web console shows it.
var importStatus = map[string]string{
	"uploading": "Uploading",
	"fetching":  "Reading the archive",
	"ready":     "Ready to import",
	"starting":  "Starting",
	"running":   "Importing",
	"done":      "Complete",
	"failed":    "Failed",
}

// importDetail is the answer of GET /imports/{id}. The fields that the commands use have a
// type. Raw has the full answer for --json.
type importDetail struct {
	ID            string `json:"id"`
	Status        string `json:"status"`
	Source        string `json:"source"`
	FileName      string `json:"fileName"`
	SizeBytes     int64  `json:"sizeBytes"`
	ReceivedBytes int64  `json:"receivedBytes"`
	PanelName     string `json:"panelName"`
	Account       string `json:"account"`
	CustomerName  string `json:"customerName"`
	ServerName    string `json:"serverName"`
	Error         string `json:"error"`
	Progress      struct {
		Total  int `json:"total"`
		Done   int `json:"done"`
		Failed int `json:"failed"`
	} `json:"progress"`
	Warnings []struct {
		Item    string `json:"item"`
		Message string `json:"message"`
	} `json:"warnings"`
	Plan  *importPlan  `json:"plan"`
	Items []importItem `json:"items"`
}

type importPlan struct {
	Websites []struct {
		Name      string  `json:"name"`
		PHP       *string `json:"phpVersion"`
		ImageID   *string `json:"imageId"`
		ImageName *string `json:"imageName"`
		DiskBytes int64   `json:"diskBytes"`
		Databases []any   `json:"databases"`
		WordPress bool    `json:"wordpress"`
		Conflict  *string `json:"conflict"`
	} `json:"websites"`
	MailDomains []struct {
		Name         string  `json:"name"`
		Mailboxes    int     `json:"mailboxes"`
		QuotaBytes   int64   `json:"quotaBytes"`
		Aliases      int     `json:"aliases"`
		Messages     int     `json:"messages"`
		NewPasswords int     `json:"newPasswords"`
		Conflict     *string `json:"conflict"`
	} `json:"mailDomains"`
	MailSpace *struct {
		Mailboxes int   `json:"mailboxes"`
		Bytes     int64 `json:"bytes"`
	} `json:"mailSpace"`
	DNSZones []struct {
		Name     string  `json:"name"`
		Exists   bool    `json:"exists"`
		Conflict *string `json:"conflict"`
	} `json:"dnsZones"`
	Images []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"images"`
}

type importItem struct {
	Kind   string  `json:"kind"`
	Name   string  `json:"name"`
	State  string  `json:"state"`
	Error  *string `json:"error"`
	Detail *string `json:"detail"`
}

// statusText gives the status of an import. A complete import with a failed part is
// "Complete with problems".
func (d importDetail) statusText() string {
	if d.Status == "done" && d.Progress.Failed > 0 {
		return "Complete with problems"
	}
	if t, ok := importStatus[d.Status]; ok {
		return t
	}
	return d.Status
}

// imports reads the backup of an account from cPanel, DirectAdmin, Plesk or HestiaCP and makes
// its websites, databases, mailboxes and DNS zones for a customer (FR-1501, FR-1502). Only an
// administrator can import an account.
func (a *app) imports(ctx context.Context, sub string, args []string) error {
	switch sub {
	case "list", "ls":
		rows, err := client.All[map[string]any](ctx, a.api, "/imports", nil)
		if err != nil {
			return err
		}
		if a.json {
			return a.printJSON(rows)
		}
		for _, r := range rows {
			r["status"] = importStatus[fmt.Sprint(r["status"])]
			if p, ok := r["progress"].(map[string]any); ok && r["status"] == "Complete" && p["failed"] != float64(0) {
				r["status"] = "Complete with problems"
			}
			if r["fileName"] == nil {
				r["fileName"] = "-"
			}
		}
		a.table(rows, []string{"fileName", "status", "panelName", "account", "customerName", "serverName", "id"})
		return nil
	case "get":
		if err := need(args, 1, "an import ID"); err != nil {
			return err
		}
		return a.printImport(ctx, args[0])
	case "create":
		return a.importCreate(ctx, args)
	case "upload":
		if err := need(args, 1, "an import ID"); err != nil {
			return err
		}
		fs := flag.NewFlagSet("upload", flag.ContinueOnError)
		fs.SetOutput(a.err)
		file := fs.String("file", "", "the backup file")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if *file == "" {
			return errors.New("give the backup file with --file FILE")
		}
		d, _, err := a.readImport(ctx, args[0])
		if err != nil {
			return err
		}
		if d.Status != "uploading" {
			return fmt.Errorf("the import %s does not wait for a file. Its status is: %s", d.ID, d.statusText())
		}
		if err := a.uploadArchive(ctx, d.ID, *file, d.SizeBytes, d.ReceivedBytes); err != nil {
			return err
		}
		return a.waitImport(ctx, d.ID, "ready")
	case "start":
		return a.importStart(ctx, args)
	case "delete", "remove", "rm":
		if err := need(args, 1, "an import ID"); err != nil {
			return err
		}
		if err := a.api.Do(ctx, http.MethodDelete, "/imports/"+url.PathEscape(args[0]), nil, nil, nil); err != nil {
			return err
		}
		fmt.Fprintln(a.out, "The import is removed. The servers delete their copies of the archive.")
		return nil
	}
	return fmt.Errorf("unknown command imports %s", sub)
}

// readImport gets one import. raw is the full answer.
func (a *app) readImport(ctx context.Context, id string) (importDetail, map[string]any, error) {
	var body json.RawMessage
	if err := a.api.Do(ctx, http.MethodGet, "/imports/"+url.PathEscape(id), nil, nil, &body); err != nil {
		return importDetail{}, nil, err
	}
	var d importDetail
	var raw map[string]any
	if err := json.Unmarshal(body, &d); err != nil {
		return importDetail{}, nil, err
	}
	json.Unmarshal(body, &raw)
	return d, raw, nil
}

// findCustomer gives the ID of a customer from its ID or its name.
func (a *app) findCustomer(ctx context.Context, ref string) (string, error) {
	rows, err := client.All[map[string]any](ctx, a.api, "/customers", nil)
	if err != nil {
		return "", err
	}
	var found []string
	for _, c := range rows {
		if c["id"] == ref {
			return ref, nil
		}
		if strings.EqualFold(fmt.Sprint(c["name"]), ref) {
			found = append(found, fmt.Sprint(c["id"]))
		}
	}
	switch len(found) {
	case 0:
		return "", fmt.Errorf("there is no customer %s", ref)
	case 1:
		return found[0], nil
	}
	return "", fmt.Errorf("more than one customer has the name %s. Use the ID: cf customers list", ref)
}

func (a *app) importCreate(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("create", flag.ContinueOnError)
	fs.SetOutput(a.err)
	customer := fs.String("customer", "", "the customer that gets the account: a name or an ID")
	server := fs.String("server", "", "the web server for the websites: a name or an ID")
	file := fs.String("file", "", "the backup file. The command sends it to the panel in parts.")
	address := fs.String("url", "", "an HTTPS URL of the backup file. Each server downloads it from there.")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *customer == "" || *server == "" {
		return errors.New("give --customer CUSTOMER and --server SERVER")
	}
	if (*file == "") == (*address == "") {
		return errors.New("give the backup with --file FILE or --url URL, not both")
	}
	customerID, err := a.findCustomer(ctx, *customer)
	if err != nil {
		return err
	}
	serverID, err := a.serverID(ctx, *server)
	if err != nil {
		return err
	}
	body := map[string]any{"customerId": customerID, "serverId": serverID}
	var size int64
	if *file != "" {
		st, err := os.Stat(*file)
		if err != nil {
			return err
		}
		if !st.Mode().IsRegular() || st.Size() == 0 {
			return fmt.Errorf("%s is not a backup file", *file)
		}
		size = st.Size()
		body["fileName"], body["sizeBytes"] = filepath.Base(*file), size
	} else {
		body["url"] = *address
	}
	var out struct {
		Import struct {
			ID string `json:"id"`
		} `json:"import"`
	}
	if err := a.api.Do(ctx, http.MethodPost, "/imports", nil, body, &out); err != nil {
		return err
	}
	id := out.Import.ID
	fmt.Fprintf(a.out, "Import %s is made.\n", id)
	if *file != "" {
		if err := a.uploadArchive(ctx, id, *file, size, 0); err != nil {
			return fmt.Errorf("%w. To continue the upload: cf imports upload %s --file %s", err, id, *file)
		}
	}
	return a.waitImport(ctx, id, "ready")
}

// uploadArchive sends a file to the panel in parts, from the offset that the panel has. When
// the panel has a different offset, for example after a connection error, the command
// continues from the offset of the panel.
func (a *app) uploadArchive(ctx context.Context, id, path string, size, from int64) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return err
	}
	if st.Size() != size {
		return fmt.Errorf("%s has %d bytes. The import waits for a file of %d bytes", path, st.Size(), size)
	}
	buf := make([]byte, importChunk)
	shown := int64(-1)
	for offset := from; offset < size; {
		n, err := f.ReadAt(buf[:min(int64(importChunk), size-offset)], offset)
		if err != nil && !errors.Is(err, io.EOF) {
			return err
		}
		var out struct {
			ReceivedBytes int64 `json:"receivedBytes"`
		}
		err = a.api.SendBytes(ctx, http.MethodPut, "/imports/"+url.PathEscape(id)+"/archive", url.Values{"offset": {strconv.FormatInt(offset, 10)}}, buf[:n], &out)
		var apiErr *client.Error
		if errors.As(err, &apiErr) && apiErr.Status == http.StatusConflict {
			// The panel has a different part of the file.
			if json.Unmarshal(apiErr.Body, &out) != nil || out.ReceivedBytes == offset || out.ReceivedBytes > size {
				return err
			}
			offset = out.ReceivedBytes
			continue
		}
		if err != nil {
			return err
		}
		offset = out.ReceivedBytes
		if step := offset * 10 / size; step != shown {
			shown = step
			fmt.Fprintf(a.out, "Sent %s of %s.\n", byteSize(float64(offset)), byteSize(float64(size)))
		}
	}
	return nil
}

// waitImport waits until the import has the status until, or has ended. It shows each
// change of the status and of the progress. With --no-wait, it only shows the status.
func (a *app) waitImport(ctx context.Context, id, until string) error {
	last := ""
	for {
		d, _, err := a.readImport(ctx, id)
		if err != nil {
			return err
		}
		line := d.statusText()
		if d.Status == "running" || d.Status == "done" {
			line += fmt.Sprintf(": %d of %d parts ended", d.Progress.Done+d.Progress.Failed, d.Progress.Total)
			if d.Progress.Failed > 0 {
				line += fmt.Sprintf(", %d failed", d.Progress.Failed)
			}
		}
		if line != last {
			fmt.Fprintln(a.out, line+".")
			last = line
		}
		switch {
		case d.Status == "failed":
			return fmt.Errorf("the import failed: %s", d.Error)
		case d.Status == until || d.Status == "done":
			if d.Status == "ready" {
				fmt.Fprintf(a.out, "See the plan with: cf imports get %s\nStart the import with: cf imports start %s --all\n", d.ID, d.ID)
			}
			if d.Status == "done" && d.Progress.Failed > 0 {
				return fmt.Errorf("%d parts of the import failed. See them with: cf imports get %s", d.Progress.Failed, d.ID)
			}
			return nil
		case a.noWait:
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(3 * a.poll):
		}
	}
}

func (a *app) printImport(ctx context.Context, id string) error {
	d, raw, err := a.readImport(ctx, id)
	if err != nil {
		return err
	}
	if a.json {
		return a.printJSON(raw)
	}
	fmt.Fprintf(a.out, "Import:     %s\nStatus:     %s\n", d.ID, d.statusText())
	if d.FileName != "" {
		fmt.Fprintf(a.out, "Archive:    %s\n", d.FileName)
	}
	if d.Status == "uploading" {
		fmt.Fprintf(a.out, "Received:   %s of %s\n", byteSize(float64(d.ReceivedBytes)), byteSize(float64(d.SizeBytes)))
	}
	if d.PanelName != "" {
		fmt.Fprintf(a.out, "Backup:     %s, account %s\n", d.PanelName, d.Account)
	}
	fmt.Fprintf(a.out, "Customer:   %s\nServer:     %s\n", d.CustomerName, d.ServerName)
	if d.Error != "" {
		fmt.Fprintf(a.out, "Error:      %s\n", d.Error)
	}
	if p := d.Plan; p != nil {
		a.printPlan(p)
	}
	if len(d.Warnings) > 0 {
		fmt.Fprintln(a.out, "\nWarnings:")
		for _, w := range d.Warnings {
			fmt.Fprintf(a.out, "  %s\n", w.Message)
		}
	}
	if len(d.Items) > 0 {
		fmt.Fprintln(a.out)
		rows := make([]map[string]any, 0, len(d.Items))
		for _, it := range d.Items {
			note := "-"
			if it.Error != nil {
				note = *it.Error
			} else if it.Detail != nil {
				note = *it.Detail
			}
			rows = append(rows, map[string]any{"kind": it.Kind, "name": it.Name, "state": it.State, "note": note})
		}
		a.table(rows, []string{"kind", "name", "state", "note"})
	}
	return nil
}

func (a *app) printPlan(p *importPlan) {
	reason := func(c *string) string {
		if c == nil {
			return "-"
		}
		return "cannot import: " + *c
	}
	if len(p.Websites) > 0 {
		fmt.Fprintln(a.out, "\nWebsites:")
		rows := make([]map[string]any, 0, len(p.Websites))
		for _, w := range p.Websites {
			image, php := "-", "-"
			if w.ImageName != nil {
				image = *w.ImageName
			}
			if w.PHP != nil {
				php = *w.PHP
			}
			rows = append(rows, map[string]any{"name": w.Name, "oldPhp": php, "image": image, "disk": byteSize(float64(w.DiskBytes)), "databases": float64(len(w.Databases)), "wordpress": w.WordPress, "note": reason(w.Conflict)})
		}
		a.table(rows, []string{"name", "oldPhp", "image", "disk", "databases", "wordpress", "note"})
	}
	if len(p.MailDomains) > 0 {
		fmt.Fprintln(a.out, "\nMail domains:")
		rows := make([]map[string]any, 0, len(p.MailDomains))
		for _, m := range p.MailDomains {
			note := reason(m.Conflict)
			if note == "-" && m.NewPasswords > 0 {
				note = fmt.Sprintf("%d mailboxes get a new password", m.NewPasswords)
			}
			rows = append(rows, map[string]any{"name": m.Name, "mailboxes": float64(m.Mailboxes), "quota": byteSize(float64(m.QuotaBytes)), "aliases": float64(m.Aliases), "messages": float64(m.Messages), "note": note})
		}
		a.table(rows, []string{"name", "mailboxes", "quota", "aliases", "messages", "note"})
		if s := p.MailSpace; s != nil {
			fmt.Fprintf(a.out, "The plan of the customer has space for %d more mailboxes and %s more mail. A mailbox that does not fit fails.\n", s.Mailboxes, byteSize(float64(s.Bytes)))
		}
	}
	if len(p.DNSZones) > 0 {
		fmt.Fprintln(a.out, "\nDNS zones:")
		rows := make([]map[string]any, 0, len(p.DNSZones))
		for _, z := range p.DNSZones {
			note := reason(z.Conflict)
			if note == "-" && z.Exists {
				note = "the customer has this zone: the import adds the records that it does not have"
			}
			rows = append(rows, map[string]any{"name": z.Name, "note": note})
		}
		a.table(rows, []string{"name", "note"})
	}
	if len(p.Images) > 0 {
		fmt.Fprintln(a.out, "\nImages for --website NAME=IMAGE_ID:")
		for _, i := range p.Images {
			fmt.Fprintf(a.out, "  %s  %s\n", i.ID, i.Name)
		}
	}
}

func (a *app) importStart(ctx context.Context, args []string) error {
	if err := need(args, 1, "an import ID"); err != nil {
		return err
	}
	fs := flag.NewFlagSet("start", flag.ContinueOnError)
	fs.SetOutput(a.err)
	all := fs.Bool("all", false, "import each website, mail domain and DNS zone that the plan can import")
	var websites, mail, zones multi
	fs.Var(&websites, "website", "a website to import, as NAME or NAME=IMAGE_ID. You can give it more than one time.")
	fs.Var(&mail, "mail", "a mail domain to import. You can give it more than one time.")
	fs.Var(&zones, "zone", "a DNS zone to import. You can give it more than one time.")
	keep := fs.Bool("keep-wp-config", false, "do not change the database settings in wp-config.php")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	d, _, err := a.readImport(ctx, args[0])
	if err != nil {
		return err
	}
	if d.Plan == nil {
		return fmt.Errorf("the import %s has no plan. Its status is: %s", d.ID, d.statusText())
	}
	images := map[string]string{}
	var names []string
	for _, w := range websites {
		name, image, _ := strings.Cut(w, "=")
		names = append(names, name)
		if image != "" {
			images[name] = image
		}
	}
	if *all {
		names = nil
		for _, w := range d.Plan.Websites {
			if w.Conflict == nil {
				names = append(names, w.Name)
			}
		}
		for _, m := range d.Plan.MailDomains {
			if m.Conflict == nil && !slices.Contains(mail, m.Name) {
				mail = append(mail, m.Name)
			}
		}
		for _, z := range d.Plan.DNSZones {
			if z.Conflict == nil && !slices.Contains(zones, z.Name) {
				zones = append(zones, z.Name)
			}
		}
	}
	if len(names)+len(mail)+len(zones) == 0 && *all {
		return errors.New("the plan has no part that the import can make. See the reasons with: cf imports get " + d.ID)
	}
	if len(names)+len(mail)+len(zones) == 0 {
		return errors.New("select the parts to import: give --all, or --website, --mail and --zone. See the plan with: cf imports get " + d.ID)
	}
	// The panel cannot make a part with a conflict: the part would fail.
	conflicts := map[string]*string{}
	for _, w := range d.Plan.Websites {
		conflicts["website "+w.Name] = w.Conflict
	}
	for _, m := range d.Plan.MailDomains {
		conflicts["mail domain "+m.Name] = m.Conflict
	}
	for _, z := range d.Plan.DNSZones {
		conflicts["DNS zone "+z.Name] = z.Conflict
	}
	for _, part := range []struct {
		kind  string
		names []string
	}{{"website", names}, {"mail domain", mail}, {"DNS zone", zones}} {
		for _, name := range part.names {
			c, ok := conflicts[part.kind+" "+name]
			if !ok {
				return fmt.Errorf("the backup has no %s %s. See the plan with: cf imports get %s", part.kind, name, d.ID)
			}
			if c != nil {
				return fmt.Errorf("the import cannot make the %s %s: %s", part.kind, name, *c)
			}
		}
	}
	selected := make([]map[string]any, 0, len(names))
	for _, name := range names {
		w := map[string]any{"name": name}
		if image, ok := images[name]; ok {
			w["imageId"] = image
		}
		selected = append(selected, w)
	}
	body := map[string]any{"websites": selected, "mailDomains": nonNil(mail), "dnsZones": nonNil(zones), "rewriteWordPress": !*keep}
	if err := a.api.Do(ctx, http.MethodPost, "/imports/"+url.PathEscape(d.ID)+"/start", nil, body, nil); err != nil {
		return err
	}
	fmt.Fprintf(a.out, "The import of %s, %s and %s starts.\n", plural(len(selected), "website", "websites"), plural(len(mail), "mail domain", "mail domains"), plural(len(zones), "DNS zone", "DNS zones"))
	return a.waitImport(ctx, d.ID, "done")
}
