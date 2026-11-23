package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"

	"github.com/itbm/cloudfactory/cli/internal/client"
)

// moveWebsite moves a website to a different server. The new server receives the files
// through the tunnel, and the old server sends them. The command waits for the job of the new
// server: the move is complete when the website operates there.
func (a *app) moveWebsite(ctx context.Context, w client.Website, args []string) error {
	fs := flag.NewFlagSet("move", flag.ContinueOnError)
	to := fs.String("server", "", "the server that gets the website: a name or an ID")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *to == "" {
		return errors.New("give the new server with --server SERVER")
	}
	id, err := a.serverID(ctx, *to)
	if err != nil {
		return err
	}
	var raw map[string]json.RawMessage
	if err := a.api.Do(ctx, http.MethodPost, "/websites/"+w.ID+"/move", nil, map[string]any{"serverId": id}, &raw); err != nil {
		return err
	}
	if a.json {
		return a.printJSON(raw)
	}
	var jobs []client.Job
	json.Unmarshal(raw["jobs"], &jobs)
	fmt.Fprintf(a.out, "Website %s moves to %s. It does not operate while its files move.\n", w.Name, *to)
	if len(jobs) == 0 {
		return nil
	}
	if a.noWait {
		for _, j := range jobs {
			fmt.Fprintf(a.out, "Job %s is in the queue: %s\n", j.ID, j.Title)
		}
		return nil
	}
	// The first job is the job of the new server. It ends after the files arrived and the
	// website started.
	return a.wait(ctx, jobs[0].ID)
}
