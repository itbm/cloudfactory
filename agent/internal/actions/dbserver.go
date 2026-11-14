package actions

import (
	"context"
	"os"
	"path/filepath"

	agentv1 "github.com/itbm/cloudfactory/agent/gen/cloudfactory/agent/v1"
	"github.com/itbm/cloudfactory/agent/internal/jobs"
)

// SetDatabaseServer makes a database server of a customer and starts it, or gives a database
// server new limits. New limits restart the database server: the limits of the memory and of
// the CPU are in the unit of the container, and the memory limit of Valkey is an option of
// its command.
func (d *Deps) SetDatabaseServer(req *agentv1.DatabaseServerRequest) (jobs.Job, error) {
	s := req.GetDatabaseServer()
	if err := checkDatabaseServer(s); err != nil {
		return jobs.Job{}, err
	}
	key, _ := serverKey(s.GetId())
	return jobs.Job{ID: req.GetJobId(), Key: key, Steps: []jobs.Step{
		{Name: "Start database server", Do: func(ctx context.Context) error {
			_, err := d.ensureServer(ctx, s, true)
			return err
		}},
	}}, nil
}

// DeleteDatabaseServer stops a database server and removes its data. The panel sends it only
// for a database server with no databases. A job that runs again finds no record of the
// database server, and does nothing.
func (d *Deps) DeleteDatabaseServer(req *agentv1.DeleteDatabaseServerRequest) (jobs.Job, error) {
	key, err := serverKey(req.GetDatabaseServer())
	if err != nil {
		return jobs.Job{}, err
	}
	// find gives the container of the database server, when the state has its record.
	find := func() (dbServer, bool, error) {
		customer, record, _, err := d.State.DatabaseServer(key)
		return dbServer{ID: key, Engine: record.Engine}, customer != "", err
	}
	return jobs.Job{ID: req.GetJobId(), Key: key, Steps: []jobs.Step{
		{Name: "Stop database server", Do: func(ctx context.Context) error {
			s, ok, err := find()
			if err != nil || !ok {
				return err
			}
			lock := d.instanceLock(key)
			lock.Lock()
			defer lock.Unlock()
			return d.stopServer(ctx, s)
		}},
		{Name: "Remove data", Do: func(ctx context.Context) error {
			s, ok, err := find()
			if err != nil || !ok {
				return err
			}
			lock := d.instanceLock(key)
			lock.Lock()
			defer lock.Unlock()
			if err := d.removeServerData(ctx, s); err != nil {
				return err
			}
			d.dbMu.Lock()
			err = d.State.DeleteDatabaseServer(key)
			d.dbMu.Unlock()
			if err != nil {
				return err
			}
			// The firewall forgets the port of the database server.
			return d.syncDatabaseAccess(ctx)
		}},
	}}, nil
}

// serverExists reports whether the Unix user of a database server has the unit of its
// container. A statement that only removes data does not start a database server that this
// server does not have.
func (d *Deps) serverExists(s dbServer) bool {
	dir, err := d.Host.UnitDir(dbUser(s))
	if err != nil {
		return false
	}
	_, err = os.Stat(filepath.Join(dir, s.container()+".container"))
	return err == nil
}

// existingServer gives the database server of an ID from a request, when this server has its
// record and its container.
func (d *Deps) existingServer(id, engine string) (dbServer, bool) {
	key, err := serverKey(id)
	if err != nil {
		return dbServer{}, false
	}
	s := dbServer{ID: key, Engine: engine}
	if customer, record, _, err := d.State.DatabaseServer(key); err != nil || customer == "" || record.Engine != engine {
		return s, false
	}
	return s, d.serverExists(s)
}
