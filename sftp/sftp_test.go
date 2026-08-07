package taildrive_sftp

import (
	"bytes"
	"testing"
)

func TestBuildInit(t *testing.T) {
	const version uint32 = 3

	var want []byte = []byte{1, 0, 0, 0, byte(version)}

	got := buildInit(version)

	if !bytes.Equal(want, got) {
		t.Errorf("buildInit(%d) = %v,  want %v", version, got, want)
	}
}

func wireString(s string) []byte {
	return marshalString(nil, s)
}

func TestParseVersion_BareNoExtensions(t *testing.T) {
	payload := []byte{sshFxpVersion, 0, 0, 0, 3}

	v, err := parseVersion(payload)
	if err != nil {
		t.Fatalf("parseVersion returned error on valid bare payload: %v", err)
	}
	if v.Version != 3 {
		t.Errorf("Version = %d, want 3", v.Version)
	}
	if len(v.Extensions) != 0 {
		t.Errorf("Extensions = %v, want empty", v.Extensions)
	}
}

func TestParseVersion_OneExtension(t *testing.T) {
	payload := []byte{sshFxpVersion, 0, 0, 0, 3}
	payload = append(payload, wireString("posix-rename@openssh.com")...)
	payload = append(payload, wireString("1")...)

	v, err := parseVersion(payload)
	if err != nil {
		t.Fatalf("parseVersion: %v", err)
	}
	if len(v.Extensions) != 1 {
		t.Fatalf("len(Extensions) = %d, want 1", len(v.Extensions))
	}
	if got := v.Extensions[0].Name; got != "posix-rename@openssh.com" {
		t.Errorf("Name = %q, want %q", got, "posix-rename@openssh.com")
	}
	if got := v.Extensions[0].Data; got != "1" {
		t.Errorf("Data = %q, want %q", got, "1")
	}
}

func TestParseVersion_WrongTypeByte(t *testing.T) {
	// 101 is SSH_FXP_STATUS — a real packet type, just not legal here.
	payload := []byte{101, 0, 0, 0, 3}

	if _, err := parseVersion(payload); err == nil {
		t.Fatal("expected error on wrong type byte, got nil")
	}
}

func TestParseVersion_TruncatedMidPair(t *testing.T) {
	// Name present, data entirely missing.
	payload := []byte{sshFxpVersion, 0, 0, 0, 3}
	payload = append(payload, wireString("fsync@openssh.com")...)

	if _, err := parseVersion(payload); err == nil {
		t.Fatal("expected error on payload truncated mid-pair, got nil")
	} else {
		t.Logf("got expected error: %v", err)
	}
}

func TestSFTPHandshake(t *testing.T) {
	/*const version uint32 = 10

	want := versionPacket{Version: version}

	writebuf := bytes.Buffer{}
	readbuf := bytes.Buffer{}

	readbuf.Write([]byte{1, 0, 0, 0, byte(version)})

	got, err := handshake(&writebuf, &readbuf)
	if err != nil {
		t.Errorf("handshake([], [1, 0, 0, 0, %d]) = %v, %v", version, *got, err)
	}

	if got.Version != want.Version {
		t.Errorf("handshake([], [1, 0, 0, 0, %d]) = %v, %v, want %v, nil", version, got, err, want)
	} */

}
