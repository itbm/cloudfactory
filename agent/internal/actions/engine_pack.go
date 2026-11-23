package actions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"strings"

	"github.com/itbm/cloudfactory/agent/internal/packs"
	"github.com/itbm/cloudfactory/agent/internal/panel"
	"github.com/itbm/cloudfactory/agent/internal/quadlet"
)

// The engines of the database packs (cf-database/1, docs/packs.md). The image of a database
// pack, not the agent, knows the commands of its engine: the image has the program
// /cf/manage (manage.go). The agent runs one operation of the program for each task. Thus a
// database pack needs no agent code.
//
// A database server of a pack engine gets the same isolation as a built-in one. The first
// version of the contract has no move to a different server, no Adminer and no access from
// outside the server.

// packDriver is the driver of all pack engines.
type packDriver struct{}

// managedDriver is the capability of an engine whose image does the work with its data: a
// pack engine.
type managedDriver interface {
	engineDriver
	managed()
}

func (packDriver) managed() {}

// errPackEngine is the answer to a request that a pack engine does not have in cf-database/1.
func errPackEngine(engine, what string) error {
	return fmt.Errorf("%s is a database engine of a pack; %s is not available for the engines of packs", engineLabel(engine), what)
}

// isManaged reports whether an engine is a pack engine.
func isManaged(engine string) bool {
	drv, err := driverOf(engine)
	if err != nil {
		return false
	}
	_, ok := drv.(managedDriver)
	return ok
}

// superuserEnv is the variable of the password of the administrator of a pack engine.
const superuserEnv = "CF_SUPERUSER_PASSWORD"

func (packDriver) adjustUnit(*quadlet.Database, DatabaseServer) {}

func (packDriver) prepareSecret(ctx context.Context, d *Deps, s dbServer, u quadlet.Database) error {
	return d.writeSuperuserSecret(ctx, s, u.EnvFile)
}

// ready asks the image. A new data directory takes some time, and the image answers false
// until the engine accepts connections.
func (packDriver) ready(ctx context.Context, d *Deps, s dbServer) error {
	var answer struct {
		Ready bool `json:"ready"`
	}
	if err := d.manage(ctx, s, "ready", nil, &answer); err != nil {
		return err
	}
	if !answer.Ready {
		return errors.New("the engine does not accept connections yet")
	}
	return nil
}

func (packDriver) started(context.Context, *Deps, dbServer) error { return nil }

func (packDriver) secretPath(d *Deps, s dbServer) string {
	return filepath.Join(d.dbDir(s), s.Engine+".env")
}

func (packDriver) checkSecret(secret []byte) error {
	return checkEnvSecret("pack engine", map[string]bool{superuserEnv: true}, secret)
}

func (packDriver) writeSecret(d *Deps, s dbServer, secret []byte) error {
	return d.writeEnvSecret(s, secret)
}

func (packDriver) sizes(ctx context.Context, d *Deps, s dbServer) ([]panel.DatabaseSize, error) {
	if err := d.EnsureDatabaseService(ctx, s); err != nil {
		return nil, err
	}
	return d.manageSizesOf(ctx, s)
}

func (packDriver) dropUser(ctx context.Context, d *Deps, s dbServer, user string) error {
	return d.manageOp(ctx, s, "drop-user", map[string]string{"user": user})
}

func (packDriver) dropDatabase(ctx context.Context, d *Deps, s dbServer, name string) error {
	return d.manageOp(ctx, s, "drop-database", map[string]string{"name": name})
}

// createDatabase makes a new database. The image refuses a database that exists.
func (packDriver) createDatabase(ctx context.Context, d *Deps, s dbServer, name string) error {
	return d.manageOp(ctx, s, "create-database", map[string]string{"name": name})
}

// ensureDatabase makes a database when it is not there.
func (p packDriver) ensureDatabase(ctx context.Context, d *Deps, s dbServer, name string) error {
	sizes, err := p.sizes(ctx, d, s)
	if err != nil {
		return err
	}
	for _, size := range sizes {
		if size.Name == name {
			return nil
		}
	}
	return p.createDatabase(ctx, d, s, name)
}

// packAccess is one database of a user in the operation set-user.
type packAccess struct {
	Name   string `json:"name"`
	Access string `json:"access"`
}

// setUser makes a user when it is not there, and sets its databases to exactly the list. An
// empty password keeps the password of the user.
func (packDriver) setUser(ctx context.Context, d *Deps, s dbServer, user, pass string, databases []packAccess) error {
	if databases == nil {
		databases = []packAccess{}
	}
	in := struct {
		User      string       `json:"user"`
		Password  string       `json:"password,omitempty"`
		Databases []packAccess `json:"databases"`
	}{user, pass, databases}
	return d.manageOp(ctx, s, "set-user", in)
}

// manageInput is the first line of stdin of an operation for one database.
func manageInput(name string) string {
	b, _ := json.Marshal(map[string]string{"name": name})
	return string(b) + "\n"
}

// manageArgs gives the command of an operation that streams a dump: "dump" writes it to
// stdout, and "restore" reads it from stdin after the first line.
func (d *Deps) manageArgs(s dbServer, op string) ([]string, error) {
	args, err := d.dbExec(s, "-i")
	if err != nil {
		return nil, err
	}
	return append(args, manageProgram, op), nil
}

