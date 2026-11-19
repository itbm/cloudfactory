package actions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"syscall"

	"github.com/itbm/cloudfactory/agent/internal/gate"
	"github.com/itbm/cloudfactory/agent/internal/host"
	"github.com/itbm/cloudfactory/agent/internal/images"
	"github.com/itbm/cloudfactory/agent/internal/panel"
	"github.com/itbm/cloudfactory/agent/internal/pki"
	"github.com/itbm/cloudfactory/agent/internal/quadlet"
	"github.com/itbm/cloudfactory/agent/internal/selfupdate"
	"github.com/itbm/cloudfactory/agent/internal/validate"
)

// Second audit, the privilege boundary.
//
// The agent service has two processes. The worker is the user cf-agent: it has the
// connection to the panel, the jobs and the state, and it cannot change the host. The root
// helper is root: it does only the operations of this file (RootOps) and the commands that
// the policy accepts (Allow, in policy.go).
//
// A Deps is on one of the two sides:
//
//   - In the worker, Gate is the connection to the root helper. A function that needs root
//     starts with "if d.Gate != nil" and asks the helper for its operation.
//   - In the root helper, Gate is nil. The helper checks the values of the worker, and then
//     runs the same function, which now does the work.
//
// A command that root runs as an administrator, for example "cf-agent panel install", has a
// Deps with no Gate too.
//
// The rule for a new operation: the worker gives names (a Unix user, the ID of a database
// server, the ID of a job), never a path. The helper makes each path from its own settings.

// The names of the operations of the root helper.
const (
	opUserEnsure  = "user.ensure"
	opUserStorage = "user.storage"
	opUserRemove  = "user.remove"
	opUserQuota   = "user.quota"
	opUnitWrite   = "unit.write"
	opUnitRemove  = "unit.remove"
	opBlockDevice = "host.blockDevice"
	opQuotaMount  = "host.quotaMount"

	opImagesInit   = "images.init"
	opImagesEnsure = "images.ensure"
	opImagesPrune  = "images.prune"

	opAgentKeep    = "agent.keep"
	opAgentReplace = "agent.replace"
	opPlatform     = "config.platform"
	opPanelCA      = "config.panelCA"

	opEdgePrepare = "edge.prepare"

	opSftpSet    = "sftp.set"
	opSftpRemove = "sftp.remove"
	opSftpSync   = "sftp.sync"
	opSftpStart  = "sftp.start"
	opSftpRetire = "sftp.retire"

	opDnsStart  = "dns.start"
	opDnsWrite  = "dns.write"
	opDnsLoad   = "dns.load"
	opDnsRemove = "dns.remove"
	opDnsStatus = "dns.status"

	opDBPrepare      = "db.prepare"
	opDBRemoveData   = "db.removeData"
	opDBRemoveOld    = "db.removeOld"
	opDBDisk         = "db.disk"
	opDBQuotaMount   = "db.quotaMount"
	opDBNetwork      = "db.network"
	opDBAccess       = "db.access"
	opDBValkeySecret = "db.valkeySecret"

	opSiteIni    = "site.ini"
	opSiteEnv    = "site.env"
	opSiteExists = "site.exists"
	opSiteDisk   = "site.disk"
	opSiteResize = "site.resize"

	opDumpsReset    = "dumps.reset"
	opDumpsRemove   = "dumps.remove"
	opDumpsWrite    = "dumps.write"
	opValkeyDump    = "valkey.dump"
	opValkeyRestore = "valkey.restore"
	opRestoreStage  = "restore.stage"
	opRestoreClean  = "restore.clean"
	opRestoreCopy   = "restore.copy"
	opRestoreHash   = "restore.hash"
	opRestoreLoad   = "restore.load"
	opSecretMake    = "secret.make"
	opSecretRemove  = "secret.remove"

	opPanelDomain = "panel.domain"

	opRebootMark  = "reboot.mark"
	opRebootTake  = "reboot.take"
	opRebootClear = "reboot.clear"

	// The mesh operations are in mesh.go: opMeshApply, opMeshStatus and opMeshRotate.

	opStateRead  = "state.read"
	opStateWrite = "state.write"
)

// The values of the operations.
type (
	userIn struct {
		User string
	}
	ensureIn struct {
		User string
		// DiskBytes is the disk limit of a new website. 0 means no limit.
		DiskBytes int64
	}
	siteIn struct {
		User string
		// Measure makes the helper measure the files of a website that has no disk image.
		Measure bool
	}
	quotaIn struct {
		User  string
		Bytes int64
	}
	unitIn struct {
		User, Name, Content string
	}
	imageIn struct {
		Ref string
	}
	pruneIn struct {
		InUse []string
	}
	replaceIn struct {
		Release        selfupdate.Release
		AllowDowngrade bool
	}
	platformIn struct {
		Platform string
	}
	pinIn struct {
		Fingerprint string
	}
	sftpIn struct {
		User    string
		Enabled bool
		Keys    []string
	}
	serverIn struct {
		Server dbServer
		// Customer, Address and Record are the customer of a database server, the address of
		// that customer, and the port and the limits of the database server (db.prepare).
		Customer, Address string
		Record            DatabaseServer
		// Measure asks for the size of a directory with no disk image (db.disk).
		Measure bool
	}
	diskOut struct {
		// Used and Size are bytes. Size is the disk limit that the server applies: the user
		// quota or the size of the disk image. It is 0 with no limit. Used is -1 when the
		// helper did not measure a directory.
		Used, Size int64
		// Known is true when the kernel gave Used: the helper did not read the directory.
		Known bool
	}
	accessIn struct {
		Elements []accessElement
	}
	iniIn struct {
		User, Suffix, Content string
	}
	dumpIn struct {
		User, Engine, Name string
		// Argv is the dump command. The policy of the helper checks it.
		Argv []string
	}
	restoreIn struct {
		Job, User, Engine, Name string
		// Argv is the client command that reads the dump. The policy of the helper checks it.
		Argv []string
	}
	copyIn struct {
		Job, User string
		// Rel is a path in the directory of the website, or "." for all files.
		Rel string
	}
	secretIn struct {
		// Server is the database container whose user reads the file.
		Server  dbServer
		Content string
	}
	pathIn struct {
		Path string
	}
	jobIn struct {
		Job string
	}
	stateOut struct {
		Data   []byte
		Exists bool
	}
	stateIn struct {
		Data []byte
	}
)

