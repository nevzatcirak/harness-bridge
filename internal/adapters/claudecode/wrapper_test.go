package claudecode

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

type fakeFS map[string]FileInfo

func (f fakeFS) Lstat(p string) (FileInfo, error) {
	if i, ok := f[p]; ok {
		return i, nil
	}
	return FileInfo{}, &fs.PathError{Op: "lstat", Path: p, Err: fs.ErrNotExist}
}

func rootDir(mode fs.FileMode) FileInfo { return FileInfo{Mode: fs.ModeDir | mode, UID: 0, Nlink: 2} }

func goodWrapperFS() fakeFS {
	return fakeFS{
		"/":                      rootDir(0o755),
		"/usr":                   rootDir(0o755),
		"/usr/local":             rootDir(0o755),
		"/usr/local/libexec":     rootDir(0o755),
		"/usr/local/libexec/hbc": FileInfo{Mode: 0o755, UID: 0, Nlink: 1},
	}
}

func TestValidateWrapper(t *testing.T) {
	const w = "/usr/local/libexec/hbc"
	if err := ValidateWrapper(goodWrapperFS(), w); err != nil {
		t.Fatalf("good wrapper refused: %v", err)
	}
	mut := func(f func(fakeFS)) fakeFS { m := goodWrapperFS(); f(m); return m }
	for name, tc := range map[string]struct {
		fsys fakeFS
		path string
	}{
		"relative path":      {goodWrapperFS(), "libexec/hbc"},
		"unclean path":       {goodWrapperFS(), "/usr/local/../local/libexec/hbc"},
		"shell string":       {goodWrapperFS(), "/usr/local/libexec/hbc --flag"},
		"missing":            {goodWrapperFS(), "/usr/local/libexec/nope"},
		"symlink":            {mut(func(m fakeFS) { m[w] = FileInfo{Mode: fs.ModeSymlink | 0o777, UID: 0} }), w},
		"directory":          {mut(func(m fakeFS) { m[w] = rootDir(0o755) }), w},
		"not root owned":     {mut(func(m fakeFS) { m[w] = FileInfo{Mode: 0o755, UID: 1000, Nlink: 1} }), w},
		"group writable":     {mut(func(m fakeFS) { m[w] = FileInfo{Mode: 0o775, UID: 0, Nlink: 1} }), w},
		"other writable":     {mut(func(m fakeFS) { m[w] = FileInfo{Mode: 0o757, UID: 0, Nlink: 1} }), w},
		"hard linked":        {mut(func(m fakeFS) { m[w] = FileInfo{Mode: 0o755, UID: 0, Nlink: 2} }), w},
		"dir other writable": {mut(func(m fakeFS) { m["/usr/local/libexec"] = rootDir(0o777) }), w},
		"dir group writable": {mut(func(m fakeFS) { m["/usr/local/libexec"] = rootDir(0o775) }), w},
		"dir not root":       {mut(func(m fakeFS) { m["/usr/local/libexec"] = FileInfo{Mode: fs.ModeDir | 0o755, UID: 1000, Nlink: 2} }), w},
		"ancestor writable":  {mut(func(m fakeFS) { m["/usr/local"] = rootDir(0o777) }), w},
		"ancestor symlink":   {mut(func(m fakeFS) { m["/usr/local"] = FileInfo{Mode: fs.ModeSymlink | 0o777, UID: 0} }), w},
		"setuid":             {mut(func(m fakeFS) { m[w] = FileInfo{Mode: fs.ModeSetuid | 0o755, UID: 0, Nlink: 1} }), w},
	} {
		if err := ValidateWrapper(tc.fsys, tc.path); err == nil {
			t.Errorf("%s: wrapper accepted", name)
		}
	}
	if err := ValidateWrapper(goodWrapperFS(), w); err != nil && errors.Is(err, fs.ErrNotExist) {
		t.Fatal("unexpected")
	}
}

