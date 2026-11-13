//go:build !linux

package claudecode

// Off linux the operator files cannot be read safely, so loading fails closed.
func ReadConfigFile(string, uint32, int64) ([]byte, error) { return nil, errUnsupportedPlatform }

// LoadSessions fails closed off linux.
func LoadSessions(FS, string, uint32, []string) ([]Session, error) {
	return nil, errUnsupportedPlatform
}