// manageOp runs an operation that answers "{}". It starts the database server first, if it
// does not operate.
func (d *Deps) manageOp(ctx context.Context, s dbServer, op string, in any) error {
	if err := d.EnsureDatabaseService(ctx, s); err != nil {
		return err
	}
	var answer struct{}
	err := d.manage(ctx, s, op, in, &answer)
	if err != nil && ctx.Err() == nil && (packDriver{}).ready(ctx, d, s) != nil {
		// The container possibly stopped. The next job starts it again (see Deps.sql).
		d.dbMu.Lock()
		delete(d.dbReady, s)
		d.dbMu.Unlock()
	}
	return err
}

// manageSizesOf gives the sizes of the databases from /cf/manage.
func (d *Deps) manageSizesOf(ctx context.Context, s dbServer) ([]panel.DatabaseSize, error) {
	var answer struct {
		Sizes []struct {
			Name  string `json:"name"`
			Bytes *int64 `json:"bytes"`
		} `json:"sizes"`
	}
	if err := d.manage(ctx, s, "sizes", nil, &answer); err != nil {
		return nil, err
	}
	sizes := make([]panel.DatabaseSize, 0, len(answer.Sizes))
	for _, row := range answer.Sizes {
		if row.Name == "" || row.Bytes == nil || *row.Bytes < 0 {
			return nil, fmt.Errorf("%s sizes: a size is not valid", manageProgram)
		}
		sizes = append(sizes, panel.DatabaseSize{Engine: s.Engine, Name: row.Name, SizeBytes: *row.Bytes})
	}
	return sizes, nil
}

// restorePack loads a dump into a database of a pack engine. The image replaces all data of
// the database. feed runs the restore command with the dump on its stdin, after the first
// line (manageInput).
func (d *Deps) restorePack(ctx context.Context, s dbServer, feed func(ctx context.Context, client []string) error) error {
	if err := d.EnsureDatabaseService(ctx, s); err != nil {
		return err
	}
	client, err := d.manageArgs(s, "restore")
	if err != nil {
		return err
	}
	return feed(ctx, client)
}

// copyPackDatabase copies a database of a pack engine into a new database of the same engine:
// the dump goes from the dump command to the restore command as a stream.
func (d *Deps) copyPackDatabase(ctx context.Context, from dbServer, fromName string, to dbServer, toName string) error {
	drv := packDriver{}
	// A job that runs again after a restart finds the database of its first run.
	if err := drv.ensureDatabase(ctx, d, to, toName); err != nil {
		return err
	}
	if err := d.EnsureDatabaseService(ctx, from); err != nil {
		return err
	}
	dump, err := d.manageArgs(from, "dump")
	if err != nil {
		return err
	}
	return d.restorePack(ctx, to, func(ctx context.Context, client []string) error {
		pr, pw := io.Pipe()
		dumped := make(chan error, 1)
		go func() {
			err := d.Run.Stream(ctx, strings.NewReader(manageInput(fromName)), pw, dump[0], dump[1:]...)
			pw.CloseWithError(err)
			dumped <- err
		}()
		err := d.Run.Stream(ctx, io.MultiReader(strings.NewReader(manageInput(toName)), pr), nil, client[0], client[1:]...)
		pr.CloseWithError(err)
		if derr := <-dumped; derr != nil && err == nil {
			err = derr
		}
		return err
	})
}

// packEngines gives the engines of the database packs of the server.
func packEngines(list []packs.Pack) []quadlet.EngineSpec {
	var out []quadlet.EngineSpec
	for _, p := range list {
		if !p.Manifest.IsDatabase() {
			continue
		}
		m := p.Manifest
		out = append(out, quadlet.EngineSpec{
			Name: m.Slug, Label: m.Name, Port: m.Database.PortNumber(),
			Lines: quadlet.PackEngineLines(m.Database.DataDir), Pack: m.Slug + " " + m.Version,
		})
	}
	return out
}

// packEngineImage gives the image of a version of a pack engine, with its digest. It is empty
// when the server has no such pack or version.
func (d *Deps) packEngineImage(engine, version string) string {
	d.packsMu.Lock()
	defer d.packsMu.Unlock()
	for _, p := range d.packs {
		if p.Manifest.Slug != engine || !p.Manifest.IsDatabase() {
			continue
		}
		if img, ok := p.Manifest.Image(version); ok {
			return img.Ref + "@" + img.Digest
		}
	}
	return ""
}

// packEngineImages gives the images of all versions of the pack engines of the server.
func (d *Deps) packEngineImages() []string {
	d.packsMu.Lock()
	defer d.packsMu.Unlock()
	var out []string
	for _, p := range d.packs {
		if !p.Manifest.IsDatabase() {
			continue
		}
		for _, img := range p.Manifest.Images {
			out = append(out, img.Ref+"@"+img.Digest)
		}
	}
	return out
}

// checkEngineVersion checks the version of a database server. A pack engine needs a version
// of its pack. A built-in engine has none: its image comes with the agent.
func (d *Deps) checkEngineVersion(engine, version string) error {
	if !isManaged(engine) {
		if version != "" {
			return fmt.Errorf("the engine %s has no versions; send no version", engineLabel(engine))
		}
		return nil
	}
	if version == "" {
		return fmt.Errorf("a database server of the engine %s needs a version", engineLabel(engine))
	}
	if d.packEngineImage(engine, version) == "" {
		return fmt.Errorf("the pack %s has no version %q", engine, version)
	}
	return nil
}

// logPackEngine writes a database pack to the log when the agent loads it.
func logPackEngine(p packs.Pack) {
	slog.Info("database engine of a pack", "pack", p.Manifest.Slug, "version", p.Manifest.Version, "port", p.Manifest.Database.Port, "signed", p.Signed())
}
