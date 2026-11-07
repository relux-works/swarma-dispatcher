# R-DP1 logbook

2026-10-11. Docs-only contract repair prepared for review.

The six R-DP1 audit rows are aligned in spec/README and the three named existing diagrams. The newer owner preconditions resolve executor/first-consumer, managed PATH, clean wire naming and native-resume choices that were open in the pinned contract table. Their decisions are recorded as newer preconditions, without attributing them to the older commit.

Identity cleanup authority is limited to identities registered by the dispatcher as one-shot, separately after account retirement. Standing identity retirement remains operator-only. Unresolved helper retirement enum, helper v2 signature grammar and launch-ticket/host-role publication remain explicit questions with options; no extra wire values or queue bounds were chosen.

The internal v0 sweep is documented outside caller requests. Capacity v0 refusal/v1 queue and permanent quota refusal are distinct. Root caller correlation and receiver effect keys are distinct, and symbolic examples preserve the existing hash formula.

Documentation checks: standalone `git diff --check` exit 0 and `git diff --name-only` exit 0. The two standalone artifact `git diff --no-index --check /dev/null` checks each exited 1 (expected difference against an empty file) with no diagnostic; neither is claimed as passing. `git status --short` exited 0. No tests, builds, renderer or product lint suite ran under the task brief. Generic implementation checklist entries are replaced by documentation-specific entries; no unrun command is marked passing. See the alignment outcome for coverage and limits.
