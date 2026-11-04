# Repository rules

Recorded 2026-11-04 from the live API:
`gh api repos/nevzatcirak/harness-bridge/rulesets/26467411`
(ruleset `updated_at` 2026-11-03T20:13:05.176Z). The ruleset is authoritative;
re-read it before relying on this summary.

## Ruleset `main` (id 26467411)

| Setting | Live value |
| --- | --- |
| Target | branch; include `~DEFAULT_BRANCH`, `refs/heads/main`; exclude none |
| Enforcement | `active` |
| Bypass | `bypass_actors: []`; `current_user_can_bypass: never` |
| Deletion | protected (`deletion` rule) |
| Force push / history rewrite | protected (`non_fast_forward` rule) |
| Pull request required | yes |
| Allowed merge methods | `merge` only |
| Required approving reviews | 0 |
| Dismiss stale reviews on push | false |
| Require code owner review | false |
| Require last push approval | false |
| Require review thread resolution | false |
| `require_extra_approval_for_unattributed_changes` | true (semantics unverified) |
| Required status checks | `Go checks` (no integration/app pinned) |
| Strict (branch up to date before merge) | false |
| `do_not_enforce_on_create` | false |

## Consequences

- `Go checks` is the job name in `.github/workflows/ci.yml`. Renaming the job
  breaks the required check.
- Strict mode is off, so GitHub does not force a PR head to contain the
  current `main`. The merge gate in
  [D-MERGE-DELEGATION](decisions/D-MERGE-DELEGATION.md) requires exact-head CI
  on the current base instead.
- Zero required approvals means GitHub does not enforce review. Review is a
  process gate, recorded in the decision above.
- The effect of `require_extra_approval_for_unattributed_changes` has not been
  verified. Do not rely on it as a control.
