package m0client

import (
	"crypto/rand"
	"fmt"
	"io"

	"github.com/relux-works/swarma-dispatcher/internal/m0"
)

// Exit statuses of the caller tool.
const (
	// ExitAccepted: the daemon answered ok:true.
	ExitAccepted = 0
	// ExitRefused: the daemon answered a refusal envelope (ok:false).
	ExitRefused = 16
	// ExitUnknown: bytes were sent and no complete valid reply was read. The
	// outcome is unknown; the stored request can be sent again with resend.
	ExitUnknown = 17
	// ExitLocal: refused locally before any byte was sent.
	ExitLocal = 64
)

const usage = `usage: swarma-m0-caller dispatch --config /absolute/caller.json
       swarma-m0-caller cancel --config /absolute/caller.json --dispatch-request-id ID
       swarma-m0-caller resend --config /absolute/caller.json --request-id ID`

const (
	verbDispatch = "dispatch"
	verbCancel   = "cancel"
	verbResend   = "resend"
)

type command struct {
	verb   string
	config string
	id     string
}

func parseArgs(args []string) (command, bool) {
	switch {
	case len(args) == 3 && args[0] == verbDispatch && args[1] == "--config":
		return command{verb: verbDispatch, config: args[2]}, true
	case len(args) == 5 && args[0] == verbCancel && args[1] == "--config" && args[3] == "--dispatch-request-id":
		return command{verb: verbCancel, config: args[2], id: args[4]}, true
	case len(args) == 5 && args[0] == verbResend && args[1] == "--config" && args[3] == "--request-id":
		return command{verb: verbResend, config: args[2], id: args[4]}, true
	}
	return command{}, false
}

// Outcome is the result of one invocation. Reply is set only for
// ExitAccepted and ExitRefused. Stored reports that the wire bytes of
// RequestID are in the state directory. Reason is a short fixed word and
// Detail an operating-system or decoder error; neither ever carries key
// material or request bytes.
type Outcome struct {
	Exit      int
	Reply     []byte
	RequestID string
	Stored    bool
	Sent      int
	Reason    string
	Detail    error
}

func local(reason string, detail error) Outcome {
	return Outcome{Exit: ExitLocal, Reason: reason, Detail: detail}
}

// classify maps one completed transport attempt to an outcome: nothing sent
// is a local refusal; a valid envelope is accepted or refused; everything
// else after the first sent byte is unknown.
func classify(id string, stored bool, sent int, reply []byte, stage string, err error) Outcome {
	o := Outcome{RequestID: id, Stored: stored, Sent: sent, Reason: stage, Detail: err}
	switch {
	case err != nil && sent == 0:
		o.Exit = ExitLocal
	case err != nil:
		o.Exit = ExitUnknown
	default:
		accepted, valid := classifyReply(reply, id)
		switch {
		case !valid:
			o.Exit, o.Reason = ExitUnknown, "reply_invalid"
		case accepted:
			o.Exit, o.Reply, o.Reason = ExitAccepted, reply, ""
		default:
			o.Exit, o.Reply, o.Reason = ExitRefused, reply, ""
		}
	}
	return o
}

// Run executes one invocation with the kernel CSPRNG and returns the exit
// status. Stdout receives exactly the reply envelope bytes and nothing else;
// diagnostics go to stderr.
func Run(args []string, stdout, stderr io.Writer) int {
	return runWith(args, stdout, stderr, rand.Reader)
}

func runWith(args []string, stdout, stderr io.Writer, random io.Reader) int {
	cmd, ok := parseArgs(args)
	if !ok {
		fmt.Fprintln(stderr, usage)
		return ExitLocal
	}
	var o Outcome
	switch cmd.verb {
	case verbDispatch:
		o = submit(cmd.config, m0.OpDispatch, "", random)
	case verbCancel:
		o = submit(cmd.config, m0.OpCancel, cmd.id, random)
	default:
		o = resend(cmd.config, cmd.id)
	}
	return report(o, stdout, stderr)
}

func report(o Outcome, stdout, stderr io.Writer) int {
	detail := ""
	if o.Detail != nil {
		detail = ": " + o.Detail.Error()
	}
	switch o.Exit {
	case ExitAccepted, ExitRefused:
		if _, err := stdout.Write(o.Reply); err != nil {
			fmt.Fprintln(stderr, "swarma-m0-caller: reply received but not written to stdout:", err)
			fmt.Fprintln(stderr, "request_id="+o.RequestID)
			return ExitUnknown
		}
		if o.Exit == ExitRefused {
			fmt.Fprintln(stderr, "swarma-m0-caller: refused by the dispatcher")
		}
	case ExitUnknown:
		fmt.Fprintln(stderr, "swarma-m0-caller: outcome unknown: "+o.Reason+detail)
		fmt.Fprintln(stderr, "request_id="+o.RequestID)
	default:
		fmt.Fprintln(stderr, "swarma-m0-caller: refused locally, nothing sent: "+o.Reason+detail)
		if o.Stored {
			fmt.Fprintln(stderr, "request_id="+o.RequestID+" stored, not sent")
		}
	}
	return o.Exit
}
