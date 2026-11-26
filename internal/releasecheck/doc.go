// Package releasecheck holds the tests of the release pipeline (ADR-026 section 3 and 4.1, slice S2): the
// structure of the workflows and of .goreleaser.yaml, the release gates of scripts/release, and install.sh.
// It has no production code: the artifacts it checks are files of the repository, and the tests read and run
// them. Nothing here signs, publishes or contacts a network.
package releasecheck
