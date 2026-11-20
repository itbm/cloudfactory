package actions

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/itbm/cloudfactory/agent/internal/panel"
)

// The management contract of an engine image (trial, switch Deps.ManageTrial, off by default).
//
// An engine image can have the program /cf/manage. The agent runs
//
//	podman exec -i <container> /cf/manage <operation>
//
// with a JSON object on stdin, and reads one JSON object on stdout. The contract is
// cf-database/1 (docs/plugins.md). With it, the image and not the agent knows the commands of
// its engine, so that an engine from a different publisher needs no agent code.
//
// In this trial, the agent asks the MariaDB image for the sizes of its databases. When the
// image has no /cf/manage or the answer is not valid, the agent uses its own query, as before.

// manageProgram is the path of the management program in an engine image.
const manageProgram = "/cf/manage"

// manageContract is the contract version that this agent version knows.
const manageContract = "cf-database/1"

// manage runs one operation of /cf/manage in the container of a database server.
func (d *Deps) manage(ctx context.Context, s dbServer, op string, in, out any) error {
	input := "{}"
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		input = string(b)
	}
	args, err := d.dbExec(s, "-i")
	if err != nil {
		return err
	}
	args = append(args, manageProgram, op)
	res, err := d.Run.RunInput(ctx, input+"\n", args[0], args[1:]...)
	if err != nil {
		return err
	}
	dec := json.NewDecoder(strings.NewReader(res))
	dec.DisallowUnknownFields()
	if err := dec.Decode(out); err != nil {
		return fmt.Errorf("%s %s: the answer is not valid: %w", manageProgram, op, err)
	}
	return nil
}

// manageSizes gives the sizes of the databases from /cf/manage. ok is false when the trial
// is off or the image did not give a valid answer: then the caller uses its own query.
func (d *Deps) manageSizes(ctx context.Context, s dbServer) (sizes []panel.DatabaseSize, ok bool) {
	if !d.ManageTrial {
		return nil, false
	}
	var answer struct {
		Sizes []struct {
			Name  string `json:"name"`
			Bytes *int64 `json:"bytes"`
		} `json:"sizes"`
	}
	if err := d.manage(ctx, s, "sizes", nil, &answer); err != nil {
		slog.Warn("the management program of the image did not give the sizes; the agent uses its own query", "server", s.String(), "err", err)
		return nil, false
	}
	for _, row := range answer.Sizes {
		if row.Name == "" || row.Bytes == nil || *row.Bytes < 0 {
			slog.Warn("the management program of the image gave a size that is not valid; the agent uses its own query", "server", s.String())
			return nil, false
		}
		sizes = append(sizes, panel.DatabaseSize{Engine: s.Engine, Name: row.Name, SizeBytes: *row.Bytes})
	}
	return sizes, true
}
