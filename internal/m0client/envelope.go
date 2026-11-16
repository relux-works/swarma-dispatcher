package m0client

import "strings"

// MaxReplyBytes is the largest reply payload the daemon's 512-byte outbound
// buffer can carry.
const MaxReplyBytes = 508

type cursor struct {
	b string
	i int
}

func (c *cursor) lit(s string) bool {
	if strings.HasPrefix(c.b[c.i:], s) {
		c.i += len(s)
		return true
	}
	return false
}

// str consumes one string body up to its closing quote. Only the wire subset's
// repertoire is accepted: 0x20..0x7e without quote or backslash.
func (c *cursor) str() bool {
	for c.i < len(c.b) {
		ch := c.b[c.i]
		if ch == '"' {
			c.i++
			return true
		}
		if ch < 0x20 || ch > 0x7e || ch == '\\' {
			return false
		}
		c.i++
	}
	return false
}

// helloFrame and helloResult are the two negotiation templates of the
// transport contract.
func helloFrame(id string) string { return `{"request_id":"` + id + `","versions":[1]}` }
func helloResult(id string) string {
	return `{"ok":true,"request_id":"` + id + `","result":{"version":1},"v":1}`
}

// classifyReply recognizes exactly the two reply envelopes the daemon writes
// for a request with this correlation ID: the result envelope (accepted true)
// and the refusal envelope (accepted false). Anything else, including another
// correlation ID, is not a valid reply.
func classifyReply(reply []byte, id string) (accepted, valid bool) {
	if len(reply) == 0 || len(reply) > MaxReplyBytes {
		return false, false
	}
	c := &cursor{b: string(reply)}
	if c.lit(`{"ok":true,"request_id":"`+id+`","result":{"effect_id":"`) && c.str() && c.lit(`,"state":"`) && c.str() && c.lit(`},"v":1}`) && c.i == len(c.b) {
		return true, true
	}
	c = &cursor{b: string(reply)}
	if c.lit(`{"error":{"code":"`) && c.str() && c.lit(`,"message":"`) && c.str() && c.lit(`},"ok":false,"request_id":"`+id+`","v":1}`) && c.i == len(c.b) {
		return false, true
	}
	return false, false
}
