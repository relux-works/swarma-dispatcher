package m0client

import (
	"strings"
	"testing"
)

const formsID = "0123456789abcdef0123456789abcdef"

// Claim: classifyReply accepts exactly the daemon's result envelope (accepted)
// and refusal envelope (refused) for this correlation ID; another ID, an
// escape, trailing bytes, an oversize body and the hello result are not valid
// replies.
func TestReplyEnvelopeForms(t *testing.T) {
	result := `{"ok":true,"request_id":"` + formsID + `","result":{"effect_id":"e1","state":"started"},"v":1}`
	refusal := `{"error":{"code":"signature_invalid","message":"signature does not verify over the preimage"},"ok":false,"request_id":"` + formsID + `","v":1}`
	if accepted, valid := classifyReply([]byte(result), formsID); !accepted || !valid {
		t.Fatal("control: result envelope not accepted")
	}
	if accepted, valid := classifyReply([]byte(refusal), formsID); accepted || !valid {
		t.Fatal("control: refusal envelope not recognized")
	}
	other := strings.Repeat("f", 32)
	for name, reply := range map[string]string{
		"other id result":  strings.Replace(result, formsID, other, 1),
		"other id refusal": strings.Replace(refusal, formsID, other, 1),
		"escape":           strings.Replace(refusal, "signature does", `signature\u0020does`, 1),
		"trailing":         result + " ",
		"extra member":     strings.Replace(result, `"v":1}`, `"v":1,"x":1}`, 1),
		"hello result":     helloResult(formsID),
		"oversize":         strings.Replace(refusal, "signature does", strings.Repeat("a", MaxReplyBytes), 1),
		"empty":            "",
	} {
		if _, valid := classifyReply([]byte(reply), formsID); valid {
			t.Errorf("%s: accepted as a valid reply", name)
		}
	}
}

// Claim: only the three exact command forms parse; anything else is a usage
// refusal with exit 64 and empty stdout.
func TestCommandForms(t *testing.T) {
	for _, args := range [][]string{
		{"dispatch", "--config", "/c"},
		{"cancel", "--config", "/c", "--dispatch-request-id", formsID},
		{"resend", "--config", "/c", "--request-id", formsID},
	} {
		if _, ok := parseArgs(args); !ok {
			t.Errorf("control: %v refused", args)
		}
	}
	for _, args := range [][]string{
		nil,
		{"dispatch"},
		{"dispatch", "--config", "/c", "extra"},
		{"dispatch", "--key", "/c"},
		{"cancel", "--config", "/c"},
		{"cancel", "--config", "/c", "--request-id", formsID},
		{"resend", "--config", "/c", "--dispatch-request-id", formsID},
		{"status", "--config", "/c"},
	} {
		if _, ok := parseArgs(args); ok {
			t.Errorf("%v parsed", args)
		}
		var stdout, stderr strings.Builder
		if code := Run(args, &stdout, &stderr); code != ExitLocal || stdout.Len() != 0 {
			t.Errorf("%v: exit %d stdout %q", args, code, stdout.String())
		}
	}
}

// Claim: a request ID is exactly 32 lowercase hex characters, and the
// generator spells 16 source bytes that way.
func TestRequestIDForm(t *testing.T) {
	id, err := NewRequestID(strings.NewReader("\x00\x01\x02\x03\x04\x05\x06\x07\x08\x09\xaa\xbb\xcc\xdd\xee\xff"))
	if err != nil || id != "00010203040506070809aabbccddeeff" || !ValidRequestID(id) {
		t.Fatalf("control: %q %v", id, err)
	}
	for _, bad := range []string{"", strings.Repeat("a", 31), strings.Repeat("a", 33), strings.Repeat("A", 32), strings.Repeat("g", 32)} {
		if ValidRequestID(bad) {
			t.Errorf("%q accepted", bad)
		}
	}
	if _, err := NewRequestID(strings.NewReader("short")); err == nil {
		t.Error("short random source accepted")
	}
}
