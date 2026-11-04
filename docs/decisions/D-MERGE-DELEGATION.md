# D-MERGE-DELEGATION — merge authority delegated to the Planning Desk

Date: 2026-11-04. Decided by: maintainer. Status: in effect.

## Decision

The maintainer delegated merge authority to the Planning Desk. A merge is
allowed only when every gate below holds:

1. Exact-head CI on the current base.
2. An independent exact-SHA review with verified provenance, and no push after
   the review.
3. A planner gate comment.
4. A merge commit with a SHA lock.
5. A check of the `main` push CI after the merge.

Workers never merge.

## Notes

- Merge commit is the only merge method the `main` ruleset allows. See
  [REPOSITORY_RULES.md](../REPOSITORY_RULES.md).
- The ruleset requires 0 approvals and does not enforce strict up-to-date
  branches. This decision's gate supplies those controls as process.
- Tags, releases and deployment remain with the maintainer (AGENTS.md).