// accessElement lets one website connect to one database server of its customer.
type accessElement struct {
	// UID is the UID of the Unix user of the website.
	UID int
	// Number is the address number of the customer (quadlet.DatabaseAddress).
	Number int
	// Port is the port of the database server.
	Port int
}

// serviceUsers are the Unix users of the services whose containers the worker starts and
// stops. The installer makes them. They are not root.
//
// The worker cannot run a program of its choice as one of these users, or as the user of a
// database server, and it cannot give the text of one of their units: the user of a database
// server owns the data of a customer, and root does file work in the directory of the
// database servers. The root helper makes their units from its own settings.
var serviceUsers = []string{host.EdgeUser, host.DnsUser, host.MailUser, host.WebmailUser}

// websiteUser checks that name is the Unix user of a website: the name has the form of a
// website user, and a user that exists has a UID in the range of the website users. The
// worker can never name root or a user of an administrator.
func (d *Deps) websiteUser(name string) error {
	if err := validate.UnixUser(name); err != nil {
		return err
	}
	uid, err := d.Host.LookupUID(name)
	if err != nil {
		var unknown user.UnknownUserError
		if errors.As(err, &unknown) {
			return nil
		}
		return err
	}
	if uid < host.WebsiteIDMin || uid > host.WebsiteIDMax {
		return fmt.Errorf("the user %s has the UID %d, which is not in the range of the website users", name, uid)
	}
	return nil
}

// databaseUser checks that name is the Unix user of a database server: the name has the form
// of such a user, and a user that exists has a UID in the range of these users. The root
// helper makes the user itself (db.prepare), so the worker cannot make root or the user of a
// website a database user.
func (d *Deps) databaseUser(name string) error {
	if err := host.CheckDatabaseUser(name); err != nil {
		return err
	}
	uid, err := d.Host.LookupUID(name)
	if err != nil {
		var unknown user.UnknownUserError
		if errors.As(err, &unknown) {
			return nil
		}
		return err
	}
	if uid < host.DatabaseIDMin || uid > host.DatabaseIDMax {
		return fmt.Errorf("the user %s has the UID %d, which is not in the range of the users of the database servers", name, uid)
	}
	return nil
}

// managedUser checks that name is the Unix user of a website, of a database server or of a
// service.
func (d *Deps) managedUser(name string) error {
	if slices.Contains(serviceUsers, name) {
		return nil
	}
	if strings.HasPrefix(name, host.DatabaseUserPrefix) {
		return d.databaseUser(name)
	}
	return d.websiteUser(name)
}

// existingWebsiteUser is websiteUser for a user that must exist.
func (d *Deps) existingWebsiteUser(name string) error {
	if err := d.websiteUser(name); err != nil {
		return err
	}
	_, err := d.Host.LookupUID(name)
	return err
}

// checkServer checks a database server from the worker.
func checkServer(s dbServer) error {
	if err := checkEngine(s.Engine); err != nil {
		return err
	}
	key, err := serverKey(s.ID)
	if err != nil {
		return err
	}
	if key != s.ID {
		return errors.New("the ID of the database server is not in lower case")
	}
	return nil
}

// checkPrepare checks the values of a database server from the worker, before the root
// helper makes its directories and its unit.
func checkPrepare(in serverIn) error {
	s, r := in.Server, in.Record
	if err := checkServer(s); err != nil {
		return err
	}
	key, err := instanceKey(in.Customer)
	if err != nil {
		return err
	}
	if key != in.Customer {
		return errors.New("the customer of the database server is not valid")
	}
	if _, err := quadlet.DatabaseNumber(in.Address); err != nil {
		return err
	}
	if r.Engine != s.Engine {
		return fmt.Errorf("the database server has the engine %s, not %s", s.Engine, r.Engine)
	}
	if err := quadlet.CheckDatabasePort(r.Engine, r.Port); err != nil {
		return err
	}
	if r.VCPUs < 0 || r.MemoryBytes < 0 || r.DiskBytes < 0 {
		return errors.New("a limit of the database server is less than 0")
	}
	return nil
}

// containerOf reports whether name is the name of the container of the database server of a
// Unix user. Thus a command as the user of a database server cannot name the container of a
// different database server.
func containerOf(unixUser, name string) bool {
	id, ok := strings.CutPrefix(unixUser, host.DatabaseUserPrefix)
	if !ok || host.CheckDatabaseUser(unixUser) != nil {
		return false
	}
	for _, engine := range Engines {
		if name == quadlet.DatabaseContainer(engine, id) {
			return true
		}
	}
	return false
}

