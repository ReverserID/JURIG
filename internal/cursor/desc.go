package cursor

import (
	_ "embed"
	"fmt"
	"sync"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"

	// register well-known types agent.proto may import
	_ "google.golang.org/protobuf/types/known/anypb"
	_ "google.golang.org/protobuf/types/known/emptypb"
	_ "google.golang.org/protobuf/types/known/structpb"
	_ "google.golang.org/protobuf/types/known/timestamppb"
)

// agentDesc is the compiled FileDescriptorProto for agent.proto (package
// agent.v1), extracted from Cursor's CLI protobuf schema.
//
//go:embed agent.pb
var agentDesc []byte

var (
	fileOnce sync.Once
	fileDesc protoreflect.FileDescriptor
	fileErr  error
)

// AgentFile returns the parsed agent.v1 file descriptor (cached).
func AgentFile() (protoreflect.FileDescriptor, error) {
	fileOnce.Do(func() {
		var fdp descriptorpb.FileDescriptorProto
		if err := proto.Unmarshal(agentDesc, &fdp); err != nil {
			fileErr = fmt.Errorf("unmarshal descriptor: %w", err)
			return
		}
		fileDesc, fileErr = protodesc.NewFile(&fdp, protoregistry.GlobalFiles)
	})
	return fileDesc, fileErr
}

// Message returns the MessageDescriptor for a short message name (e.g.
// "AgentClientMessage") in package agent.v1.
func Message(name string) (protoreflect.MessageDescriptor, error) {
	f, err := AgentFile()
	if err != nil {
		return nil, err
	}
	md := f.Messages().ByName(protoreflect.Name(name))
	if md == nil {
		return nil, fmt.Errorf("message agent.v1.%s not found", name)
	}
	return md, nil
}
