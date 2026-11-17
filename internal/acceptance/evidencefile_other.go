//go:build !unix

package acceptance

import (
	"errors"
	"os"
)

// CreateEvidenceFile is refused on this platform (the live driver is unix-only).
func CreateEvidenceFile(p string) (*os.File, error) {
	return nil, errors.New("acceptance: the evidence file needs a unix platform (fails closed)")
}
