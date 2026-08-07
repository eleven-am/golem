package codec

import "testing"

// FuzzP7EventCodecRejectsMalformedAndOversizedInput is the evidence fuzz gate,
// not an alias for the unit fuzz target. It varies both hostile bytes and the
// configured public bound, and asserts the one result that is independent of
// schema resolution: an input larger than the accepted bound is always
// rejected. All other malformed inputs must remain panic-free.
func FuzzP7EventCodecRejectsMalformedAndOversizedInput(f *testing.F) {
	f.Add([]byte{}, uint32(1))
	f.Add([]byte("GOLEMEVENT"), uint32(8))
	f.Add(append([]byte("GOLEMEVENT"), make([]byte, 256)...), uint32(16))
	f.Add([]byte{0xff, 0xff, 0xff, 0xff}, uint32(HardMaxEncodedBytes))
	f.Fuzz(func(t *testing.T, input []byte, rawLimit uint32) {
		limit := int(rawLimit%uint32(HardMaxEncodedBytes)) + 1
		_, err := Decode(input, testResolver{}, Limits{MaxEncodedBytes: limit})
		if len(input) > limit && err == nil {
			t.Fatalf("oversized input bytes=%d limit=%d was accepted", len(input), limit)
		}
	})
}
