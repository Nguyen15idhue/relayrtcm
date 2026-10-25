package main

import (
	"bufio"
	"fmt"
	"io"
	"strings"
	"testing"
)

func TestChunkedReader(t *testing.T) {
	chunks := []string{"abc", "defgh", "ijk"}
	var raw strings.Builder
	for _, c := range chunks {
		fmt.Fprintf(&raw, "%x;ext=1\r\n%s\r\n", len(c), c)
	}
	raw.WriteString("0\r\nX-Trailer: ok\r\n\r\n")

	cr := newChunkedReader(bufio.NewReader(strings.NewReader(raw.String())))
	got, err := io.ReadAll(cr)
	if err != nil {
		t.Fatalf("ReadAll error: %v", err)
	}
	want := strings.Join(chunks, "")
	if string(got) != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestChunkedReaderSmallBuffer(t *testing.T) {
	raw := "4\r\nRTCM\r\n3\r\n123\r\n0\r\n\r\n"
	cr := newChunkedReader(bufio.NewReaderSize(strings.NewReader(raw), 2))

	var got []byte
	buf := make([]byte, 2)
	for {
		n, err := cr.Read(buf)
		got = append(got, buf[:n]...)
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("Read error: %v", err)
		}
	}
	if string(got) != "RTCM123" {
		t.Fatalf("got %q, want %q", got, "RTCM123")
	}
}