func TestValidateHistoryDir(t *testing.T) {
	const svc = 1500
	good := func() fakeFS {
		return fakeFS{
			"/":                  rootDir(0o755),
			"/var":               rootDir(0o755),
			"/var/lib":           rootDir(0o755),
			"/var/lib/hb":        FileInfo{Mode: fs.ModeDir | 0o755, UID: svc, Nlink: 2},
			"/var/lib/hb/claude": FileInfo{Mode: fs.ModeDir | 0o700, UID: svc, Nlink: 2},
		}
	}
	const d = "/var/lib/hb/claude"
	if err := ValidateHistoryDir(good(), d, svc); err != nil {
		t.Fatalf("good dir refused: %v", err)
	}
	mut := func(f func(fakeFS)) fakeFS { m := good(); f(m); return m }
	for name, tc := range map[string]struct {
		fsys fakeFS
		path string
	}{
		"relative":          {good(), "var/lib/hb/claude"},
		"missing":           {good(), "/var/lib/hb/other"},
		"mode 0750":         {mut(func(m fakeFS) { m[d] = FileInfo{Mode: fs.ModeDir | 0o750, UID: svc, Nlink: 2} }), d},
		"mode 0755":         {mut(func(m fakeFS) { m[d] = FileInfo{Mode: fs.ModeDir | 0o755, UID: svc, Nlink: 2} }), d},
		"mode 0770":         {mut(func(m fakeFS) { m[d] = FileInfo{Mode: fs.ModeDir | 0o770, UID: svc, Nlink: 2} }), d},
		"mode 0500":         {mut(func(m fakeFS) { m[d] = FileInfo{Mode: fs.ModeDir | 0o500, UID: svc, Nlink: 2} }), d},
		"other owner":       {mut(func(m fakeFS) { m[d] = FileInfo{Mode: fs.ModeDir | 0o700, UID: 0, Nlink: 2} }), d},
		"claude user owner": {mut(func(m fakeFS) { m[d] = FileInfo{Mode: fs.ModeDir | 0o700, UID: 1600, Nlink: 2} }), d},
		"final symlink":     {mut(func(m fakeFS) { m[d] = FileInfo{Mode: fs.ModeSymlink | 0o777, UID: svc} }), d},
		"a regular file":    {mut(func(m fakeFS) { m[d] = FileInfo{Mode: 0o600, UID: svc, Nlink: 1} }), d},
		"parent writable":   {mut(func(m fakeFS) { m["/var/lib/hb"] = FileInfo{Mode: fs.ModeDir | 0o777, UID: svc, Nlink: 2} }), d},
		"parent group w":    {mut(func(m fakeFS) { m["/var/lib/hb"] = FileInfo{Mode: fs.ModeDir | 0o775, UID: svc, Nlink: 2} }), d},
		"parent foreign":    {mut(func(m fakeFS) { m["/var/lib/hb"] = FileInfo{Mode: fs.ModeDir | 0o755, UID: 4242, Nlink: 2} }), d},
		"parent symlink":    {mut(func(m fakeFS) { m["/var/lib"] = FileInfo{Mode: fs.ModeSymlink | 0o777, UID: 0} }), d},
	} {
		if err := ValidateHistoryDir(tc.fsys, tc.path, svc); err == nil {
			t.Errorf("%s: history dir accepted", name)
		}
	}
	// A sticky, root-owned, world-writable ancestor (like /tmp) is acceptable.
	m := good()
	m["/var/lib"] = FileInfo{Mode: fs.ModeDir | fs.ModeSticky | 0o777, UID: 0, Nlink: 2}
	if err := ValidateHistoryDir(m, d, svc); err != nil {
		t.Fatalf("sticky root-owned ancestor refused: %v", err)
	}
}

// The OS-backed FS reports real modes and owners.
func TestOSFSLstat(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "f")
	if err := os.WriteFile(f, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := OSFS{}.Lstat(f)
	if err != nil || info.Mode.Perm() != 0o600 || info.Nlink != 1 || info.UID != uint32(os.Geteuid()) {
		t.Fatalf("lstat = %+v %v", info, err)
	}
	link := filepath.Join(dir, "l")
	if err := os.Symlink(f, link); err != nil {
		t.Fatal(err)
	}
	if li, err := (OSFS{}).Lstat(link); err != nil || li.Mode&fs.ModeSymlink == 0 {
		t.Fatalf("symlink not reported: %+v %v", li, err)
	}
}