// unitOf reports whether name is the unit of the database server of a Unix user (containerOf).
func unitOf(unixUser, name string) bool {
	container, ok := strings.CutSuffix(name, ".container")
	return ok && containerOf(unixUser, container)
}

var (
	suffixRE = regexp.MustCompile(`^(-b)?$`)
	// unitNameRE is the name of a Quadlet unit of a website or of a service.
	unitNameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,120}\.(container|network|volume)$`)
)

// checkImageRef checks an image reference for the shared image store: an image with a tag
// and a digest. It cannot start with "-", so it is never an option of Podman.
func checkImageRef(ref string) error {
	name, digest, ok := strings.Cut(ref, "@")
	if !ok || !images.Shareable(ref) {
		return fmt.Errorf("the image %q has no digest", ref)
	}
	return errors.Join(validate.Image(name), validate.Digest(digest))
}

// none makes an operation from a function with no result.
func none[In any](fn func(ctx context.Context, in In) error) gate.Handler {
	return gate.Op(func(ctx context.Context, in In) (gate.None, error) { return gate.None{}, fn(ctx, in) })
}

// RootOps gives the operations of the root helper. d is the Deps of the helper: it has no
// Gate. Each operation checks the values of the worker before it does the work.
func (d *Deps) RootOps() map[string]gate.Handler {
	if d.Gate != nil {
		panic("actions: RootOps is for the root helper, not for the worker")
	}
	h := d.Host
	ops := map[string]gate.Handler{
		// The Unix users of the websites, their units and their disk limits.
		opUserEnsure: none(func(ctx context.Context, in ensureIn) error {
			if err := d.websiteUser(in.User); err != nil {
				return err
			}
			if in.DiskBytes < 0 {
				return errors.New("the disk limit is less than 0")
			}
			return d.ensureWebsiteUser(ctx, in.User, in.DiskBytes)
		}),
		opUserStorage: none(func(ctx context.Context, in userIn) error {
			if err := d.existingWebsiteUser(in.User); err != nil {
				return err
			}
			return h.LinkStorageConfig(ctx, in.User)
		}),
		opUserRemove: none(func(ctx context.Context, in userIn) error {
			if err := d.websiteUser(in.User); err != nil {
				return err
			}
			return d.removeWebsiteUser(ctx, in.User)
		}),
		opUserQuota: none(func(ctx context.Context, in quotaIn) error {
			if err := d.existingWebsiteUser(in.User); err != nil {
				return err
			}
			if in.Bytes < 0 {
				return errors.New("the disk limit is less than 0")
			}
			if d.QuotaMount == "" {
				return errors.New("user quotas are off on the file system of the websites")
			}
			return h.SetDiskQuota(ctx, in.User, d.QuotaMount, in.Bytes)
		}),
		// The unit of a website is for a rootless container of the user of the website. It
		// cannot give more rights than that user has. The helper makes the units of the
		// services itself (db.prepare, edge.prepare).
		opUnitWrite: gate.Op(func(_ context.Context, in unitIn) (bool, error) {
			if err := d.existingWebsiteUser(in.User); err != nil {
				return false, err
			}
			if !unitNameRE.MatchString(in.Name) {
				return false, fmt.Errorf("the unit name %q is not valid", in.Name)
			}
			return h.WriteUnit(in.User, in.Name, in.Content)
		}),
		opUnitRemove: none(func(_ context.Context, in unitIn) error {
			if strings.HasPrefix(in.User, host.DatabaseUserPrefix) {
				// The worker stops a database server and removes its unit.
				if err := d.databaseUser(in.User); err != nil {
					return err
				}
				if !unitOf(in.User, in.Name) {
					return fmt.Errorf("%q is not the unit of the database server of %s", in.Name, in.User)
				}
				return h.RemoveUnit(in.User, in.Name)
			}
			if err := d.websiteUser(in.User); err != nil {
				return err
			}
			if !unitNameRE.MatchString(in.Name) {
				return fmt.Errorf("the unit name %q is not valid", in.Name)
			}
			return h.RemoveUnit(in.User, in.Name)
		}),
		opBlockDevice: gate.Op(func(ctx context.Context, _ gate.None) (string, error) {
			return h.BlockDevice(ctx, h.SitesDir)
		}),
		opQuotaMount: gate.Op(func(ctx context.Context, _ gate.None) (string, error) {
			return h.QuotaMount(ctx, h.SitesDir), nil
		}),

		// The shared image store. Podman checks the signature of each image that root pulls.
		opImagesInit: none(func(ctx context.Context, _ gate.None) error {
			if d.Images == nil {
				return errors.New("this server has no shared image store")
			}
			return d.Images.Init(ctx)
		}),
		opImagesEnsure: none(func(ctx context.Context, in imageIn) error {
			if d.Images == nil {
				return errors.New("this server has no shared image store")
			}
			if err := checkImageRef(in.Ref); err != nil {
				return err
			}
			return d.Images.Ensure(ctx, in.Ref)
		}),
		opImagesPrune: none(func(_ context.Context, in pruneIn) error {
			if d.Images == nil {
				return errors.New("this server has no shared image store")
			}
			// The helper adds the images of the units itself, so a worker that does not name
			// the image of a database server cannot remove it.
			return d.Images.Prune(append(in.InUse, d.unitImages()...))
		}),

		// A new agent binary. The helper checks the signature and the version again: it does
		// not trust the check of the worker.
		opAgentKeep: none(func(context.Context, gate.None) error { return d.keepAgent() }),
		opAgentReplace: none(func(_ context.Context, in replaceIn) error {
			// An earlier release has no root helper: its agent is one root process. A worker
			// that an attacker controls must not go back to such a release.
			if selfupdate.ParseManifest(in.Release.Manifest)[selfupdate.ManifestRootHelper] != "1" {
				return errors.New("the release has no root helper, so the root helper does not install it; an administrator can install it by hand")
			}
			// The worker cannot allow an older version: a signed old release can have a
			// fault in its root helper that a newer release repaired. Only an administrator
			// on the server allows it, with the file DowngradeFile, for one update
			// (adversarial review, finding G1-02).
			allow := in.AllowDowngrade && d.takeDowngradePermit()
			if in.AllowDowngrade && !allow && d.Updater.CheckVersion(in.Release.Version, false) != nil {
				return fmt.Errorf("the version %s is older than the version of this agent. To allow it for one update, an administrator makes the file %s on the server (as root) and starts the update again", strings.TrimPrefix(in.Release.Version, "v"), d.DowngradeFile)
			}
			return d.replaceAgent(in.Release, allow)
		}),
		opPlatform: none(func(_ context.Context, in platformIn) error { return d.savePlatform(in.Platform) }),
		opPanelCA:  none(func(_ context.Context, in pinIn) error { return d.SavePanelCAPin(in.Fingerprint) }),

		// The edge proxy. The helper makes the unit from its own settings.
		opEdgePrepare: gate.Op(func(ctx context.Context, _ gate.None) (bool, error) {
			if d.EdgeImage == "" {
				return false, errors.New("the root helper has no image of the edge proxy")
			}
			return d.prepareEdge(ctx, d.EdgeImage, d.DataDir)
		}),

		// SFTP: the keys of a website user, the user list and the container.
		opSftpSet: none(func(ctx context.Context, in sftpIn) error {
			errs := []error{d.existingWebsiteUser(in.User)}
			for _, k := range in.Keys {
				errs = append(errs, validate.SSHKey(k))
			}
			if err := errors.Join(errs...); err != nil {
				return err
			}
			return d.setSftpKeys(ctx, in.User, in.Enabled, in.Keys)
		}),
		opSftpRemove: none(func(ctx context.Context, in userIn) error {
			if err := d.websiteUser(in.User); err != nil {
				return err
			}
			return d.removeSftpKeys(ctx, in.User)
		}),
		opSftpSync:   none(func(context.Context, gate.None) error { return d.SyncSftpUsers() }),
		opSftpStart:  none(func(ctx context.Context, _ gate.None) error { return d.startSftp(ctx) }),
		opSftpRetire: none(func(ctx context.Context, _ gate.None) error { return d.retireHostSftp(ctx) }),

		// The DNS server. The helper checks each zone again, writes the zone files and the
		// configuration itself, and runs the tools of Knot as the DNS user.
		opDnsStart:  none(func(ctx context.Context, _ gate.None) error { return d.startDns(ctx) }),
		opDnsWrite:  none(func(ctx context.Context, in dnsWriteIn) error { return d.writeDnsZones(ctx, in.Zones) }),
		opDnsLoad:   none(func(ctx context.Context, in dnsNamesIn) error { return d.loadDnsZones(ctx, in.Names) }),
		opDnsRemove: none(func(ctx context.Context, in dnsNamesIn) error { return d.removeDnsZones(ctx, in.Names) }),
		opDnsStatus: gate.Op(func(ctx context.Context, _ gate.None) (dnsStatusOut, error) {
			return d.dnsStatus(ctx), nil
		}),

		// The database containers: their directories, disks and secrets, and the firewall.
		opDBPrepare: gate.Op(func(ctx context.Context, in serverIn) (bool, error) {
			if err := checkPrepare(in); err != nil {
				return false, err
			}
			return d.prepareServer(ctx, in.Server, in.Customer, in.Address, in.Record)
		}),
		opDBRemoveData: none(func(ctx context.Context, in serverIn) error {
			if err := checkServer(in.Server); err != nil {
				return err
			}
			return d.removeServerData(ctx, in.Server)
		}),
		opDBRemoveOld: none(func(ctx context.Context, _ gate.None) error { d.removeOldServerData(ctx); return nil }),
		opDBDisk: gate.Op(func(ctx context.Context, in serverIn) (diskOut, error) {
			if err := checkServer(in.Server); err != nil {
				return diskOut{}, err
			}
			return d.diskState(ctx, in.Server, in.Measure), nil
		}),
		opDBQuotaMount: gate.Op(func(context.Context, gate.None) (string, error) { return d.DBQuotaMount, nil }),
		opDBNetwork:    none(func(ctx context.Context, _ gate.None) error { return d.ensureDatabaseNetwork(ctx) }),
		opDBAccess: none(func(ctx context.Context, in accessIn) error {
			return d.loadDatabaseAccess(ctx, in.Elements)
		}),
		opDBSend:    none(func(ctx context.Context, in transferIn) error { return d.sendServer(ctx, in) }),
		opDBReceive: none(func(ctx context.Context, in transferIn) error { return d.receiveServer(ctx, in) }),
		opDBValkeySecret: gate.Op(func(ctx context.Context, in serverIn) (string, error) {
			if err := checkServer(in.Server); err != nil {
				return "", err
			}
			if !isNumbered(in.Server.Engine) {
				return "", errors.New("the database server is not a Valkey database server")
			}
			return d.valkeySecret(ctx, in.Server)
		}),

		// The files of a website that root keeps or reads.
		opSiteIni: none(func(ctx context.Context, in iniIn) error {
			if err := d.websiteUser(in.User); err != nil {
				return err
			}
			if !suffixRE.MatchString(in.Suffix) {
				return fmt.Errorf("the suffix %q is not valid", in.Suffix)
			}
			return d.writeIni(ctx, in.User, in.Suffix, in.Content)
		}),
		opSiteEnv: none(func(ctx context.Context, in iniIn) error {
			if err := d.websiteUser(in.User); err != nil {
				return err
			}
			if !suffixRE.MatchString(in.Suffix) {
				return fmt.Errorf("the suffix %q is not valid", in.Suffix)
			}
			if err := checkEnvFile(in.Content); err != nil {
				return err
			}
			return d.writeEnv(ctx, in.User, in.Suffix, in.Content)
		}),
		opSiteExists: gate.Op(func(ctx context.Context, in userIn) (bool, error) {
			if err := d.websiteUser(in.User); err != nil {
				return false, err
			}
			return d.appExists(ctx, in.User)
		}),
		opSiteDisk: gate.Op(func(ctx context.Context, in siteIn) (diskOut, error) {
			if err := d.websiteUser(in.User); err != nil {
				return diskOut{}, err
			}
			return d.siteDiskState(ctx, in.User, in.Measure), nil
		}),
		// The disk image of a website gets the size of a new disk limit. The helper gives the
		// worker no path: it makes the image file and the mount point from the name of the user.
		opSiteResize: none(func(ctx context.Context, in quotaIn) error {
			if err := d.existingWebsiteUser(in.User); err != nil {
				return err
			}
			if in.Bytes <= 0 {
				return errors.New("the disk limit is not more than 0")
			}
			return d.resizeSiteDisk(ctx, in.User, in.Bytes)
		}),

		// Backups: the database dumps and the directory of a restore. Only root can read them.
		opDumpsReset: none(func(ctx context.Context, in userIn) error {
			if err := d.websiteUser(in.User); err != nil {
				return err
			}
			return d.resetDumps(ctx, in.User)
		}),
		opDumpsRemove: none(func(ctx context.Context, in userIn) error {
			if err := d.websiteUser(in.User); err != nil {
				return err
			}
			return d.removeDumps(ctx, in.User)
		}),
		opDumpsWrite: gate.Op(func(ctx context.Context, in dumpIn) (string, error) {
			if err := errors.Join(d.websiteUser(in.User), sqlEngine(in.Engine), validate.TenantDatabaseName(in.User, in.Name), d.allowArgv(in.Argv)); err != nil {
				return "", err
			}
			return d.writeDump(ctx, in.User, in.Engine, in.Name, in.Argv)
		}),
		opValkeyDump: gate.Op(func(ctx context.Context, in valkeyBackupIn) (string, error) {
			if err := errors.Join(d.websiteUser(in.User), checkServer(in.Server), validate.TenantDatabaseName(in.User, in.Name)); err != nil {
				return "", err
			}
			if !isNumbered(in.Server.Engine) {
				return "", errors.New("the database server is not a Valkey database server")
			}
			return d.valkeyDumpToFile(ctx, in.Server, in.User, in.Name)
		}),
		opValkeyRestore: none(func(ctx context.Context, in valkeyBackupIn) error {
			if err := errors.Join(validate.ID(in.Job), d.websiteUser(in.User), checkServer(in.Server), validate.TenantDatabaseName(in.User, in.Name)); err != nil {
				return err
			}
			if !isNumbered(in.Server.Engine) {
				return errors.New("the database server is not a Valkey database server")
			}
			return d.valkeyRestoreStaged(ctx, in.Job, in.User, in.Server, in.Name)
		}),
		opRestoreStage: none(func(ctx context.Context, in jobIn) error {
			if err := validate.ID(in.Job); err != nil {
				return err
			}
			return d.makeStage(ctx, in.Job)
		}),
		opRestoreClean: none(func(ctx context.Context, in jobIn) error {
			if err := validate.ID(in.Job); err != nil {
				return err
			}
			return d.removeStage(ctx, in.Job)
		}),
		opRestoreCopy: none(func(ctx context.Context, in copyIn) error {
			if err := errors.Join(validate.ID(in.Job), d.existingWebsiteUser(in.User)); err != nil {
				return err
			}
			if in.Rel != "." && (!filepath.IsLocal(in.Rel) || filepath.Clean(in.Rel) != in.Rel) {
				return fmt.Errorf("the path %q is not a path in the website", in.Rel)
			}
			return d.copyStaged(ctx, in.Job, in.User, in.Rel)
		}),
		opRestoreHash: gate.Op(func(ctx context.Context, in restoreIn) (string, error) {
			if err := errors.Join(validate.ID(in.Job), d.websiteUser(in.User), d.backupEngine(in.Engine), validate.TenantDatabaseName(in.User, in.Name)); err != nil {
				return "", err
			}
			return d.stagedDumpHash(ctx, in.Job, in.User, in.Engine, in.Name)
		}),
		opRestoreLoad: none(func(ctx context.Context, in restoreIn) error {
			if err := errors.Join(validate.ID(in.Job), d.websiteUser(in.User), sqlEngine(in.Engine), validate.TenantDatabaseName(in.User, in.Name), d.allowArgv(in.Argv)); err != nil {
				return err
			}
			return d.loadStagedDump(ctx, in.Job, in.User, in.Engine, in.Name, in.Argv)
		}),
		opSecretMake: gate.Op(func(ctx context.Context, in secretIn) (string, error) {
			if err := checkServer(in.Server); err != nil {
				return "", err
			}
			if !secretRE.MatchString(in.Content) {
				return "", errors.New("the content of the secret file is not valid")
			}
			return d.secretFile(ctx, in.Server, in.Content)
		}),
		// removeSecret removes only a file that secretFile made.
		opSecretRemove: none(func(ctx context.Context, in pathIn) error { return d.removeSecret(ctx, in.Path) }),

		// The mesh: the tunnels to other servers. The helper checks each value of the state
		// (mesh.State.Check), and it makes each path and each command itself. The private
		// key stays in the helper.
		opMeshApply:  none(func(ctx context.Context, in meshIn) error { return d.applyMesh(ctx, in.State) }),
		opMeshStatus: gate.Op(func(ctx context.Context, _ gate.None) (*panel.Mesh, error) { return d.meshStatus(ctx) }),
		opMeshRotate: gate.Op(func(ctx context.Context, _ gate.None) (string, error) { return d.rotateMeshKey(ctx) }),
		// The networks: the helper checks each rule and each name (mesh.Rules.Check and
		// mesh.HostsFile), and it makes each path itself.
		opNetworkApply: none(func(ctx context.Context, in networkIn) error { return d.applyNetwork(ctx, in) }),

		opPanelDomain: gate.Op(func(ctx context.Context, _ gate.None) (string, error) { return d.panelDomain(ctx) }),

		// The mark of a reboot job, and the state of the worker. The files stay where an
		// earlier release has them, so that "cf-agent rollback" finds them.
		opRebootMark: none(func(ctx context.Context, in jobIn) error {
			if err := validate.ID(in.Job); err != nil {
				return err
			}
			return d.markReboot(ctx, in.Job)
		}),
		opRebootTake:  gate.Op(func(ctx context.Context, _ gate.None) (string, error) { return d.takeRebootMark(ctx), nil }),
		opRebootClear: none(func(ctx context.Context, _ gate.None) error { d.clearRebootMark(ctx); return nil }),
		opStateRead: gate.Op(func(context.Context, gate.None) (stateOut, error) {
			b, exists, err := d.State.load()
			return stateOut{Data: b, Exists: exists}, err
		}),
		opStateWrite: none(func(_ context.Context, in stateIn) error {
			if !json.Valid(in.Data) {
				return errors.New("the state is not valid JSON")
			}
			return d.State.save(in.Data)
		}),
	}
	// The mail server.
	for name, op := range d.mailRootOps() {
		ops[name] = op
	}
	return ops
}

// secretRE is the content of a secret file: the password of a temporary database user for a
// restore (restoreDump).
var secretRE = regexp.MustCompile(`^(MYSQL_PWD|PGPASSWORD)=[0-9a-f]{8,128}\n$`)

// allowArgv checks a command of the worker against the policy of the helper.
func (d *Deps) allowArgv(argv []string) error {
	if len(argv) == 0 {
		return errors.New("no command")
	}
	return d.Allow(gate.Command{Argv: argv})
}

// hostRoot is host.Root for the worker: each method asks the root helper.
type hostRoot struct{ g gate.Gate }

// EnsureUser gives no disk limit: the worker makes the user of a new website with
// Deps.ensureWebsiteUser, which sends the limit.
func (r hostRoot) EnsureUser(ctx context.Context, unixUser string) error {
	return r.g.Call(ctx, opUserEnsure, ensureIn{User: unixUser}, nil)
}

func (r hostRoot) LinkStorageConfig(ctx context.Context, unixUser string) error {
	return r.g.Call(ctx, opUserStorage, userIn{User: unixUser}, nil)
}

func (r hostRoot) RemoveUser(ctx context.Context, unixUser string) error {
	return r.g.Call(ctx, opUserRemove, userIn{User: unixUser}, nil)
}

func (r hostRoot) WriteUnit(unixUser, name, content string) (changed bool, err error) {
	err = r.g.Call(context.Background(), opUnitWrite, unitIn{User: unixUser, Name: name, Content: content}, &changed)
	return changed, err
}

func (r hostRoot) RemoveUnit(unixUser, name string) error {
	return r.g.Call(context.Background(), opUnitRemove, unitIn{User: unixUser, Name: name}, nil)
}

// SetDiskQuota does not send the mount point: the helper knows the file system of the websites.
func (r hostRoot) SetDiskQuota(ctx context.Context, unixUser, _ string, bytes int64) error {
	return r.g.Call(ctx, opUserQuota, quotaIn{User: unixUser, Bytes: bytes}, nil)
}

func (r hostRoot) BlockDevice(ctx context.Context, _ string) (dev string, err error) {
	err = r.g.Call(ctx, opBlockDevice, nil, &dev)
	return dev, err
}

func (r hostRoot) QuotaMount(ctx context.Context, _ string) (mount string) {
	_ = r.g.Call(ctx, opQuotaMount, nil, &mount)
	return mount
}

// imagesRoot is the shared image store for the worker: each method asks the root helper.
type imagesRoot struct{ g gate.Gate }

func (r imagesRoot) Init(ctx context.Context) error { return r.g.Call(ctx, opImagesInit, nil, nil) }

func (r imagesRoot) Ensure(ctx context.Context, ref string) error {
	return r.g.Call(ctx, opImagesEnsure, imageIn{Ref: ref}, nil)
}

// EnsureService is not for the worker: the root helper puts the image of a database engine
// in the shared store when it prepares a database server (db.prepare).
func (r imagesRoot) EnsureService(context.Context, string) error {
	return errors.New("only the root helper puts the image of a database engine in the shared store")
}

func (r imagesRoot) Prune(inUse []string) error {
	return r.g.Call(context.Background(), opImagesPrune, pruneIn{InUse: inUse}, nil)
}

// stateRoot keeps the state of the worker through the root helper.
type stateRoot struct{ g gate.Gate }

func (r stateRoot) Read() ([]byte, bool, error) {
	var out stateOut
	err := r.g.Call(context.Background(), opStateRead, nil, &out)
	return out.Data, out.Exists, err
}

func (r stateRoot) Write(b []byte) error {
	return r.g.Call(context.Background(), opStateWrite, stateIn{Data: b}, nil)
}

// UseGate makes d the Deps of the worker: all work that needs root goes to the root helper
// through g. Call it before the first action.
func (d *Deps) UseGate(g gate.Gate) {
	d.Gate = g
	d.Run = g
	if d.Host != nil {
		d.Host.Run = g
		d.Host.Root = hostRoot{g}
	}
	if d.Images != nil {
		d.Images = imagesRoot{g}
	}
	if d.State != nil {
		d.State.Store = stateRoot{g}
	}
	d.Apt.Run = g
}

// The functions below are small parts of the actions that need root. Each one has the two
// sides of the boundary.

// DatabaseQuotaMount gives the mount point of the directory of the database servers when user
// quotas are on there, or "". The root helper finds it at its start, and the worker asks the
// helper: the status report tells the panel when the server does not apply the disk limit
// of a database server.
func (d *Deps) DatabaseQuotaMount(ctx context.Context) (mount string) {
	if d.Gate != nil {
		_ = d.Gate.Call(ctx, opDBQuotaMount, nil, &mount)
		return mount
	}
	return d.DBQuotaMount
}

// keepAgent copies the agent binary for a rollback.
func (d *Deps) keepAgent() error {
	if d.Gate != nil {
		return d.Gate.Call(context.Background(), opAgentKeep, nil, nil)
	}
	return d.Updater.KeepPrevious()
}

// takeDowngradePermit reports whether an administrator allowed one update to an older
// version, and removes the permission. The permission is a file that only root can make.
func (d *Deps) takeDowngradePermit() bool {
	if d.DowngradeFile == "" {
		return false
	}
	fi, err := os.Lstat(d.DowngradeFile)
	if err != nil || !fi.Mode().IsRegular() {
		return false
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok && st.Uid != uint32(os.Geteuid()) {
		return false
	}
	return os.Remove(d.DowngradeFile) == nil
}

// replaceAgent puts a new agent binary in place. The root helper checks the version and the
// signature itself.
func (d *Deps) replaceAgent(rel selfupdate.Release, allowDowngrade bool) error {
	if d.Gate != nil {
		return d.Gate.Call(context.Background(), opAgentReplace, replaceIn{Release: rel, AllowDowngrade: allowDowngrade}, nil)
	}
	if err := d.Updater.CheckVersion(rel.Version, allowDowngrade); err != nil {
		return err
	}
	if rel.Asset != selfupdate.AssetName(d.Arch) {
		return fmt.Errorf("the binary %q is not for this server", rel.Asset)
	}
	if err := d.Updater.Verify(rel); err != nil {
		return err
	}
	return d.Updater.Replace(rel.Binary)
}

// savePlatform keeps the gVisor platform in the settings of the agent. The root helper uses
// the new platform for the units that it writes.
func (d *Deps) savePlatform(platform string) error {
	if platform != "systrap" && platform != "kvm" {
		return errors.New("the gVisor platform must be systrap or kvm")
	}
	if d.Gate != nil {
		if err := d.Gate.Call(context.Background(), opPlatform, platformIn{Platform: platform}, nil); err != nil {
			return err
		}
	} else if d.SavePlatform != nil {
		if err := d.SavePlatform(platform); err != nil {
			return err
		}
	}
	d.rtMu.Lock()
	d.Gvisor.Platform = platform
	d.rtMu.Unlock()
	return nil
}

// SavePanelCAPin keeps the fingerprint of the root certificate of the panel CA in the settings
// of the agent. The worker learns the fingerprint from the panel, on a connection that it
// checked, and uses it for its own connections to the panel.
func (d *Deps) SavePanelCAPin(fingerprint string) error {
	if err := pki.CheckFingerprint(fingerprint); err != nil {
		return err
	}
	if d.Gate != nil {
		return d.Gate.Call(context.Background(), opPanelCA, pinIn{Fingerprint: fingerprint}, nil)
	}
	if d.SavePanelCA == nil {
		return errors.New("the agent cannot save the fingerprint of the panel CA")
	}
	return d.SavePanelCA(fingerprint)
}

// writeIni writes the php.ini of a website. Root owns the file, so the website cannot
// change it.
func (d *Deps) writeIni(ctx context.Context, unixUser, suffix, content string) error {
	if d.Gate != nil {
		return d.Gate.Call(ctx, opSiteIni, iniIn{User: unixUser, Suffix: suffix, Content: content}, nil)
	}
	_, err := host.WriteFile(d.iniPath(unixUser, suffix), content, 0o644)
	return err
}

// writeEnv writes the environment file of a container of a website (envFile). Only the Unix
// user of the website can read it: Podman reads it as that user, and the variables can have
// secrets, for example the password of a database.
func (d *Deps) writeEnv(ctx context.Context, unixUser, suffix, content string) error {
	if d.Gate != nil {
		return d.Gate.Call(ctx, opSiteEnv, iniIn{User: unixUser, Suffix: suffix, Content: content}, nil)
	}
	path := d.envPath(unixUser, suffix)
	if _, err := host.WriteFile(path, content, 0o600); err != nil {
		return err
	}
	if os.Geteuid() != 0 {
		// The tests do not run as root.
		return nil
	}
	uid, err := d.Host.LookupUID(unixUser)
	if err != nil {
		return err
	}
	gid, err := d.Host.GID(unixUser)
	if err != nil {
		return err
	}
	return os.Lchown(path, uid, gid)
}

// checkEnvFile checks each line of an environment file with the input rules, so that the
// root helper writes only variables of a customer.
func checkEnvFile(content string) error {
	lines := strings.Split(strings.TrimSuffix(content, "\n"), "\n")
	if content == "" {
		lines = nil
	}
	if len(lines) > validate.MaxEnvironmentVariables {
		return fmt.Errorf("a website can have %d environment variables", validate.MaxEnvironmentVariables)
	}
	for _, l := range lines {
		name, value, ok := strings.Cut(l, "=")
		if !ok {
			return errors.New("a line of the environment file has no \"=\"")
		}
		// A name that starts with CF_ is for a plugin that the panel runs (envNameOf).
		if err := errors.Join(envNameOf("custom", name), validate.EnvValue(value)); err != nil {
			return err
		}
	}
	return nil
}

// appExists reports whether a website has its directory.
func (d *Deps) appExists(ctx context.Context, unixUser string) (ok bool, err error) {
	if d.Gate != nil {
		err = d.Gate.Call(ctx, opSiteExists, userIn{User: unixUser}, &ok)
		return ok, err
	}
	if _, err := os.Stat(d.Host.AppDir(unixUser)); err != nil {
		return false, err
	}
	return true, nil
}

// panelDomain gives the domain of the panel on the panel server. Only the user of the panel
// and root can read the settings of the panel.
func (d *Deps) panelDomain(ctx context.Context) (domain string, err error) {
	if d.Gate != nil {
		err = d.Gate.Call(ctx, opPanelDomain, nil, &domain)
		return domain, err
	}
	return PanelDomain(PanelEnvFile(d.DataDir))
}

// markReboot keeps the ID of a reboot job, so that the agent can complete it after the reboot.
func (d *Deps) markReboot(ctx context.Context, jobID string) error {
	if d.Gate != nil {
		return d.Gate.Call(ctx, opRebootMark, jobIn{Job: jobID}, nil)
	}
	return os.WriteFile(d.rebootMarker(), []byte(jobID), 0o600)
}

// takeRebootMark gives the ID of the reboot job and removes the mark. It gives "" with no mark.
func (d *Deps) takeRebootMark(ctx context.Context) (id string) {
	if d.Gate != nil {
		_ = d.Gate.Call(ctx, opRebootTake, nil, &id)
		return id
	}
	b, err := os.ReadFile(d.rebootMarker())
	if err != nil {
		return ""
	}
	os.Remove(d.rebootMarker())
	return strings.TrimSpace(string(b))
}

func (d *Deps) clearRebootMark(ctx context.Context) {
	if d.Gate != nil {
		_ = d.Gate.Call(ctx, opRebootClear, nil, nil)
		return
	}
	os.Remove(d.rebootMarker())
}

// prepareEdge makes the directory of the admin socket of the edge proxy, and writes the unit
// of the edge proxy. It reports true when the unit changed. Only the edge user and root can
// open the directory (CF-13).
//
// The worker does not send the image and the data directory: the root helper uses its own
// settings.
func (d *Deps) prepareEdge(ctx context.Context, image, dataDir string) (changed bool, err error) {
	if d.Gate != nil {
		err = d.Gate.Call(ctx, opEdgePrepare, nil, &changed)
		return changed, err
	}
	if _, err := d.Run.Run(ctx, "install", "-d", "-o", host.EdgeUser, "-g", host.EdgeUser, "-m", "0700", filepath.Join(dataDir, quadlet.EdgeAdminDir)); err != nil {
		return false, err
	}
	unit := quadlet.EdgeUnit(quadlet.Edge{Image: image, DataDir: dataDir, Gvisor: d.runtime(), HTTPPort: edgeHTTPPort, HTTPSPort: edgeHTTPSPort})
	return d.Host.WriteUnit(host.EdgeUser, "cf-edge.container", unit)
}
