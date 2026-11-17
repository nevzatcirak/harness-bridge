//go:build unix

package acceptance

import (
	"errors"
	"os"
	"path"
	"sort"
	"strings"

	"golang.org/x/sys/unix"
)

// All operations below are relative to an open parent descriptor and never follow a symlink.

const dirFlags = unix.O_RDONLY | unix.O_DIRECTORY | unix.O_NOFOLLOW | unix.O_CLOEXEC

var errUnsafePath = errors.New("acceptance: a path component is a symlink, missing or not a directory (refused)")

func (o *OSDecoyFS) doFchown(fd int) error {
	if o.fchown != nil {
		return o.fchown(fd, o.UID, o.GID)
	}
	return unix.Fchown(fd, o.UID, o.GID)
}

func (o *OSDecoyFS) raceAt(p string) {
	if o.race != nil {
		o.race(p)
	}
}

func isDirMode(m uint32) bool { return m&uint32(unix.S_IFMT) == uint32(unix.S_IFDIR) }

func entryOf(p string, st *unix.Stat_t, dir bool) createdEntry {
	return createdEntry{path: p, dev: uint64(st.Dev), ino: uint64(st.Ino), dir: dir}
}

// keepOpen duplicates fd (close-on-exec) so the created object stays referenced; it returns -1 on
// failure, which the caller treats as a failed create.
func keepOpen(fd int) int {
	k, err := unix.FcntlInt(uintptr(fd), unix.F_DUPFD_CLOEXEC, 3)
	if err != nil {
		return -1
	}
	return k
}

func (o *OSDecoyFS) record(e createdEntry) {
	o.mu.Lock()
	o.created = append(o.created, e)
	o.mu.Unlock()
}

func (o *OSDecoyFS) find(p string, dir bool) (createdEntry, bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	for _, e := range o.created {
		if e.path == p && e.dir == dir {
			return e, true
		}
	}
	return createdEntry{}, false
}

func (o *OSDecoyFS) forget(e createdEntry) {
	o.mu.Lock()
	defer o.mu.Unlock()
	for i, x := range o.created {
		if x == e {
			o.created = append(o.created[:i], o.created[i+1:]...)
			if x.keep > 0 {
				unix.Close(x.keep)
			}
			return
		}
	}
}

// sameEntry reports whether name under parent is still the object recorded at create.
func sameEntry(parent int, name string, e createdEntry) (bool, error) {
	var st unix.Stat_t
	if err := unix.Fstatat(parent, name, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return false, err
	}
	isDir := isDirMode(uint32(st.Mode))
	return isDir == e.dir && uint64(st.Dev) == e.dev && uint64(st.Ino) == e.ino, nil
}

// mkdirOwned creates name under parent (0700), opens it without following, checks that it is a
// directory owned by this process (so it is the one just created), chowns the open descriptor and
// records it. It returns the descriptor of the new directory.
func (o *OSDecoyFS) mkdirOwned(parent int, name, full string) (int, error) {
	if err := unix.Mkdirat(parent, name, 0o700); err != nil {
		return -1, err
	}
	fd, err := unix.Openat(parent, name, dirFlags, 0)
	if err != nil {
		return -1, errUnsafePath // swapped between mkdirat and openat: nothing is followed or removed
	}
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil || !isDirMode(uint32(st.Mode)) || int(st.Uid) != os.Geteuid() {
		unix.Close(fd)
		return -1, errUnsafePath // not the directory this process just created
	}
	e := entryOf(full, &st, true)
	o.raceAt(full)
	if err := o.doFchown(fd); err != nil {
		if same, serr := sameEntry(parent, name, e); serr == nil && same {
			_ = unix.Unlinkat(parent, name, unix.AT_REMOVEDIR)
		}
		unix.Close(fd)
		return -1, err
	}
	if e.keep = keepOpen(fd); e.keep < 0 {
		if same, serr := sameEntry(parent, name, e); serr == nil && same {
			_ = unix.Unlinkat(parent, name, unix.AT_REMOVEDIR)
		}
		unix.Close(fd)
		return -1, errors.New("acceptance: cannot keep the created directory referenced")
	}
	o.record(e)
	return fd, nil
}

// walk opens dir component by component from "/" without following symlinks. With create, a
// missing component is created (and recorded); without it a missing component is an error.
func (o *OSDecoyFS) walk(dir string, create bool) (int, error) {
	if !cleanAbs(dir) && dir != "/" {
		return -1, errors.New("acceptance: refused path")
	}
	fd, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, err
	}
	cur := ""
	for _, comp := range strings.Split(strings.TrimPrefix(dir, "/"), "/") {
		if comp == "" {
			continue
		}
		cur += "/" + comp
		next, err := unix.Openat(fd, comp, dirFlags, 0)
		if err != nil && create && errors.Is(err, unix.ENOENT) {
			next, err = o.mkdirOwned(fd, comp, cur)
		} else if err != nil {
			err = errUnsafePath
		}
		unix.Close(fd)
		if err != nil {
			return -1, err
		}
		fd = next
	}
	return fd, nil
}

