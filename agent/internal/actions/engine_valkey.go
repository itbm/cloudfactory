package actions

import (
	"context"
	"errors"
	"regexp"

	"github.com/itbm/cloudfactory/agent/internal/host"
	"github.com/itbm/cloudfactory/agent/internal/panel"
	"github.com/itbm/cloudfactory/agent/internal/quadlet"
)

// valkeyDriver is the driver of Valkey. The agent speaks RESP to Valkey (valkey.go).
type valkeyDriver struct{}

func (valkeyDriver) numbered() {}

func (valkeyDriver) adjustUnit(u *quadlet.Database, record DatabaseServer) {
	u.MemoryMB = valkeyDataMB(record.MemoryBytes)
}

func (valkeyDriver) prepareSecret(_ context.Context, d *Deps, s dbServer, u quadlet.Database) error {
	// Valkey has a user file, not an environment file.
	return d.writeValkeyUsers(s, u.DataDir)
}

func (valkeyDriver) ready(ctx context.Context, d *Deps, s dbServer) error { return d.valkeyReady(ctx, s) }

func (valkeyDriver) started(context.Context, *Deps, dbServer) error { return nil }

func (valkeyDriver) secretPath(d *Deps, s dbServer) string { return d.valkeySecretFile(s) }

var valkeySecretRE = regexp.MustCompile(`^[0-9a-f]{32,128}\n?$`)

func (valkeyDriver) checkSecret(secret []byte) error {
	if !valkeySecretRE.MatchString(string(secret)) {
		return errors.New("the secret of the Valkey administrator is not valid")
	}
	return nil
}

func (v valkeyDriver) writeSecret(d *Deps, s dbServer, secret []byte) error {
	_, err := host.WriteFile(v.secretPath(d, s), string(secret), 0o600)
	return err
}

func (valkeyDriver) sizes(ctx context.Context, d *Deps, s dbServer) ([]panel.DatabaseSize, error) {
	return d.valkeySizes(ctx, s)
}

// dropUser does nothing: dropDatabase removes the user of a Valkey database.
func (valkeyDriver) dropUser(context.Context, *Deps, dbServer, string) error { return nil }

func (valkeyDriver) dropDatabase(ctx context.Context, d *Deps, s dbServer, name string) error {
	return d.valkeyDelete(ctx, s, name)
}
