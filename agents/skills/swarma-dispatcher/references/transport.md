# Capacity-refusal transport

Read the [transport contract](../../../../spec/transport.md) for deployment and
wire details. The implemented entrypoints are the two `main` functions in
[dispatcher](../../../../cmd/swarma-dispatcher/main.go) and
[capacity](../../../../cmd/swarma-capacity/main.go), backed by `transport.Daemon`
and `transport.Capacity` in [service_linux.go](../../../../internal/transport/service_linux.go).
Non-Linux builds return `ErrUnsupported` through [unsupported.go](../../../../internal/transport/unsupported.go).

## Command contract

The following are argument synopses for separately supplied binaries, not an
installation or startup procedure:

```text
swarma-dispatcher --config /absolute/server.json
swarma-capacity --config /absolute/client.json
```

Each command requires exactly those two arguments. There are no other flags,
environment defaults, endpoint overrides, account-name lookups or reloads.
Do not fabricate configuration pins to make an example executable.

- The daemon emits failures to stderr and exits 1. SIGINT/SIGTERM initiates
  idempotent shutdown; successful owned cleanup exits 0. Cleanup/ownership/budget
  failures remain failures, and no readiness record is printed.
- The client makes one exchange without automatic reconnect. On a verified hold,
  it prints the exact refusal JSON plus a newline and exits **2**. Local,
  configuration, protocol or output failure exits **1** with stderr diagnostics.
  A successful transport exchange is still closed admission.

## Configuration boundary

The two closed config shapes are defined by `commonConfig`, `principal`,
`serverConfig`, `clientConfig`, `configKeys`, `parseServer` and `parseClient` in
[config.go](../../../../internal/transport/config.go). Common keys are
`connect_gid`, `endpoint_dev`, `endpoint_ino`, `service_gid`, `service_uid`, `v`.
Server config adds `callers`; client config adds `caller`. Each principal has
exactly `dev`, `dir`, `ino`, `uid`. Server callers are 1..32, strictly increasing
by UID. The byte limits are 16384 server / 4096 client; `v` must be 1.

Use complete pre-provisioned protected snapshots. `sourceSnapshot` in
[files_linux.go](../../../../internal/transport/files_linux.go) requires an
absolute nonsymlink path through protected root-owned ancestors and a regular,
root-owned, single-link file: server mode `0440` with effective service GID;
client mode `0444` with root GID. JSON is canonical ASCII with exact members,
ordered unique keys, no outside whitespace, escapes or trailing newline.

The fixed endpoint is `/opt/swarma/run/dispatcher/dispatcher.sock`.
`runtimeConfig`, `principalPin` and `endpointPin` enforce numeric runtime identity,
groups and directory pins. Root, managed UIDs 30000..59999 and service-as-caller
are excluded. Unmanaged UID non-reuse is a deployment invariant, not a ledger
check. Provisioning and native deployment qualification are separate requirements.

Startup refuses any existing endpoint, including a stale socket. Do not remove
it or retry startup automatically: the contract's operator filesystem procedure
requires separately verified identity, lifetime lock and refused-connect evidence.
It is not a wire operation. See `createEndpointCalls`, `acquireLock` and
`removeOwned` in [endpoint_linux.go](../../../../internal/transport/endpoint_linux.go).

## Inert wire example and neighboring refusal

These are literal payload specimens, not commands to send. Each payload would
need its own four-byte big-endian length prefix. Only after protected peer/pin
checks does the protocol perform this sequential exchange; `r1` is the same
correlation ID throughout:

```text
client: {"request_id":"r1","versions":[1]}
server: {"ok":true,"request_id":"r1","result":{"version":1},"v":1}
client: {"op":"capacity","request_id":"r1"}
server: {"error":{"code":"capacity:hold","message":"admission closed"},"ok":false,"request_id":"r1","v":1}
```

Neighboring refusal: after the same hello, replacing only `capacity` with `start`
produces `{"error":{"code":"request_invalid","message":"unsupported request"},"ok":false,"request_id":"r1","v":1}`
and closes. This recognizes an unsupported operation; it does not implement start.
An extra member or a mismatched request ID instead closes without an operation
result. Do not pipeline, append a second operation or normalize malformed JSON.

The literal constructors/recognizers are `hello`, `helloResult`, `capacity`,
`hold`, `invalid`, `parseHello`, `parseOperation` in
[json.go](../../../../internal/transport/json.go); `serveOperation` and
`capacityExchange` enforce stage order. Existing `TestWireRecognition` in
[json_test.go](../../../../internal/transport/json_test.go) and
`TestProtocolSessions` in [service_linux_test.go](../../../../internal/transport/service_linux_test.go)
contain adjacent controls. Reading those cases does not attest a live exchange.
Socket receives use the descriptor-aware path in
[socket_linux.go](../../../../internal/transport/socket_linux.go), not generic
`frame.Read`; do not substitute an opaque byte codec for ancillary rejection.
