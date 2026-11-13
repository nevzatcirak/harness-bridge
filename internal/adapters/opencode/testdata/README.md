# OpenCode native fixtures

`permission_1_19_4.json` is the permission fixture of the permission observer.

The `*_148842e.*` files are **derived from the upstream OpenCode source at commit
`148842ef7cb1bc34192b3fff57d0f9721ab9c801` (the pin of ADR-023 section 1.2), NOT captured
from a live server (UNVERIFIED).** Each is an example of a shape that source defines; none
is evidence of what a running OpenCode answers. They exist so the dashboard's four diff
labels, caps and redaction are tested against the documented shapes, not against shapes
guessed in a test.

| File | Route and shape (upstream citations in ADR-023 section 1.2) |
|---|---|
| `messages_summary_diffs_148842e.json` | `GET /session/:id/message`: array of `{info, parts}`; the user messages show `info.summary` absent, present without `diffs`, `diffs: []` and a non-empty `diffs` |
| `session_diff_148842e.json` | `GET /session/:id/diff?messageID=`: array of `FileDiff` (`file?`, `patch?`, `additions`, `deletions`, `status?`) |
| `vcs_diff_148842e.json` | `GET /vcs/diff?mode=git`: array of `Vcs.FileDiff` (`file` required); includes a file whose patch OpenCode replaced by the header-only `emptyPatch` (cap), and a binary notice |
| `vcs_diff_raw_148842e.txt` | `GET /vcs/diff/raw`: plain `text/x-diff` |

Secrets in these files are fake (`sk-ant-FAKE...`), only there to exercise redaction.
