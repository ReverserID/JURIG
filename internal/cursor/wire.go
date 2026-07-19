package cursor

import "encoding/binary"

// Connect protocol framing: each message is [1 byte flags][4 byte BE length][payload].
// flags bit 0x02 marks the end-of-stream trailer (a JSON blob, not a protobuf message).

const endStreamFlag = 0x02

// frame wraps a payload in a Connect data frame (flags=0).
func frame(payload []byte) []byte {
	buf := make([]byte, 5+len(payload))
	buf[0] = 0
	binary.BigEndian.PutUint32(buf[1:5], uint32(len(payload)))
	copy(buf[5:], payload)
	return buf
}

// deframe reads one Connect frame from buf. Returns the flags, payload, the
// remaining bytes, and ok=false if buf doesn't yet hold a full frame.
func deframe(buf []byte) (flags byte, payload, rest []byte, ok bool) {
	if len(buf) < 5 {
		return 0, nil, buf, false
	}
	n := binary.BigEndian.Uint32(buf[1:5])
	if uint32(len(buf)-5) < n {
		return 0, nil, buf, false
	}
	return buf[0], buf[5 : 5+n], buf[5+n:], true
}

// isEndStream reports whether a frame's flags mark the end-of-stream trailer.
func isEndStream(flags byte) bool { return flags&endStreamFlag != 0 }
