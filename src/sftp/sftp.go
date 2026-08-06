package sftp

import (
	"encoding/binary"
	"fmt"
	"io"
)

const sshFxpInit byte = 1
const sshFxpVersion byte = 2

type extension struct {
	Name string
	Data string
}

type versionPacket struct {
	Version    uint32
	Extensions []extension
}

func marshalUint32(b []byte, v uint32) []byte {
	return append(b, byte(v>>24), byte(v>>16), byte(v>>8), byte(v))
}

func marshalUint64(b []byte, v uint64) []byte {
	b = marshalUint32(b, uint32(v>>32))
	return marshalUint32(b, uint32(v))
}

func marshalString(b []byte, s string) []byte {
	b = marshalUint32(b, uint32(len(s)))
	return append(b, s...)
}

func unmarshalUint32(b []byte) (uint32, []byte, error) {
	if len(b) < 4 {
		return 0, nil, fmt.Errorf("short buffer for uint32: have %d bytes", len(b))
	}

	return binary.BigEndian.Uint32(b), b[4:], nil
}

func unmarshalString(b []byte) (string, []byte, error) {
	l, b, err := unmarshalUint32(b)
	if err != nil {
		return "", nil, err
	}

	if uint32(len(b)) < l {
		return "", nil, fmt.Errorf("short buffer for string: need %d have %d", l, len(b))
	}

	return string(b[:l]), b[l:], nil
}

func writePacket(w io.Writer, payload []byte) error {
	var lenBuf [4]byte

	binary.BigEndian.PutUint32(lenBuf[:], uint32(len(payload)))

	if _, err := w.Write(lenBuf[:]); err != nil {
		return err
	}

	_, err := w.Write(payload)
	return err
}

func readPacket(r io.Reader) ([]byte, error) {
	var lenBuf [4]byte

	if _, err := io.ReadFull(r, lenBuf[:]); err != nil {
		return nil, err
	}

	n := binary.BigEndian.Uint32(lenBuf[:])
	payload := make([]byte, n)

	if _, err := io.ReadFull(r, payload); err != nil {
		return nil, err
	}

	return payload, nil
}

func buildInit(version uint32) []byte {
	b := make([]byte, 0, 5)
	b = append(b, sshFxpInit)
	b = marshalUint32(b, version)
	return b
}

func parseVersion(payload []byte) (*versionPacket, error) {
	rest := payload[1:] // skip type byte

	version, rest, err := unmarshalUint32(rest)
	if err != nil {
		return nil, fmt.Errorf("reading version: %w", err)
	}

	v := &versionPacket{Version: version}
	for len(rest) > 0 {
		var name, data string

		name, rest, err = unmarshalString(rest)
		if err != nil {
			return nil, fmt.Errorf("reading extension name: %w", err)
		}

		data, rest, err = unmarshalString(rest)
		if err != nil {
			return nil, fmt.Errorf("reading extension data: %w", err)
		}

		v.Extensions = append(v.Extensions, extension{name, data})
	}

	return v, nil
}

func handshake(w io.Writer, r io.Reader) (*versionPacket, error) {
	if err := writePacket(w, buildInit(3)); err != nil {
		return nil, fmt.Errorf("sending init: %w", err)
	}

	payload, err := readPacket(r)
	if err != nil {
		return nil, fmt.Errorf("reading version reply: %w", err)
	}

	return parseVersion(payload)
}
