//go:build linux

package keymaker_test

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/relux-works/swarma-dispatcher/internal/p17/codes"
	"github.com/relux-works/swarma-dispatcher/internal/p17/fakes"
	"github.com/relux-works/swarma-dispatcher/internal/p17/keymaker"
)

// Claim: a fresh connection with a different timeout replays the exact persisted end frame, including its original wire timeout.
func TestPinnedEndExact(t *testing.T) {
	c := vectors(t)
	stored := c.named(t, "end")
	conn, _ := openVectorAt(t, c, c.named(t, "end-success"), stored, 100*time.Millisecond)
	res, sent, err := conn.EndExact(vectorID, keymaker.EndRequest{RunID: vectorRun}, stored)
	if err != nil || !res.Ended || res.RunID != vectorRun || !bytes.Equal(stored, sent) || conn.Requests() != 1 {
		t.Fatalf("%+v %v", res, err)
	}
}

// Claim: wrong-ID, wrong-run, wrong-method, truncated or malformed persisted end intents refuse before any request; the nearby exact replay succeeds.
func TestPinnedEndIntentRefused(t *testing.T) {
	c := vectors(t)
	for _, mode := range []string{"id", "run", "method", "truncated", "malformed"} {
		t.Run(mode, func(t *testing.T) {
			d := &fakes.KeymakerDialer{Script: &fakes.KeymakerScript{}}
			conn, err := keymaker.Open(context.Background(), d, keymaker.Pinned{}, 100*time.Millisecond)
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			body := c.named(t, "end")
			switch mode {
			case "id":
				body = bytes.ReplaceAll(body, []byte(vectorID), []byte(strings.Repeat("2", 32)))
			case "run":
				body = bytes.ReplaceAll(body, []byte(vectorRun), []byte(strings.Repeat("2", 32)))
			case "method":
				body = c.named(t, "admission-bind")
			case "truncated":
				body = body[:len(body)-1]
			case "malformed":
				body = frameBytes(`{"params":`)
			}
			_, _, err = conn.EndExact(vectorID, keymaker.EndRequest{RunID: vectorRun}, body)
			if !codes.Is(err, codes.EndUnknown) || conn.Requests() != 0 || len(d.Script.Ends()) != 0 || len(d.Script.Binds()) != 0 {
				t.Fatalf("intent sent: %v", err)
			}
		})
	}
}
