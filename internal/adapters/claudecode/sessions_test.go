package claudecode

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const otherUUID = "00000000-0000-4000-8000-000000000001"

func sessJSON(entries ...string) []byte { return []byte("[" + strings.Join(entries, ",") + "]") }
func ent(id, root string) string {
	return `{"session_id":"` + id + `","root":"` + root + `","label":"x"}`
}

func TestParseSessions(t *testing.T) {
	roots := []string{"/srv/projects"}
	good, err := ParseSessions(sessJSON(ent("claudecode:"+testUUID, "/srv/projects/a"), ent("claudecode:"+otherUUID, "/srv/projects")), roots)
	if err != nil || len(good) != 2 || good[0].UUID() != testUUID || good[1].Root != "/srv/projects" {
		t.Fatalf("good = %+v, %v", good, err)
	}
	bad := map[string][]byte{
		"empty document":        []byte(``),
		"not a list":            []byte(`{}`),
		"trailing data":         []byte(`[] []`),
		"unknown field":         []byte(`[{"session_id":"claudecode:` + testUUID + `","root":"/srv/projects/a","x":1}]`),
		"opencode id":           sessJSON(ent("opencode:ses_abc", "/srv/projects/a")),
		"uppercase uuid":        sessJSON(ent("claudecode:"+strings.ToUpper(testUUID), "/srv/projects/a")),
		"bare uuid":             sessJSON(ent(testUUID, "/srv/projects/a")),
		"duplicate":             sessJSON(ent("claudecode:"+testUUID, "/srv/projects/a"), ent("claudecode:"+testUUID, "/srv/projects/b")),
		"relative root":         sessJSON(ent("claudecode:"+testUUID, "projects/a")),
		"unclean root":          sessJSON(ent("claudecode:"+testUUID, "/srv/projects/../projects/a")),
		"trailing slash root":   sessJSON(ent("claudecode:"+testUUID, "/srv/projects/a/")),
		"root of the fs":        sessJSON(ent("claudecode:"+testUUID, "/")),
		"outside allowed roots": sessJSON(ent("claudecode:"+testUUID, "/etc")),
		"prefix sibling":        sessJSON(ent("claudecode:"+testUUID, "/srv/projects-evil/a")),
		"long label":            sessJSON(`{"session_id":"claudecode:` + testUUID + `","root":"/srv/projects/a","label":"` + strings.Repeat("a", 65) + `"}`),
		"newline label":         sessJSON(`{"session_id":"claudecode:` + testUUID + `","root":"/srv/projects/a","label":"a\nb"}`),
	}
	for name, data := range bad {
		if _, err := ParseSessions(data, roots); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	var many []string
	for i := 0; i <= MaxSessions; i++ {
		many = append(many, ent("claudecode:00000000-0000-4000-8000-"+strings.Repeat("0", 12-len(itoa(i)))+itoa(i), "/srv/projects/a"))
	}
	if _, err := ParseSessions(sessJSON(many[:MaxSessions]...), roots); err != nil {
		t.Errorf("exactly %d entries must load: %v", MaxSessions, err)
	}
	if _, err := ParseSessions(sessJSON(many...), roots); err == nil {
		t.Errorf("%d entries must fail", MaxSessions+1)
	}
	if _, err := ParseSessions(sessJSON(), []string{"relative"}); err == nil {
		t.Error("an unclean allowed root must fail")
	}
}

func TestCheckSessionACL(t *testing.T) {
	list := []Session{{ID: "claudecode:" + testUUID}, {ID: "claudecode:" + otherUUID}}
	if err := CheckSessionACL(list, []string{"claudecode:" + testUUID, "claudecode:" + otherUUID, "opencode:ses_x"}); err != nil {
		t.Fatal(err)
	}
	if err := CheckSessionACL(list, []string{"claudecode:" + testUUID}); err == nil {
		t.Fatal("a listed session missing from the ACL must fail")
	}
}

func goodConfigFS(uid uint32) fakeFS {
	return fakeFS{
		"/":               rootDir(0o755),
		"/etc":            rootDir(0o755),
		"/etc/hb":         rootDir(0o755),
		"/etc/hb/sess":    FileInfo{Mode: 0o600, UID: uid, Nlink: 1},
		"/etc/hb/sticky":  FileInfo{Mode: 0o600, UID: uid, Nlink: 1},
		"/tmp":            FileInfo{Mode: fs.ModeDir | fs.ModeSticky | 0o777, UID: 0, Nlink: 2},
		"/tmp/sess":       FileInfo{Mode: 0o600, UID: uid, Nlink: 1},
		"/tmp/loose":      FileInfo{Mode: 0o640, UID: uid, Nlink: 1},
		"/etc/hb/foreign": FileInfo{Mode: 0o600, UID: 7, Nlink: 1},
	}
}

func TestValidateConfigFile(t *testing.T) {
	const uid = 996
	f := goodConfigFS(uid)
	if err := ValidateConfigFile(f, "/etc/hb/sess", uid); err != nil {
		t.Fatalf("good file refused: %v", err)
	}
	if err := ValidateConfigFile(f, "/tmp/sess", uid); err != nil {
		t.Fatalf("a file under a sticky root directory is accepted: %v", err)
	}
	mut := func(fn func(fakeFS)) fakeFS { m := goodConfigFS(uid); fn(m); return m }
	for name, tc := range map[string]struct {
		fsys fakeFS
		path string
	}{
		"relative":       {f, "hb/sess"},
		"unclean":        {f, "/etc/hb/../hb/sess"},
		"missing":        {f, "/etc/hb/none"},
		"mode 0640":      {f, "/tmp/loose"},
		"foreign owner":  {f, "/etc/hb/foreign"},
		"symlink":        {mut(func(m fakeFS) { m["/etc/hb/sess"] = FileInfo{Mode: fs.ModeSymlink | 0o600, UID: uid} }), "/etc/hb/sess"},
		"directory":      {mut(func(m fakeFS) { m["/etc/hb/sess"] = rootDir(0o700) }), "/etc/hb/sess"},
		"hard linked":    {mut(func(m fakeFS) { m["/etc/hb/sess"] = FileInfo{Mode: 0o600, UID: uid, Nlink: 2} }), "/etc/hb/sess"},
		"ancestor group": {mut(func(m fakeFS) { m["/etc/hb"] = rootDir(0o775) }), "/etc/hb/sess"},
		"ancestor link":  {mut(func(m fakeFS) { m["/etc/hb"] = FileInfo{Mode: fs.ModeSymlink | 0o777} }), "/etc/hb/sess"},
		"ancestor owner": {mut(func(m fakeFS) { m["/etc/hb"] = FileInfo{Mode: fs.ModeDir | 0o755, UID: 7, Nlink: 2} }), "/etc/hb/sess"},
	} {
		if err := ValidateConfigFile(tc.fsys, tc.path, uid); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestLoadSessionsFromAFile(t *testing.T) {
	dir := t.TempDir()
	roots := []string{dir}
	write := func(name string, mode os.FileMode, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), mode); err != nil {
			t.Fatal(err)
		}
		_ = os.Chmod(p, mode)
		return p
	}
	uid := uint32(os.Geteuid())
	okp := write("ok.json", 0o600, string(sessJSON(ent("claudecode:"+testUUID, dir+"/p"))))
	got, err := LoadSessions(OSFS{}, okp, uid, roots)
	if err != nil || len(got) != 1 {
		t.Fatalf("load: %+v, %v", got, err)
	}
	if _, err := LoadSessions(OSFS{}, write("loose.json", 0o644, "[]"), uid, roots); err == nil {
		t.Error("mode 0644 must be refused")
	}
	if _, err := LoadSessions(OSFS{}, okp, uid+1, roots); err == nil {
		t.Error("a file owned by another user must be refused")
	}
	link := filepath.Join(dir, "link.json")
	_ = os.Symlink(okp, link)
	if _, err := LoadSessions(OSFS{}, link, uid, roots); err == nil {
		t.Error("a symlink must be refused")
	}
	// The descriptor check holds even if the path check is bypassed.
	if _, err := ReadConfigFile(link, uid, 1<<20); err == nil {
		t.Error("ReadConfigFile followed a symlink")
	}
	big := write("big.json", 0o600, "["+strings.Repeat(" ", 2<<20)+"]")
	if _, err := LoadSessions(OSFS{}, big, uid, roots); err == nil {
		t.Error("an oversized file must be refused, not truncated")
	}
}
