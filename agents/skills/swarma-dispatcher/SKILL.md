---
name: swarma-dispatcher
description: Use Swarma Dispatcher's capacity-refusal CLI and internal Go components; distinguish implemented transport from proposed launch contracts.
---

# Swarma Dispatcher

Use this skill for this module's command contract, byte framing, admission state,
or launch-design questions. Source availability does not establish an installed
command or a qualified deployment.

- For the implemented Linux commands, protected configuration, response semantics
  and an inert wire example, read [capacity transport](references/transport.md).
- For repository-local framing or admission work, read
  [internal components](references/components.md). These Go packages live under
  `internal/`; they are not a public SDK for unrelated external modules.
- For launch, cancellation, workspace transfer, signing or executor-entry design,
  start with the [implementation status](../../../README.md), then read only the
  relevant [dispatcher specification](../../../spec/dispatcher.md),
  [M0 launch candidate](../../../spec/m0-launch.md) or
  [executor-entry proposal](../../../spec/m0-executor-entry.md).
  Those designs do not add callable operations to the current transport.

Every valid capacity exchange returns `capacity:hold`; negotiation success does
not open admission. There is no implemented launch service, plugin registration
API or configuration-based plugin loader. Do not translate proposed operations
into assumed commands or treat schema-valid M0 specimens as authorized requests.
For M0 shape questions, use the existing [fixture guide](../../../fixtures/m0-launch/README.md),
which distinguishes shape checks from pending runtime enforcement.

Report the actual response or component result and what it establishes. Keep
unknown effects unresolved; neither a frame, a local admission record nor a
capacity refusal proves a process absent or authorizes a new launch.
