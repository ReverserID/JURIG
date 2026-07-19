package cursor

import "testing"

func TestFrameRoundTrip(t *testing.T) {
	payload := []byte("hello-cursor-protobuf")
	f := frame(payload)
	if f[0] != 0 {
		t.Fatal("flag should be 0")
	}
	// append a partial second frame + trailing
	buf := append(append([]byte{}, f...), frame([]byte("second"))...)
	flags, p, rest, ok := deframe(buf)
	if !ok || flags != 0 || string(p) != "hello-cursor-protobuf" {
		t.Fatalf("frame1: %q ok=%v", p, ok)
	}
	_, p2, rest2, ok2 := deframe(rest)
	if !ok2 || string(p2) != "second" || len(rest2) != 0 {
		t.Fatalf("frame2: %q", p2)
	}
	// incomplete
	if _, _, _, ok3 := deframe(f[:3]); ok3 {
		t.Fatal("should not parse partial")
	}
}
