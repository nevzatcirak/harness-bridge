# H4-B evidence provenance

The machine-readable H4-B evidence files below were produced on 2026-10-25 for
candidate commit `fdd51181b7a7bb441ee6e3557bbcc8542a1a9612` (PR #5), built on base
`0b3c319c96b0da0f921d63e8be6e5b063c66b0fc`. They are carried unchanged by the
MIG-A-02 reconciliation (PR #10) as historical evidence. Their `base_revision` and
timestamp fields name the base, not the candidate. They are **not** evidence for
any later head, including the MIG-A-02 merge commit.

| File | Content | Candidate revision |
|---|---|---|
| `docs/H4_B_TEST_RESULTS.json` | gofmt/build/vet/test/race and fixture results | `fdd5118` |
| `docs/H4_B_LIVE_READ_ONLY.json` | sanitized live read-only native observation | `fdd5118` |
| `tests/confinement/evidence/kernel-results.json` | opt-in privileged kernel fixture results | `fdd5118` |
| `tests/confinement/evidence/unit-verify.txt` | systemd static verification output | `fdd5118` |

The pre-submission semantics described alongside that evidence are superseded by
P2-REC-02B. See ADR-014, CONTRACTS.md and the note in H4_B_SECURITY_EVIDENCE.md.
Before any acceptance use, regenerate the evidence for the exact head under review.
