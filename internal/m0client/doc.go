// Package m0client is the caller side of the DRAFT M0 request form in
// spec/m0-launch.md section 2: it reads a protected installed dispatch or cancel
// signing plan, signs exactly its request R with the associated local Ed25519
// seed after checking caller identity, stores the wire bytes and performs one
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
