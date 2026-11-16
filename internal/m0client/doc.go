// Package m0client is the caller side of the DRAFT M0 request form in
// spec/m0-launch.md section 2: it builds one closed dispatch or cancel request
// R from a protected configuration, signs the exact preimage with a local
// Ed25519 seed file, stores the wire bytes before sending and performs one
// client exchange of spec/transport.md.
//
// The canonical bytes, the closed-form validation and the key and signature
// spellings all come from internal/m0, the code the daemon itself uses; this
// package holds no second canonicalizer. It never creates, rotates or repairs
// a key file, never prints or stores the seed and never retries inside one
// invocation. The transport client is Linux only; other platforms refuse.
//
// Nothing here installs, enables or qualifies anything: a request the daemon
// answers is still subject to every gate of the specification.
package m0client
