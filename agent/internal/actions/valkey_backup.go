package actions

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

// The backup of a Valkey database (switch Deps.ValkeyBackups, off by default).
//
// Root makes the dump and loads it, as the root helper writes the dumps of the SQL engines:
// the dump directory is for root only (audit finding R8), and only root can read the password
// of the Valkey administrator. The dump has each key of the database number with DUMP, and
// the time at which it expires (PEXPIRETIME). A restore empties the database number and
// loads each key with RESTORE ... ABSTTL. SCAN is not one point in time: a key that the
// website changes during the dump has its value from before or after the change.
//
// The format of the dump file:
//
//	"CFVALKEY1\n"
//	for each key: 'K', uvarint length, key, uvarint length, DUMP value, varint expire time
//	              in Unix milliseconds (0: no expire time)
//	'E', uvarint number of keys

const valkeyDumpMagic = "CFVALKEY1\n"

// maxValkeyDumpValue is the maximum size of one key or one DUMP value. It is the default
// maximum size of a string in Valkey (proto-max-bulk-len).
const maxValkeyDumpValue = 512 << 20

// valkeyBackupIn is the value of the operations valkey.dump and valkey.restore.
type valkeyBackupIn struct {
	Job, User, Name string
	Server          dbServer
}

// errNoValkeyBackups is the error when the switch is off.
var errNoValkeyBackups = errors.New("the backups of Valkey databases are off on this server (valkeyBackups in agent.json)")

// backupEngine checks the engine of a database in a backup or a restore. The SQL engines
// can always have a backup. Valkey can have one only when the switch is on.
func (d *Deps) backupEngine(engine string) error {
	if isNumbered(engine) && d.ValkeyBackups {
		return nil
	}
	return sqlEngine(engine)
}

// valkeyNumberOf gives the database number of a Valkey database (its user).
func valkeyNumberOf(v *valkeyConn, name string) (int, error) {
	users, err := valkeyUsers(v)
	if err != nil {
		return 0, err
	}
	number, ok := users[name]
	if !ok || number < 1 {
		return 0, fmt.Errorf("the Valkey database %s does not exist", name)
	}
	return number, nil
}

