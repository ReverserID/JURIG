package agent

// EventKind classifies agent events streamed to the UI.
type EventKind string

const (
	EvText       EventKind = "text"        // assistant prose
	EvToolCall   EventKind = "tool_call"   // model requested a tool
	EvToolResult EventKind = "tool_result" // tool output
	EvCmd        EventKind = "cmd"         // subprocess command line
	EvStatus     EventKind = "status"      // step counter / provider info
	EvUsage      EventKind = "usage"       // token usage for a model call
	EvError      EventKind = "error"
	EvDone       EventKind = "done" // final answer emitted, run finished
)

// Event is a single streamed update from the running agent.
type Event struct {
	Kind    EventKind
	Text    string
	Tool    string
	Step    int
	In, Out int // token usage (EvUsage)
}

// Sink receives events. Nil is safe (dropped).
type Sink func(Event)

func (s Sink) emit(e Event) {
	if s != nil {
		s(e)
	}
}
