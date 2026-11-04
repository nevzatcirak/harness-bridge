# Evidence provenance

The files in this directory are historical results for candidate commit
`fdd51181b7a7bb441ee6e3557bbcc8542a1a9612` (2026-10-25). They are not evidence for
any later head. See [docs/H4_B_EVIDENCE_PROVENANCE.md](../../../docs/H4_B_EVIDENCE_PROVENANCE.md).

Host provenance (R2-PR5-FIXTURE-REVIEW F-B, recorded 2026-11-04): these files do
not name the host that produced them. The independent review found leftover
fixture directories from 2026-10-25 and 2026-10-27 on the host that runs the
production Bridge and OpenCode, and hypothesised that `kernel-results.json` was
produced there. That is unconfirmed. Treat these results as produced on a
non-disposable host until a re-run on a named disposable host replaces them.
Those leftover directories were left untouched; removing them is an operator
decision.
