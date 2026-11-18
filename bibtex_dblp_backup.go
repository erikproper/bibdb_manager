/*
 *
 * Module:    bibtex_check
 * Component:
 * - bibtex_dblp
 *   - bibtex_dblp_backup
 *
 * Nextcloud copy of the DBLP store (plan step 21.4). The store itself is used
 * locally in dblpFolder(); a copy of dblp.sqlite3 and the name-map CSVs is kept in
 * DBLP/ in the shared DB folder (shared_db_folder in the settings; by default the
 * library's folder, in Nextcloud). Another machine then
 * restores that copy instead of building the store from the XML.
 *
 * A push copies each file under a temporary name and renames it, so Nextcloud only
 * sees complete files. The stamp file dblp.stamp is removed first and written last:
 * a copy without a stamp is incomplete and is never restored from.
 *
 * The copy counts as newer than the local store when it was built from a later
 * DBLP XML (dblp-YYYY-MM-DD.xml.gz, so the basenames sort by date).
 *
 * Creator: Henderik A. Proper (e.proper@acm.org), Luxembourg, in collaboration with Claude.ai
 *
 * Version of: 18.11.2026
 *
 */

package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"syscall"
	"time"
)

const (
	dblpBackupStampName = "dblp.stamp"

	// dblpBackupPartSuffix marks a file that is still being copied.
	dblpBackupPartSuffix = ".part"
)

// dblpNameMapFiles are the name-map CSVs an import writes next to the store; the
// store is of no use without them.
var dblpNameMapFiles = []string{
	"dblp_name_key.csv",
	"dblp_orcid_key.csv",
	"dblp_key_canonical.csv",
	"dblp_key_bibtex.csv",
	"dblp_homepages_oldies.csv",
}

// TDblpBackupStamp describes the copy in the shared DB folder.
type TDblpBackupStamp struct {
	XMLFile  string `json:"xml_file"`  // basename of the XML the store was built from
	PushedAt string `json:"pushed_at"` // RFC 3339
	Machine  string `json:"machine"`
	Size     int64  `json:"size"` // of dblp.sqlite3, to tell a complete sync from a partial one
}

func dblpBackupFolder() string {
	return sharedDBFolder + dblpSubfolder
}

// readDblpBackupStamp returns the stamp of the Nextcloud copy, or nil when there is
// no (complete) copy.
func readDblpBackupStamp() *TDblpBackupStamp {
	data, err := os.ReadFile(dblpBackupFolder() + dblpBackupStampName)
	if err != nil {
		return nil
	}
	var stamp TDblpBackupStamp
	if json.Unmarshal(data, &stamp) != nil || stamp.XMLFile == "" {
		return nil
	}
	return &stamp
}

// copyFileViaPart copies src to dst through dst+".part", synced to disk before the
// rename, so dst is either absent, the old file, or the complete new one.
func copyFileViaPart(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	part := dst + dblpBackupPartSuffix
	out, err := os.Create(part)
	if err != nil {
		return err
	}
	_, err = io.CopyBuffer(out, in, make([]byte, 4<<20))
	if err == nil {
		err = out.Sync()
	}
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(part)
		return err
	}
	return os.Rename(part, dst)
}

// pushDblpStore copies the local store and its name-map CSVs to the shared DB folder.
// Called after a successful import, and by -push_dblp.
func pushDblpStore() error {
	xmlFile := readDblpCurrentXML()
	if xmlFile == "" {
		return fmt.Errorf("no DBLP store in %s", dblpFolder())
	}
	info, err := os.Stat(dblpDBPath())
	if err != nil {
		return err
	}
	dir := dblpBackupFolder()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	start := time.Now()
	messagef("Copying the DBLP store to %s ...\n", dir)
	if err := os.Remove(dir + dblpBackupStampName); err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := copyFileViaPart(dblpDBPath(), dir+dblpDBFileName); err != nil {
		return err
	}
	for _, name := range dblpNameMapFiles {
		if !FileExists(dblpFolder() + name) {
			continue // the optional files may be absent
		}
		if err := copyFileViaPart(dblpFolder()+name, dir+name); err != nil {
			return err
		}
	}

	machine, _ := os.Hostname()
	data, _ := json.MarshalIndent(TDblpBackupStamp{
		XMLFile:  xmlFile,
		PushedAt: time.Now().Format(time.RFC3339),
		Machine:  machine,
		Size:     info.Size(),
	}, "", "  ")
	part := dir + dblpBackupStampName + dblpBackupPartSuffix
	if err := os.WriteFile(part, data, 0o644); err != nil {
		return err
	}
	if err := os.Rename(part, dir+dblpBackupStampName); err != nil {
		return err
	}
	messagef("  Copied (%s, %.0fs).\n", xmlFile, time.Since(start).Seconds())
	return nil
}

