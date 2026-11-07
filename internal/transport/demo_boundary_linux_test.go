//go:build linux

package transport

import (
	"testing"
	"time"
)

// These actual production protocol sessions prove capacity remains hold and
// demo.launch is refused. They do not qualify a root-owned CLI deployment.
func TestProductionRefusesDemoLaunch(t *testing.T) {
	for _, op := range []string{"capacity", "demo.launch"} {
		a, b := pair(t)
		done := make(chan error, 1)
		go func() { done <- serveOperation(b, time.Now(), func() error { return nil }); wakeClose(b) }()
		w := wireFor(t, a)
		if err := w.writeFrame(`{"request_id":"r","versions":[1]}`); err != nil {
			t.Fatal(err)
		}
		if reply, err := w.readFrame(); err != nil || string(reply) != `{"ok":true,"request_id":"r","result":{"version":1},"v":1}` {
			t.Fatal(string(reply), err)
		}
		if err := w.writeFrame(`{"op":"` + op + `","request_id":"r"}`); err != nil {
			t.Fatal(err)
		}
		want := `{"error":{"code":"capacity:hold","message":"admission closed"},"ok":false,"request_id":"r","v":1}`
		if op == "demo.launch" {
			want = `{"error":{"code":"request_invalid","message":"unsupported request"},"ok":false,"request_id":"r","v":1}`
		}
		if reply, err := w.readFrame(); err != nil || string(reply) != want {
			t.Fatal(string(reply), err)
		}
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(time.Second):
			t.Fatal("production session blocked")
		}
	}
}
