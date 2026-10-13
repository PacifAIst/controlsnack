// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package protocol

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestNetRequestRoundTripLeavesTheRestOfTheStream(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteNetRequest(&buf, NetRequest{Host: "example.com", Port: 443}); err != nil {
		t.Fatal(err)
	}
	buf.WriteString("GET / HTTP/1.1\r\n")
	req, err := ReadNetRequest(&buf)
	if err != nil || req != (NetRequest{Host: "example.com", Port: 443}) {
		t.Fatalf("%+v %v", req, err)
	}
	if rest, _ := io.ReadAll(&buf); string(rest) != "GET / HTTP/1.1\r\n" {
		t.Errorf("the bytes after the line belong to the connection: %q", rest)
	}
}

func TestNetReplyRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	_ = WriteNetReply(&buf, NetReply{OK: true})
	_ = WriteNetReply(&buf, NetReply{Error: "address 10.0.0.1 is private"})
	_ = WriteNetReply(&buf, NetReply{})
	for i, want := range []NetReply{{OK: true}, {Error: "address 10.0.0.1 is private"}, {Error: "refused"}} {
		got, err := ReadNetReply(&buf)
		if err != nil || got != want {
			t.Errorf("reply %d: %+v %v, want %+v", i, got, err, want)
		}
	}
	if _, err := ReadNetReply(&buf); !errors.Is(err, io.EOF) {
		t.Errorf("end of the stream: %v", err)
	}
}

func TestNetRequestRefusals(t *testing.T) {
	for name, line := range map[string]string{
		"empty line":     "\n",
		"not json":       "hello\n",
		"no host":        `{"port":80}` + "\n",
		"port zero":      `{"host":"a.com","port":0}` + "\n",
		"port too big":   `{"host":"a.com","port":65536}` + "\n",
		"port negative":  `{"host":"a.com","port":-1}` + "\n",
		"unknown field":  `{"host":"a.com","port":80,"extra":1}` + "\n",
		"two values":     `{"host":"a.com","port":80} {"host":"b.com","port":80}` + "\n",
		"space in host":  `{"host":"a .com","port":80}` + "\n",
		"slash in host":  `{"host":"a.com/x","port":80}` + "\n",
		"at in host":     `{"host":"u@a.com","port":80}` + "\n",
		"long host":      `{"host":"` + strings.Repeat("a", 254) + `","port":80}` + "\n",
		"too long line":  `{"host":"a.com","port":80,"pad":"` + strings.Repeat("x", MaxNetLine) + `"}` + "\n",
		"no line ending": `{"host":"a.com","port":80}`,
		"port as string": `{"host":"a.com","port":"80"}` + "\n",
	} {
		if _, err := ReadNetRequest(strings.NewReader(line)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, err := ReadNetRequest(strings.NewReader(`{"host":"` + strings.Repeat("a", 253) + `","port":80}` + "\n")); err != nil {
		t.Errorf("the longest host name: %v", err)
	}
}

func TestWriteNetLineRefusesWhatReadingWould(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteNetReply(&buf, NetReply{Error: strings.Repeat("x", MaxNetLine)}); !errors.Is(err, ErrNetLine) || buf.Len() != 0 {
		t.Errorf("%v (%d bytes written)", err, buf.Len())
	}
}
