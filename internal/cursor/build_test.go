package cursor

import (
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"
	"testing"
)

func TestBuildRunRequestRoundTrip(t *testing.T) {
	raw, blobs, err := BuildRunRequest("claude-4.5-sonnet", "You are Jurig.", "find login flow", "conv-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) == 0 {
		t.Fatal("empty request")
	}
	if len(blobs) != 1 {
		t.Fatalf("want 1 blob, got %d", len(blobs))
	}

	// parse back
	md, _ := Message("AgentClientMessage")
	m := dynamicpb.NewMessage(md)
	if err := proto.Unmarshal(raw, m); err != nil {
		t.Fatal(err)
	}
	run := m.Get(m.Descriptor().Fields().ByName("run_request")).Message()
	if !run.IsValid() {
		t.Fatal("no run_request")
	}
	model := run.Get(run.Descriptor().Fields().ByName("model_details")).Message()
	gotModel := model.Get(model.Descriptor().Fields().ByName("model_id")).String()
	if gotModel != "claude-4.5-sonnet" {
		t.Fatalf("model=%q", gotModel)
	}
	conv := run.Get(run.Descriptor().Fields().ByName("conversation_id")).String()
	if conv != "conv-1" {
		t.Fatalf("conv=%q", conv)
	}
	// dig user text
	action := run.Get(run.Descriptor().Fields().ByName("action")).Message()
	uma := action.Get(action.Descriptor().Fields().ByName("user_message_action")).Message()
	um := uma.Get(uma.Descriptor().Fields().ByName("user_message")).Message()
	txt := um.Get(um.Descriptor().Fields().ByName("text")).String()
	if txt != "find login flow" {
		t.Fatalf("text=%q", txt)
	}
	// root prompt blob present
	cs := run.Get(run.Descriptor().Fields().ByName("conversation_state")).Message()
	roots := cs.Get(cs.Descriptor().Fields().ByName("root_prompt_messages_json")).List()
	if roots.Len() != 1 {
		t.Fatalf("roots=%d", roots.Len())
	}
	var _ protoreflect.Value
	t.Log("round-trip OK: model, conv, user text, system blob all encode+decode correctly")
}
