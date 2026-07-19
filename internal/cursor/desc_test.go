package cursor

import "testing"

func TestDescriptorParses(t *testing.T) {
	f, err := AgentFile()
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("package=%s messages=%d", f.Package(), f.Messages().Len())
	for _, n := range []string{"AgentClientMessage", "AgentServerMessage", "AgentRunRequest", "UserMessage", "ConversationAction", "ModelDetails", "ConversationStateStructure"} {
		md, err := Message(n)
		if err != nil {
			t.Fatalf("missing %s: %v", n, err)
		}
		t.Logf("%s fields=%d", n, md.Fields().Len())
	}
}