// valkeyDumpToFile writes the dump of a Valkey database to its file in the dump directory of
// the website, and gives the SHA-256 hash of the dump.
func (d *Deps) valkeyDumpToFile(ctx context.Context, s dbServer, unixUser, name string) (sum string, err error) {
	if !d.ValkeyBackups {
		return "", errNoValkeyBackups
	}
	if d.Gate != nil {
		err = d.Gate.Call(ctx, opValkeyDump, valkeyBackupIn{User: unixUser, Name: name, Server: s}, &sum)
		return sum, err
	}
	v, err := d.valkeyService(ctx, s)
	if err != nil {
		return "", err
	}
	defer v.Close()
	v.maxBulk = maxValkeyDumpValue
	number, err := valkeyNumberOf(v, name)
	if err != nil {
		return "", err
	}
	if _, err := v.do("SELECT", strconv.Itoa(number)); err != nil {
		return "", err
	}
	f, err := os.OpenFile(filepath.Join(d.dumpDir(unixUser), dumpFile(s.Engine, name)), os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	w := bufio.NewWriter(io.MultiWriter(f, h))
	err = valkeyWriteDump(ctx, v, w)
	if ferr := w.Flush(); err == nil {
		err = ferr
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return "", fmt.Errorf("dump the Valkey database %s: %w", name, err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// valkeyWriteDump writes the keys of the selected database in the format of the dump file.
func valkeyWriteDump(ctx context.Context, v *valkeyConn, w io.Writer) error {
	if _, err := io.WriteString(w, valkeyDumpMagic); err != nil {
		return err
	}
	var count uint64
	cursor := "0"
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		_ = v.c.SetDeadline(time.Now().Add(valkeyTimeout))
		res, err := v.do("SCAN", cursor, "COUNT", "500")
		if err != nil {
			return err
		}
		page, ok := res.([]any)
		if !ok || len(page) != 2 {
			return errors.New("valkey: unexpected answer to SCAN")
		}
		cursor, _ = page[0].(string)
		keys, _ := page[1].([]any)
		for _, k := range keys {
			key, _ := k.(string)
			_ = v.c.SetDeadline(time.Now().Add(valkeyTimeout))
			value, err := v.do("DUMP", key)
			if err != nil {
				return err
			}
			if value == nil {
				// The key expired or went after SCAN.
				continue
			}
			expire, err := v.do("PEXPIRETIME", key)
			if err != nil {
				return err
			}
			at, _ := expire.(int64)
			if at == -2 {
				continue
			}
			if at < 0 {
				at = 0
			}
			if err := writeValkeyRecord(w, key, value.(string), at); err != nil {
				return err
			}
			count++
		}
		if cursor == "0" || cursor == "" {
			break
		}
	}
	_, err := w.Write(binary.AppendUvarint([]byte{'E'}, count))
	return err
}

func writeValkeyRecord(w io.Writer, key, value string, expireAt int64) error {
	b := []byte{'K'}
	b = binary.AppendUvarint(b, uint64(len(key)))
	b = append(b, key...)
	b = binary.AppendUvarint(b, uint64(len(value)))
	if _, err := w.Write(b); err != nil {
		return err
	}
	if _, err := io.WriteString(w, value); err != nil {
		return err
	}
	_, err := w.Write(binary.AppendVarint(nil, expireAt))
	return err
}

// valkeyRecord is one key of a dump file.
type valkeyRecord struct {
	key, value string
	expireAt   int64
}

// readValkeyDump reads a dump file. each gets each key. It checks the end of the file: a dump
// that is not complete gives an error.
func readValkeyDump(r io.Reader, each func(valkeyRecord) error) error {
	br := bufio.NewReader(r)
	magic := make([]byte, len(valkeyDumpMagic))
	if _, err := io.ReadFull(br, magic); err != nil || string(magic) != valkeyDumpMagic {
		return errors.New("the file is not a Valkey dump of the agent")
	}
	readString := func() (string, error) {
		n, err := binary.ReadUvarint(br)
		if err != nil {
			return "", err
		}
		if n > maxValkeyDumpValue {
			return "", errors.New("the Valkey dump has a value that is too large")
		}
		b := make([]byte, n)
		_, err = io.ReadFull(br, b)
		return string(b), err
	}
	var count uint64
	for {
		kind, err := br.ReadByte()
		if err != nil {
			return errors.New("the Valkey dump is not complete")
		}
		switch kind {
		case 'K':
			var rec valkeyRecord
			if rec.key, err = readString(); err != nil {
				return fmt.Errorf("read the Valkey dump: %w", err)
			}
			if rec.value, err = readString(); err != nil {
				return fmt.Errorf("read the Valkey dump: %w", err)
			}
			if rec.expireAt, err = binary.ReadVarint(br); err != nil {
				return fmt.Errorf("read the Valkey dump: %w", err)
			}
			if err := each(rec); err != nil {
				return err
			}
			count++
		case 'E':
			n, err := binary.ReadUvarint(br)
			if err != nil || n != count {
				return errors.New("the Valkey dump is not complete")
			}
			if _, err := br.ReadByte(); err != io.EOF {
				return errors.New("the Valkey dump has data after its end")
			}
			return nil
		default:
			return errors.New("the Valkey dump is not valid")
		}
	}
}

// valkeyRestoreStaged loads the restored dump of a Valkey database into its database number.
// The database first loses all its keys, so that the result is the database of the backup.
// The caller checked the hash of the dump. The dump is read one time before the database
// changes, so that a dump that is not complete does not empty the database.
func (d *Deps) valkeyRestoreStaged(ctx context.Context, jobID, unixUser string, s dbServer, name string) error {
	if !d.ValkeyBackups {
		return errNoValkeyBackups
	}
	if d.Gate != nil {
		return d.Gate.Call(ctx, opValkeyRestore, valkeyBackupIn{Job: jobID, User: unixUser, Name: name, Server: s}, nil)
	}
	check, err := d.stagedDump(jobID, unixUser, s.Engine, name)
	if err != nil {
		return err
	}
	err = readValkeyDump(check, func(valkeyRecord) error { return nil })
	check.Close()
	if err != nil {
		return err
	}
	d.valkeyMu.Lock()
	defer d.valkeyMu.Unlock()
	v, err := d.valkeyService(ctx, s)
	if err != nil {
		return err
	}
	defer v.Close()
	number, err := valkeyNumberOf(v, name)
	if err != nil {
		return err
	}
	if _, err := v.do("SELECT", strconv.Itoa(number)); err != nil {
		return err
	}
	if _, err := v.do("FLUSHDB"); err != nil {
		return err
	}
	f, err := d.stagedDump(jobID, unixUser, s.Engine, name)
	if err != nil {
		return err
	}
	defer f.Close()
	return readValkeyDump(f, func(rec valkeyRecord) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		_ = v.c.SetDeadline(time.Now().Add(valkeyTimeout))
		args := []string{"RESTORE", rec.key, strconv.FormatInt(rec.expireAt, 10), rec.value, "REPLACE"}
		if rec.expireAt > 0 {
			args = append(args, "ABSTTL")
		}
		_, err := v.do(args...)
		return err
	})
}