// restoreDblpStore replaces the local store and its name-map CSVs with the
// Nextcloud copy. The copy goes to dblpDBNewPath() first and is checked there, so
// a bad or partly synced copy leaves the local store untouched.
func restoreDblpStore() error {
	stamp := readDblpBackupStamp()
	if stamp == nil {
		return fmt.Errorf("no complete DBLP store copy in %s", dblpBackupFolder())
	}
	src := dblpBackupFolder() + dblpDBFileName
	info, err := os.Stat(src)
	if err != nil {
		return err
	}
	if info.Size() != stamp.Size {
		return fmt.Errorf("the copy in %s is not fully synced yet (%d of %d bytes)",
			dblpBackupFolder(), info.Size(), stamp.Size)
	}

	start := time.Now()
	messagef("Restoring the DBLP store (%s, pushed by %s) ...\n", stamp.XMLFile, stamp.Machine)
	removeDblpDBFiles(dblpDBNewPath())
	if err := copyFileViaPart(src, dblpDBNewPath()); err != nil {
		removeDblpDBFiles(dblpDBNewPath())
		return err
	}
	if err := checkDblpDBNew(stamp.XMLFile); err != nil {
		removeDblpDBFiles(dblpDBNewPath())
		return err
	}
	for _, name := range dblpNameMapFiles {
		if !FileExists(dblpBackupFolder() + name) {
			continue
		}
		if err := copyFileViaPart(dblpBackupFolder()+name, dblpFolder()+name); err != nil {
			removeDblpDBFiles(dblpDBNewPath())
			return err
		}
	}
	if err := swapInDblpDBNew(); err != nil {
		return err
	}
	messagef("  Restored (%.0fs).\n", time.Since(start).Seconds())
	return nil
}

// checkDblpDBNew checks that dblpDBNewPath() is intact and was built from xmlFile.
func checkDblpDBNew(xmlFile string) error {
	db, err := sql.Open(sqliteDatabaseDriver, "file:"+dblpDBNewPath()+"?mode=ro")
	if err != nil {
		return err
	}
	defer db.Close()
	var check string
	if err := db.QueryRow(`PRAGMA quick_check`).Scan(&check); err != nil {
		return err
	}
	if check != "ok" {
		return fmt.Errorf("the restored copy fails quick_check: %s", check)
	}
	var built string
	if err := db.QueryRow(`SELECT value FROM meta WHERE name = ?`, dblpMetaXMLFile).Scan(&built); err != nil {
		return fmt.Errorf("the restored copy has no meta: %w", err)
	}
	if built != xmlFile {
		return fmt.Errorf("the restored copy was built from %s, not %s (sync still in progress?)", built, xmlFile)
	}
	return nil
}

// tryLockDblp takes the DBLP lock without waiting. It returns nil when another
// process (an import) holds it. The caller unlocks by closing the file.
func tryLockDblp() *os.File {
	f, err := os.OpenFile(dblpFolder()+"dblp"+LockFileExtension, os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return nil
	}
	if syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) != nil {
		f.Close()
		return nil
	}
	return f
}

// maybeRestoreDblpStore offers a restore when the Nextcloud copy is newer than the
// local store, or when there is no local store at all. Skipped while an import is
// running, as that will produce a newer store anyway.
func maybeRestoreDblpStore() {
	stamp := readDblpBackupStamp()
	if stamp == nil {
		return
	}
	lock := tryLockDblp()
	if lock == nil {
		return
	}
	defer lock.Close()

	local := readDblpCurrentXML()
	if local != "" && local >= stamp.XMLFile {
		return
	}
	var warning string
	if local == "" {
		warning = fmt.Sprintf("There is no local DBLP store, but %s holds a copy built from %s.",
			dblpBackupFolder(), stamp.XMLFile)
	} else {
		warning = fmt.Sprintf("The DBLP store copy in %s (%s, pushed by %s) is newer than the local store (%s).",
			dblpBackupFolder(), stamp.XMLFile, stamp.Machine, local)
	}
	if !isTTY {
		// A question would end a scripted run; just point at the flag.
		Reporting.Warning("%s Run with -restore_dblp to use it.", warning)
		return
	}
	if !Reporting.WarningYesNoQuestion("Restore the DBLP store from that copy", "%s", warning) {
		return
	}
	if err := restoreDblpStore(); err != nil {
		Reporting.Warning("Could not restore the DBLP store: %s", err)
	}
}
