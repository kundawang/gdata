package gdata

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"testing"
)

// mustBuildFixedFrame hand-assembles a GDS1 frame without calling
// marshalSlotFrame, so any accidental change to the on-disk layout is
// caught by the test rather than hidden by shared code.
func mustBuildFixedFrame(t *testing.T, seq uint64, version string, payload []byte) []byte {
	t.Helper()
	buf := make([]byte, 0, slotFrameMinSize+len(version)+len(payload))
	tmp := make([]byte, 8)
	buf = append(buf, slotFrameMagic[:]...)
	binary.BigEndian.PutUint16(tmp[:2], slotFrameVersion)
	buf = append(buf, tmp[:2]...)
	binary.BigEndian.PutUint64(tmp, seq)
	buf = append(buf, tmp...)
	binary.BigEndian.PutUint16(tmp[:2], uint16(len(version)))
	buf = append(buf, tmp[:2]...)
	buf = append(buf, version...)
	sum := sha256.Sum256(payload)
	buf = append(buf, sum[:]...)
	binary.BigEndian.PutUint32(tmp[:4], uint32(len(payload)))
	buf = append(buf, tmp[:4]...)
	buf = append(buf, payload...)
	return buf
}

func mustBuildManifest(t *testing.T, manifest slotManifest) []byte {
	t.Helper()
	data, err := json.MarshalIndent(manifest, "", "\t")
	if err != nil {
		t.Fatal(err)
	}
	return data
}