// Mkdir implements DecoyFS: it creates one directory (the parent must exist), records it, and fails
// when it already exists.
func (o *OSDecoyFS) Mkdir(p string) error {
	if !cleanAbs(p) {
		return errors.New("acceptance: refused directory path")
	}
	parent, err := o.walk(path.Dir(p), false)
	if err != nil {
		return err
	}
	defer unix.Close(parent)
	fd, err := o.mkdirOwned(parent, path.Base(p), p)
	if err != nil {
		return err
	}
	unix.Close(fd)
	return nil
}

// Write implements DecoyFS: missing ancestors are created and recorded, the file is created with
// O_CREAT|O_EXCL|O_NOFOLLOW, written and chowned through its descriptor.
func (o *OSDecoyFS) Write(p string, content []byte) error {
	if realNames[path.Base(p)] || !cleanAbs(p) {
		return errors.New("acceptance: refused decoy path")
	}
	parent, err := o.walk(path.Dir(p), true)
	if err != nil {
		return err
	}
	defer unix.Close(parent)
	name := path.Base(p)
	fd, err := unix.Openat(parent, name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return err
	}
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		unix.Close(fd)
		return err
	}
	e := entryOf(p, &st, false)
	f := os.NewFile(uintptr(fd), name)
	if _, err := f.Write(content); err != nil {
		_ = f.Close()
		unlinkIfSame(parent, name, e)
		return errors.New("acceptance: decoy write failed")
	}
	o.raceAt(p)
	if err := o.doFchown(int(f.Fd())); err != nil {
		_ = f.Close()
		unlinkIfSame(parent, name, e) // created here (O_EXCL): do not leave it with the wrong owner
		return err
	}
	if e.keep = keepOpen(int(f.Fd())); e.keep < 0 {
		_ = f.Close()
		unlinkIfSame(parent, name, e)
		return errors.New("acceptance: cannot keep the created file referenced")
	}
	if err := f.Close(); err != nil {
		unix.Close(e.keep)
		unlinkIfSame(parent, name, e)
		return errors.New("acceptance: decoy write failed")
	}
	o.record(e)
	return nil
}

// unlinkIfSame removes name under parent only while it is still the recorded object.
func unlinkIfSame(parent int, name string, e createdEntry) {
	if same, err := sameEntry(parent, name, e); err == nil && same {
		_ = unix.Unlinkat(parent, name, 0)
	}
}

// Remove implements DecoyFS: only a file this value created, after re-checking that the entry is
// still that file (device and inode), relative to the parent descriptor.
func (o *OSDecoyFS) Remove(p string) error {
	if realNames[path.Base(p)] || !cleanAbs(p) {
		return errors.New("acceptance: refused")
	}
	e, ok := o.find(p, false)
	if !ok {
		return errors.New("acceptance: refused: not a file this driver created")
	}
	parent, err := o.walk(path.Dir(p), false)
	if err != nil {
		return err
	}
	defer unix.Close(parent)
	same, err := sameEntry(parent, path.Base(p), e)
	if err != nil {
		if errors.Is(err, unix.ENOENT) {
			o.forget(e)
			return nil
		}
		return err
	}
	if !same {
		return errors.New("acceptance: refused: the entry is no longer the file this driver created")
	}
	if err := unix.Unlinkat(parent, path.Base(p), 0); err != nil && !errors.Is(err, unix.ENOENT) {
		return err
	}
	o.forget(e)
	return nil
}

// RemoveDir implements DecoyFS: the directories this value created at or under p, empty ones only,
// deepest first, each after the same device and inode check. Anything else is left alone.
func (o *OSDecoyFS) RemoveDir(p string) error {
	if !cleanAbs(p) {
		return errors.New("acceptance: refused")
	}
	o.mu.Lock()
	var mine []createdEntry
	for _, e := range o.created {
		if e.dir && (e.path == p || strings.HasPrefix(e.path, p+"/")) {
			mine = append(mine, e)
		}
	}
	o.mu.Unlock()
	sort.Slice(mine, func(i, j int) bool { return len(mine[i].path) > len(mine[j].path) })
	var first error
	for _, e := range mine {
		if err := o.removeOne(e); err != nil && first == nil {
			first = err
		}
	}
	return first
}

func (o *OSDecoyFS) removeOne(e createdEntry) error {
	parent, err := o.walk(path.Dir(e.path), false)
	if err != nil {
		return err // a swapped ancestor: nothing outside the created tree is touched
	}
	defer unix.Close(parent)
	same, err := sameEntry(parent, path.Base(e.path), e)
	if err != nil {
		if errors.Is(err, unix.ENOENT) {
			o.forget(e)
			return nil
		}
		return err
	}
	if !same {
		return errors.New("acceptance: refused: the entry is no longer the directory this driver created")
	}
	if err := unix.Unlinkat(parent, path.Base(e.path), unix.AT_REMOVEDIR); err != nil {
		if errors.Is(err, unix.ENOENT) {
			o.forget(e)
			return nil
		}
		return err // non-empty or busy: left in place and reported
	}
	o.forget(e)
	return nil
}
