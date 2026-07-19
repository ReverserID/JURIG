package cursor

import (
	"context"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"
)

const (
	agentURL      = "https://api2.cursor.sh/agent.v1.AgentService/Run"
	clientVersion = "cli-2026.01.09-231024f"
)

// Client talks the native Cursor Agent protocol over HTTP/2 Connect streaming.
type Client struct {
	Token string
	HTTP  *http.Client
}

// NewClient builds a client with an access token.
func NewClient(token string) *Client {
	return &Client{Token: token, HTTP: &http.Client{Timeout: 5 * time.Minute}}
}

// Chat runs a single text turn and returns the assistant's text. This is the
// minimal path (no Jurig tools yet): it answers the KV blob + request-context
// handshakes and collects text deltas. EXPERIMENTAL — verify against a live
// Cursor account.
func (c *Client) Chat(ctx context.Context, model, system, user string) (string, error) {
	convID, _ := randomUUID()
	reqBytes, blobStore, err := BuildRunRequest(model, system, user, convID)
	if err != nil {
		return "", err
	}

	pr, pw := io.Pipe()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, agentURL, pr)
	if err != nil {
		return "", err
	}
	req.Header.Set("content-type", "application/connect+proto")
	req.Header.Set("connect-protocol-version", "1")
	req.Header.Set("authorization", "Bearer "+c.Token)
	req.Header.Set("x-ghost-mode", "true")
	req.Header.Set("x-cursor-client-version", clientVersion)
	req.Header.Set("x-cursor-client-type", "cli")
	if id, e := randomUUID(); e == nil {
		req.Header.Set("x-request-id", id)
	}
	req.Header.Set("te", "trailers")

	// write the initial run request frame
	go func() {
		_, _ = pw.Write(frame(reqBytes))
	}()

	resp, err := c.HTTP.Do(req)
	if err != nil {
		pw.Close()
		return "", fmt.Errorf("cursor request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		pw.Close()
		return "", fmt.Errorf("cursor http %d: %s", resp.StatusCode, string(body))
	}

	var sb strings.Builder
	sendFrame := func(b []byte) { _, _ = pw.Write(frame(b)) }
	err = c.readStream(resp.Body, blobStore, sendFrame, func(text string) { sb.WriteString(text) })
	pw.Close()
	if err != nil {
		return sb.String(), err
	}
	return sb.String(), nil
}

// readStream deframes the response, dispatching server messages until the
// end-of-stream frame.
func (c *Client) readStream(r io.Reader, blobStore map[string][]byte, sendFrame func([]byte), onText func(string)) error {
	serverMD, err := Message("AgentServerMessage")
	if err != nil {
		return err
	}
	buf := make([]byte, 0, 64*1024)
	tmp := make([]byte, 32*1024)
	for {
		n, rerr := r.Read(tmp)
		if n > 0 {
			buf = append(buf, tmp[:n]...)
			for {
				flags, payload, rest, ok := deframe(buf)
				if !ok {
					break
				}
				buf = rest
				if isEndStream(flags) {
					return endStreamErr(payload)
				}
				sm := dynamicpb.NewMessage(serverMD)
				if proto.Unmarshal(payload, sm) == nil {
					c.dispatch(sm, blobStore, sendFrame, onText)
				}
			}
		}
		if rerr == io.EOF {
			return nil
		}
		if rerr != nil {
			return rerr
		}
	}
}

// dispatch handles one AgentServerMessage.
func (c *Client) dispatch(sm protoreflect.Message, blobStore map[string][]byte, sendFrame func([]byte), onText func(string)) {
	set := whichOneof(sm, "message")
	if set == nil {
		return
	}
	switch set.Name() {
	case "interaction_update":
		iu := sm.Get(set).Message()
		if f := whichOneof(iu, ""); f != nil && f.Name() == "text_delta" {
			td := iu.Get(f).Message()
			onText(getStr(td, "text"))
		}
	case "kv_server_message":
		c.answerKV(sm.Get(set).Message(), blobStore, sendFrame)
	case "exec_server_message":
		c.answerExec(sm.Get(set).Message(), sendFrame)
	}
}

// answerKV responds to a get_blob request with the stored blob.
func (c *Client) answerKV(kv protoreflect.Message, blobStore map[string][]byte, sendFrame func([]byte)) {
	id := getU32(kv, "id")
	set := whichOneof(kv, "")
	if set == nil || set.Name() != "get_blob_args" {
		return
	}
	blobID := getBytes(kv.Get(set).Message(), "blob_id")
	data := blobStore[hex.EncodeToString(blobID)]

	res, _ := newMsg("GetBlobResult")
	if data != nil {
		res.Set(fld(res, "blob_data"), protoreflect.ValueOfBytes(data))
	}
	kvc, _ := newMsg("KvClientMessage")
	kvc.Set(fld(kvc, "id"), protoreflect.ValueOfUint32(id))
	_ = setMsg(kvc, "get_blob_result", res)
	cm, _ := newMsg("AgentClientMessage")
	_ = setMsg(cm, "kv_client_message", kvc)
	if b, err := proto.Marshal(cm.Interface()); err == nil {
		sendFrame(b)
	}
}

// answerExec responds to request_context with an empty context (text-only mode).
func (c *Client) answerExec(ex protoreflect.Message, sendFrame func([]byte)) {
	id := getU32(ex, "id")
	execID := getStr(ex, "exec_id")
	set := whichOneof(ex, "")
	if set == nil || set.Name() != "request_context_args" {
		return // other exec (native tool) attempts ignored in text-only mode
	}
	rc, _ := newMsg("RequestContext")
	succ, _ := newMsg("RequestContextSuccess")
	_ = setMsg(succ, "request_context", rc)
	rcr, _ := newMsg("RequestContextResult")
	_ = setMsg(rcr, "success", succ)

	ecm, _ := newMsg("ExecClientMessage")
	ecm.Set(fld(ecm, "id"), protoreflect.ValueOfUint32(id))
	_ = setStr(ecm, "exec_id", execID)
	_ = setMsg(ecm, "request_context_result", rcr)
	cm, _ := newMsg("AgentClientMessage")
	_ = setMsg(cm, "exec_client_message", ecm)
	if b, err := proto.Marshal(cm.Interface()); err == nil {
		sendFrame(b)
	}
}

// ---- reflection helpers ----

func whichOneof(m protoreflect.Message, oneof string) protoreflect.FieldDescriptor {
	ods := m.Descriptor().Oneofs()
	for i := 0; i < ods.Len(); i++ {
		od := ods.Get(i)
		if oneof != "" && string(od.Name()) != oneof {
			continue
		}
		if od.IsSynthetic() {
			continue
		}
		if f := m.WhichOneof(od); f != nil {
			return f
		}
	}
	return nil
}

func getStr(m protoreflect.Message, name string) string {
	f := fld(m, name)
	if f == nil {
		return ""
	}
	return m.Get(f).String()
}
func getBytes(m protoreflect.Message, name string) []byte {
	f := fld(m, name)
	if f == nil {
		return nil
	}
	return m.Get(f).Bytes()
}
func getU32(m protoreflect.Message, name string) uint32 {
	f := fld(m, name)
	if f == nil {
		return 0
	}
	return uint32(m.Get(f).Uint())
}

// endStreamErr parses the Connect end-stream JSON trailer for an error.
func endStreamErr(payload []byte) error {
	s := strings.TrimSpace(string(payload))
	if s == "" || s == "{}" {
		return nil
	}
	if strings.Contains(s, "\"error\"") {
		return fmt.Errorf("cursor stream error: %s", s)
	}
	return nil
}
