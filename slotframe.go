package gdata

import (
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
)

// slotFrameMagic identifies a gdata save slot frame.
// It is stored at the very beginning of every slot file/value.
var slotFrameMagic = [4]byte{'G', 'D', 'S', '1'}

// slotFrameVersion is the only frame format version understood by this package.
// A frame with any other version is rejected and treated as corrupted data,
// which makes the loader roll back to an older complete slot.
const slotFrameVersion uint16 = 1

const (
	slotFrameHeaderSize = 4 + 2 + 8 + 2 // magic, frame version, seq, version string length
	slotChecksumSize    = 32
	slotDataLenSize     = 4
	slotFrameMinSize    = slotFrameHeaderSize + slotChecksumSize + slotDataLenSize
)

// marshalSlotFrame builds a self-validating slot frame:
//
//	magic(4) | frameVer(2) | seq(8) | verLen(2) | version | sha256(payload)(32) | dataLen(4) | payload
//
// Every length is encoded as big-endian. The checksum only covers the
// payload, while the fixed layout and the length prefixes detect truncated
// frames and framing damage.
func marshalSlotFrame(seq uint64, version string, payload []byte) ([]byte, error) {
	if len(version) > 65535 {
		return nil, errors.New("slot version string is too long")
	}
	totalSize := slotFrameMinSize + len(version) + len(payload)
	buf := make([]byte, 0, totalSize)
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

	return buf, nil
}

// unmarshalSlotFrame validates a frame and returns its sequence number,
// version string and payload. Any structural damage or a checksum mismatch
// produces an error wrapping ErrCorrupted.
func unmarshalSlotFrame(data []byte) (seq uint64, version string, payload []byte, err error) {
	if len(data) < slotFrameMinSize {
		return 0, "", nil, fmt.Errorf("%w: slot frame is truncated (%d bytes)", ErrCorrupted, len(data))
	}

	pos := 0
	if string(data[:4]) != string(slotFrameMagic[:]) {
		return 0, "", nil, fmt.Errorf("%w: bad slot frame magic", ErrCorrupted)
	}
	pos = 4

	if got := binary.BigEndian.Uint16(data[pos:]); got != slotFrameVersion {
		return 0, "", nil, fmt.Errorf("%w: unsupported slot frame version %d", ErrCorrupted, got)
	}
	pos += 2

	seq = binary.BigEndian.Uint64(data[pos:])
	pos += 8

	versionLen := int(binary.BigEndian.Uint16(data[pos:]))
	pos += 2

	if len(data) < pos+versionLen+slotChecksumSize+slotDataLenSize {
		return 0, "", nil, fmt.Errorf("%w: slot frame header is truncated", ErrCorrupted)
	}
	version = string(data[pos : pos+versionLen])
	pos += versionLen

	storedSum := data[pos : pos+slotChecksumSize]
	pos += slotChecksumSize

	payloadLen := int(binary.BigEndian.Uint32(data[pos:]))
	pos += slotDataLenSize

	if len(data) != pos+payloadLen {
		return 0, "", nil, fmt.Errorf("%w: slot frame length mismatch (header says %d payload bytes, got %d)",
			ErrCorrupted, payloadLen, len(data)-pos)
	}
	payload = data[pos:]

	sum := sha256.Sum256(payload)
	for i := range sum {
		if sum[i] != storedSum[i] {
			return 0, "", nil, fmt.Errorf("%w: slot frame checksum mismatch", ErrCorrupted)
		}
	}

	return seq, version, payload, nil
}
