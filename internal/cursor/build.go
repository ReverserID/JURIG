package cursor

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"
)

// newMsg builds an empty dynamic message for agent.v1.<name>.
func newMsg(name string) (protoreflect.Message, error) {
	md, err := Message(name)
	if err != nil {
		return nil, err
	}
	return dynamicpb.NewMessage(md), nil
}

func fld(m protoreflect.Message, name string) protoreflect.FieldDescriptor {
	return m.Descriptor().Fields().ByName(protoreflect.Name(name))
}

func setStr(m protoreflect.Message, name, v string) error {
	f := fld(m, name)
	if f == nil {
		return fmt.Errorf("field %q not found on %s", name, m.Descriptor().Name())
	}
	m.Set(f, protoreflect.ValueOfString(v))
	return nil
}

func setMsg(m protoreflect.Message, name string, sub protoreflect.Message) error {
	f := fld(m, name)
	if f == nil {
		return fmt.Errorf("field %q not found on %s", name, m.Descriptor().Name())
	}
	m.Set(f, protoreflect.ValueOfMessage(sub))
	return nil
}

func appendBytes(m protoreflect.Message, name string, v []byte) error {
	f := fld(m, name)
	if f == nil {
		return fmt.Errorf("field %q not found on %s", name, m.Descriptor().Name())
	}
	m.Mutable(f).List().Append(protoreflect.ValueOfBytes(v))
	return nil
}

// BuildRunRequest constructs an AgentClientMessage{run_request} for a single
// user turn, returning the marshaled bytes and the blob store (system prompt
// keyed by hex(sha256), which Cursor requests back via the KV handshake).
func BuildRunRequest(modelID, systemPrompt, userText, conversationID string) ([]byte, map[string][]byte, error) {
	blobStore := map[string][]byte{}

	// system prompt → blob
	sysJSON, _ := json.Marshal(map[string]string{"role": "system", "content": systemPrompt})
	sum := sha256.Sum256(sysJSON)
	blobStore[hex.EncodeToString(sum[:])] = sysJSON

	// conversation_state (fresh: root prompt blob + no turns)
	convState, err := newMsg("ConversationStateStructure")
	if err != nil {
		return nil, nil, err
	}
	if err := appendBytes(convState, "root_prompt_messages_json", sum[:]); err != nil {
		return nil, nil, err
	}

	// user message + action
	userMsg, err := newMsg("UserMessage")
	if err != nil {
		return nil, nil, err
	}
	mid, _ := randomUUID()
	_ = setStr(userMsg, "text", userText)
	_ = setStr(userMsg, "message_id", mid)

	uma, err := newMsg("UserMessageAction")
	if err != nil {
		return nil, nil, err
	}
	if err := setMsg(uma, "user_message", userMsg); err != nil {
		return nil, nil, err
	}
	action, err := newMsg("ConversationAction")
	if err != nil {
		return nil, nil, err
	}
	if err := setMsg(action, "user_message_action", uma); err != nil {
		return nil, nil, err
	}

	// model details
	model, err := newMsg("ModelDetails")
	if err != nil {
		return nil, nil, err
	}
	_ = setStr(model, "model_id", modelID)
	_ = setStr(model, "display_model_id", modelID)
	_ = setStr(model, "display_name", modelID)

	// run request
	run, err := newMsg("AgentRunRequest")
	if err != nil {
		return nil, nil, err
	}
	_ = setMsg(run, "conversation_state", convState)
	_ = setMsg(run, "action", action)
	_ = setMsg(run, "model_details", model)
	_ = setStr(run, "conversation_id", conversationID)

	// client message
	client, err := newMsg("AgentClientMessage")
	if err != nil {
		return nil, nil, err
	}
	if err := setMsg(client, "run_request", run); err != nil {
		return nil, nil, err
	}

	out, err := proto.Marshal(client.Interface())
	if err != nil {
		return nil, nil, err
	}
	return out, blobStore, nil
}
