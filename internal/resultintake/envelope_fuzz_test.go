package resultintake

import (
	"bytes"
	"testing"
)

func FuzzDecodeEnvelope(f *testing.F) {
	f.Add([]byte{})
	f.Add([]byte(`{}`))
	f.Add([]byte(`{"schema":"syncgate.result-envelope.v1","result_id":"result:one"}`))

	f.Fuzz(func(t *testing.T, raw []byte) {
		if len(raw) == 0 {
			contract, _ := intakeContract(t)
			_, raw, _ = BuildEnvelope(intakeEnvelope(contract, "result:fuzz"))
		}
		decoded, canonical, err := DecodeEnvelope(raw)
		if err != nil {
			return
		}
		redecoded, recanonical, err := DecodeEnvelope(canonical)
		if err != nil {
			t.Fatalf("canonical envelope did not decode: %v", err)
		}
		if decoded.Digest != redecoded.Digest || !bytes.Equal(canonical, recanonical) {
			t.Fatal("envelope decoding was not canonical")
		}
	})
}
