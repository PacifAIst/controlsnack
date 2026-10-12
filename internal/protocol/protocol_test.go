// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package protocol

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net"
	"reflect"
	"strings"
	"sync"
	"testing"
)

var (
	sum64   = strings.Repeat("ab", 32)
	nonce32 = strings.Repeat("0f", 16)
)

// --- bootstrap -----------------------------------------------------------------------------------------------

func TestPushRoundTrip(t *testing.T) {
	p := Push{Arch: ArchAarch64, Size: 1573026, SHA256: sum64, Nonce: nonce32}
	line, err := p.Line()
	if err != nil {
		t.Fatal(err)
	}
	want := "CSBOOT/1 PUSH arch=aarch64 size=1573026 sha256=" + sum64 + " nonce=" + nonce32 + "\n"
	if line != want {
		t.Fatalf("line %q\nwant %q", line, want)
	}
	if len(line) > MaxBootLine {
		t.Fatalf("a push line is %d bytes, over the %d limit", len(line), MaxBootLine)
	}
	for _, in := range []string{line, strings.TrimSuffix(line, "\n")} {
		got, err := ParsePush(in)
		if err != nil || got != p {
			t.Errorf("ParsePush(%q) = %+v, %v", in, got, err)
		}
	}
}

func TestPushValidation(t *testing.T) {
	good := Push{Arch: ArchX86_64, Size: 100, SHA256: sum64, Nonce: nonce32}
	mutations := map[string]func(*Push){
		"unknown arch":  func(p *Push) { p.Arch = "riscv64" },
		"empty arch":    func(p *Push) { p.Arch = "" },
		"zero size":     func(p *Push) { p.Size = 0 },
		"negative size": func(p *Push) { p.Size = -1 },
		"huge size":     func(p *Push) { p.Size = MaxAgentSize + 1 },
		"short hash":    func(p *Push) { p.SHA256 = sum64[:63] },
		"upper hash":    func(p *Push) { p.SHA256 = strings.ToUpper(sum64) },
		"non-hex hash":  func(p *Push) { p.SHA256 = strings.Repeat("zz", 32) },
		"short nonce":   func(p *Push) { p.Nonce = "abcd" },
		"long nonce":    func(p *Push) { p.Nonce = strings.Repeat("a", 65) },
		"upper nonce":   func(p *Push) { p.Nonce = strings.ToUpper(nonce32) },
	}
	if err := good.Validate(); err != nil {
		t.Fatal(err)
	}
	for name, mut := range mutations {
		p := good
		mut(&p)
		if _, err := p.Line(); err == nil {
			t.Errorf("%s: Line must refuse it", name)
		}
	}
	edge := good
	edge.Size = MaxAgentSize
	if err := edge.Validate(); err != nil {
		t.Errorf("the maximum size is allowed: %v", err)
	}
}

func TestParsePushRejectsMalformedLines(t *testing.T) {
	ok := "CSBOOT/1 PUSH arch=x86_64 size=10 sha256=" + sum64 + " nonce=" + nonce32
	bad := map[string]string{
		"empty":            "",
		"wrong magic":      strings.Replace(ok, "CSBOOT/1", "CSBOOT/2", 1),
		"lower magic":      strings.Replace(ok, "CSBOOT/1", "csboot/1", 1),
		"wrong verb":       strings.Replace(ok, "PUSH", "PULL", 1),
		"missing field":    "CSBOOT/1 PUSH arch=x86_64 size=10 sha256=" + sum64,
		"extra field":      ok + " extra=1",
		"duplicate field":  "CSBOOT/1 PUSH arch=x86_64 arch=x86_64 sha256=" + sum64 + " nonce=" + nonce32,
		"unknown field":    "CSBOOT/1 PUSH arch=x86_64 size=10 sha256=" + sum64 + " mode=" + nonce32,
		"no equals":        strings.Replace(ok, "size=10", "size10", 1),
		"empty value":      strings.Replace(ok, "size=10", "size=", 1),
		"size not number":  strings.Replace(ok, "size=10", "size=ten", 1),
		"size with plus":   strings.Replace(ok, "size=10", "size=+10", 1),
		"size hex":         strings.Replace(ok, "size=10", "size=0x10", 1),
		"double space":     strings.Replace(ok, "PUSH arch", "PUSH  arch", 1),
		"tab separator":    strings.Replace(ok, "PUSH arch", "PUSH\tarch", 1),
		"embedded newline": ok + "\nCSBOOT/1 OK",
		"too long":         ok + strings.Repeat(" ", MaxBootLine),
	}
	for name, line := range bad {
		if p, err := ParsePush(line); err == nil {
			t.Errorf("%s: accepted %+v", name, p)
		}
	}
	if _, err := ParsePush(ok + strings.Repeat("x", MaxBootLine)); !errors.Is(err, ErrBootLineTooLong) {
		t.Errorf("an overlong line is reported as such: %v", err)
	}
	if _, err := ParsePush("garbage"); !errors.Is(err, ErrBadBootLine) {
		t.Errorf("%v", err)
	}
}

func TestReplyRoundTrip(t *testing.T) {
	ok := Reply{OK: true, SHA256: sum64}
	if got, err := ParseReply(ok.Line()); err != nil || got != ok {
		t.Errorf("%+v %v", got, err)
	}
	if ok.Line() != "CSBOOT/1 OK sha256="+sum64+"\n" {
		t.Errorf("%q", ok.Line())
	}
	errReply := Reply{Reason: "sha256 mismatch"}
	got, err := ParseReply(errReply.Line())
	if err != nil || got.OK || got.Reason != "sha256 mismatch" {
		t.Errorf("%+v %v", got, err)
	}
	// Reasons are made printable and short.
	nasty := Reply{Reason: "bad\x00\x1b[31m\nnewsé" + strings.Repeat("x", 1000)}.Line()
	if strings.Count(nasty, "\n") != 1 || len(nasty) > MaxBootLine {
		t.Errorf("a reason must stay on one short line: %d bytes, %d newlines", len(nasty), strings.Count(nasty, "\n"))
	}
	for _, r := range nasty {
		if r != '\n' && (r < 0x20 || r > 0x7e) {
			t.Errorf("non-printable %U in %q", r, nasty)
		}
	}
	if blank := (Reply{}).Line(); blank != "CSBOOT/1 ERR unspecified\n" {
		t.Errorf("%q", blank)
	}
	for _, bad := range []string{"", "OK", "CSBOOT/1", "CSBOOT/1 MAYBE", "CSBOOT/1 OK", "CSBOOT/1 OK sha256=xyz", "CSBOOT/1 OK sha256=" + sum64[:60], "CSBOOT/1OK sha256=" + sum64} {
		if _, err := ParseReply(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	if _, err := ParseReply("CSBOOT/1 ERR " + strings.Repeat("x", MaxBootLine)); !errors.Is(err, ErrBootLineTooLong) {
		t.Errorf("%v", err)
	}
}

func TestReadBootLine(t *testing.T) {
	br := bufio.NewReader(strings.NewReader("CSBOOT/1 OK sha256=" + sum64 + "\nTHE-REST"))
	line, err := ReadBootLine(br)
	if err != nil || !strings.HasSuffix(line, "\n") {
		t.Fatalf("%q %v", line, err)
	}
	rest, _ := io.ReadAll(br)
	if string(rest) != "THE-REST" {
		t.Errorf("bytes after the newline must stay in the reader, got %q", rest)
	}
	// An unterminated flood is cut off at the limit without reading it all.
	flood := &countingReader{r: strings.NewReader(strings.Repeat("A", 10<<20))}
	if _, err := ReadBootLine(bufio.NewReaderSize(flood, 16)); !errors.Is(err, ErrBootLineTooLong) {
		t.Errorf("flood: %v", err)
	}
	if flood.n > 4*MaxBootLine {
		t.Errorf("read %d bytes of an endless line; it must stop near %d", flood.n, MaxBootLine)
	}
	if _, err := ReadBootLine(bufio.NewReader(strings.NewReader("no newline"))); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Errorf("%v", err)
	}
	if _, err := ReadBootLine(bufio.NewReader(strings.NewReader(""))); !errors.Is(err, io.EOF) {
		t.Errorf("%v", err)
	}
	exact := strings.Repeat("a", MaxBootLine-1) + "\n"
	if _, err := ReadBootLine(bufio.NewReader(strings.NewReader(exact))); err != nil {
		t.Errorf("a line of exactly the limit is fine: %v", err)
	}
	if _, err := ReadBootLine(bufio.NewReader(strings.NewReader(strings.Repeat("a", MaxBootLine) + "\n"))); !errors.Is(err, ErrBootLineTooLong) {
		t.Errorf("one byte over: %v", err)
	}
}

type countingReader struct {
	r io.Reader
	n int
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += n
	return n, err
}

// --- frames --------------------------------------------------------------------------------------------------

func TestFrameRoundTrip(t *testing.T) {
	blob := bytes.Repeat([]byte{0, 1, 2, 255}, 1000)
	f, err := NewFrame(42, "screenshot.res", ScreenshotRes{W: 1280, H: 800, Format: "webp", Hash: "h1", Cursor: Point{640, 400}, Scale: 1}, blob)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := WriteFrame(&buf, f); err != nil {
		t.Fatal(err)
	}
	// Wire layout: 4-byte big-endian header length, the JSON header, then the blob.
	raw := buf.Bytes()
	hlen := binary.BigEndian.Uint32(raw[:4])
	if int(hlen) != len(raw)-4-len(blob) {
		t.Errorf("prefix says %d header bytes, frame has %d", hlen, len(raw)-4-len(blob))
	}
	var h map[string]any
	if err := json.Unmarshal(raw[4:4+hlen], &h); err != nil || h["v"] != 1.0 || h["id"] != 42.0 || h["type"] != "screenshot.res" || h["blob_len"] != 4000.0 {
		t.Errorf("header %s (%v)", raw[4:4+hlen], err)
	}
	got, err := ReadFrame(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if got.Header.ID != 42 || got.Header.Type != "screenshot.res" || !bytes.Equal(got.Blob, blob) {
		t.Errorf("%+v", got.Header)
	}
	var res ScreenshotRes
	if err := DecodeBody(got, &res); err != nil || res.W != 1280 || res.Cursor.Y != 400 || res.Hash != "h1" {
		t.Errorf("%+v %v", res, err)
	}
}

func TestFramesBackToBack(t *testing.T) {
	var buf bytes.Buffer
	for i := uint64(1); i <= 5; i++ {
		f, _ := NewFrame(i, "ping", nil, bytes.Repeat([]byte{byte(i)}, int(i)*10))
		if err := WriteFrame(&buf, f); err != nil {
			t.Fatal(err)
		}
	}
	for i := uint64(1); i <= 5; i++ {
		f, err := ReadFrame(&buf)
		if err != nil || f.Header.ID != i || len(f.Blob) != int(i)*10 {
			t.Fatalf("frame %d: %+v %v", i, f.Header, err)
		}
	}
	if _, err := ReadFrame(&buf); !errors.Is(err, io.EOF) {
		t.Errorf("a clean end of stream is io.EOF, got %v", err)
	}
}

func rawFrame(header string, blob []byte) []byte {
	b := make([]byte, 4)
	binary.BigEndian.PutUint32(b, uint32(len(header)))
	return append(append(b, header...), blob...)
}

func TestReadFrameRejectsViolations(t *testing.T) {
	huge := make([]byte, 4)
	binary.BigEndian.PutUint32(huge, MaxHeaderBytes+1)
	zero := make([]byte, 4)
	tests := []struct {
		name string
		data []byte
		want error
	}{
		{"oversize header", huge, ErrFrameTooLarge},
		{"empty header", zero, ErrBadFrame},
		{"not json", rawFrame(`not json`, nil), ErrBadFrame},
		{"json array", rawFrame(`[1,2]`, nil), ErrBadFrame},
		{"unknown header field", rawFrame(`{"v":1,"id":1,"type":"ping","extra":true}`, nil), ErrBadFrame},
		{"trailing data in header", rawFrame(`{"v":1,"id":1,"type":"ping"}{"v":1}`, nil), ErrBadFrame},
		{"wrong version", rawFrame(`{"v":2,"id":1,"type":"ping"}`, nil), ErrBadFrame},
		{"missing version", rawFrame(`{"id":1,"type":"ping"}`, nil), ErrBadFrame},
		{"missing type", rawFrame(`{"v":1,"id":1}`, nil), ErrBadFrame},
		{"bad type characters", rawFrame(`{"v":1,"id":1,"type":"Ping!"}`, nil), ErrBadFrame},
		{"type with a path", rawFrame(`{"v":1,"id":1,"type":"../x"}`, nil), ErrBadFrame},
		{"type too long", rawFrame(`{"v":1,"id":1,"type":"`+strings.Repeat("a", 65)+`"}`, nil), ErrBadFrame},
		{"negative blob length", rawFrame(`{"v":1,"id":1,"type":"ping","blob_len":-1}`, nil), ErrFrameTooLarge},
		{"oversize blob", rawFrame(`{"v":1,"id":1,"type":"ping","blob_len":8388609}`, nil), ErrFrameTooLarge},
		{"enormous blob", rawFrame(`{"v":1,"id":1,"type":"ping","blob_len":2147483648}`, nil), ErrFrameTooLarge},
		{"id is a string", rawFrame(`{"v":1,"id":"1","type":"ping"}`, nil), ErrBadFrame},
		{"id negative", rawFrame(`{"v":1,"id":-1,"type":"ping"}`, nil), ErrBadFrame},
	}
	for _, tt := range tests {
		_, err := ReadFrame(bytes.NewReader(tt.data))
		if !errors.Is(err, tt.want) {
			t.Errorf("%s: got %v, want %v", tt.name, err, tt.want)
		}
		if !IsViolation(err) {
			t.Errorf("%s: must count as a violation", tt.name)
		}
	}
}

func TestReadFrameTruncation(t *testing.T) {
	full := rawFrame(`{"v":1,"id":1,"type":"ping","blob_len":10}`, []byte("0123456789"))
	for cut := 1; cut < len(full); cut++ {
		_, err := ReadFrame(bytes.NewReader(full[:cut]))
		if err == nil {
			t.Fatalf("a frame cut at %d of %d bytes was accepted", cut, len(full))
		}
		if cut >= 4 && !errors.Is(err, io.ErrUnexpectedEOF) && !IsViolation(err) {
			t.Errorf("cut at %d: %v", cut, err)
		}
	}
	if _, err := ReadFrame(bytes.NewReader(nil)); !errors.Is(err, io.EOF) {
		t.Errorf("empty stream: %v", err)
	}
	if IsViolation(io.ErrUnexpectedEOF) || IsViolation(io.EOF) || IsViolation(nil) {
		t.Error("a stream ending is not a protocol violation")
	}
}

func TestReadFrameNeverAllocatesWhatThePeerAnnounces(t *testing.T) {
	// A header that promises a maximum blob but delivers nothing must fail without trouble.
	data := rawFrame(`{"v":1,"id":1,"type":"ping","blob_len":8388608}`, nil)
	if _, err := ReadFrame(bytes.NewReader(data)); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Errorf("%v", err)
	}
	// The limits themselves are accepted.
	max := rawFrame(`{"v":1,"id":1,"type":"ping","blob_len":8388608}`, make([]byte, MaxBlobBytes))
	if f, err := ReadFrame(bytes.NewReader(max)); err != nil || len(f.Blob) != MaxBlobBytes {
		t.Errorf("a blob of exactly the limit is fine: %v", err)
	}
}

func TestWriteFrameValidates(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteFrame(&buf, Frame{Header: Header{V: 1, ID: 1, Type: "BAD TYPE"}}); !errors.Is(err, ErrBadFrame) {
		t.Errorf("%v", err)
	}
	if err := WriteFrame(&buf, Frame{Header: Header{ID: 1, Type: "ping"}, Blob: make([]byte, MaxBlobBytes+1)}); !errors.Is(err, ErrFrameTooLarge) {
		t.Errorf("%v", err)
	}
	// V defaults to the current version, and BlobLen follows the blob.
	buf.Reset()
	if err := WriteFrame(&buf, Frame{Header: Header{ID: 7, Type: "ping"}, Blob: []byte("xyz")}); err != nil {
		t.Fatal(err)
	}
	f, err := ReadFrame(&buf)
	if err != nil || f.Header.BlobLen != 3 || f.Header.V != Version {
		t.Errorf("%+v %v", f.Header, err)
	}
	// An oversize header is refused before anything is written.
	buf.Reset()
	big := Frame{Header: Header{V: 1, ID: 1, Type: "ping", Body: json.RawMessage(`"` + strings.Repeat("a", MaxHeaderBytes) + `"`)}}
	if err := WriteFrame(&buf, big); !errors.Is(err, ErrFrameTooLarge) || buf.Len() != 0 {
		t.Errorf("%v, %d bytes written", err, buf.Len())
	}
	if _, err := NewFrame(1, "x", make(chan int), nil); err == nil {
		t.Error("an unencodable body is an error")
	}
}

func TestDecodeBodyIsStrict(t *testing.T) {
	f := Frame{Header: Header{Type: "exec", Body: json.RawMessage(`{"command":"ls","cwd":"/tmp"}`)}}
	var req ExecReq
	if err := DecodeBody(f, &req); err != nil || req.Command != "ls" || req.Cwd != "/tmp" {
		t.Errorf("%+v %v", req, err)
	}
	for _, body := range []string{`{"command":"ls","sudo":true}`, `{"command":5}`, `[1]`, `{"command":"ls"} {"x":1}`, `"str"`} {
		if err := DecodeBody(Frame{Header: Header{Type: "exec", Body: json.RawMessage(body)}}, &ExecReq{}); !errors.Is(err, ErrBadFrame) {
			t.Errorf("%s: %v", body, err)
		}
	}
	var empty ExecReq
	if err := DecodeBody(Frame{Header: Header{Type: "ping"}}, &empty); err != nil {
		t.Errorf("an absent body is an empty object: %v", err)
	}
}

// --- messages and handshake -----------------------------------------------------------------------------------

func TestMessageTypeRegistry(t *testing.T) {
	for _, req := range []string{"ping", "screenshot", "screen_info", "windows", "input", "a11y_tree", "a11y_action", "ocr", "exec", "browser", "wait_for", "file_peek"} {
		if !IsRequest(req) || !Known(req) || !Known(ResponseOf(req)) || IsRequest(ResponseOf(req)) {
			t.Errorf("%s: request/response registration", req)
		}
	}
	for _, t1 := range []string{"hello", "hello_ack", "pong", "cancel", "error", "event"} {
		if !Known(t1) || IsRequest(t1) {
			t.Errorf("%s", t1)
		}
	}
	for _, unknown := range []string{"", "reboot", "exec.req", "screenshot.res.res", "shell", "Exec"} {
		if Known(unknown) {
			t.Errorf("%q must be unknown (the agent answers unknown types with an error)", unknown)
		}
	}
	// Every constant is itself a valid type name for the frame validator.
	for _, c := range []string{TypeHello, TypeHelloAck, TypePing, TypePong, TypeCancel, TypeError, TypeEvent, TypeScreenshot, TypeScreenInfo,
		TypeWindows, TypeInput, TypeA11yTree, TypeA11yAction, TypeOCR, TypeExec, TypeBrowser, TypeWaitFor, TypeFilePeek} {
		if !reType.MatchString(c) || !reType.MatchString(ResponseOf(c)) {
			t.Errorf("%s is not a valid frame type", c)
		}
	}
}

func goodAck() (HelloAck, Expect) {
	ack := HelloAck{Proto: "1.0", AgentVersion: "v0.1.0", AgentSHA256: sum64, Nonce: nonce32, Arch: ArchX86_64, User: "cs",
		Display: Display{1280, 800}, Capabilities: []string{CapScreenshot, CapInput, CapExec, CapWindows}}
	exp := Expect{Nonce: nonce32, AgentSHA256: sum64, Arch: ArchX86_64, User: "cs", Required: []string{CapScreenshot, CapExec}}
	return ack, exp
}

func TestHandshakeValidation(t *testing.T) {
	ack, exp := goodAck()
	if err := ack.Validate(exp); err != nil {
		t.Fatal(err)
	}
	tests := map[string]func(*HelloAck, *Expect){
		"different major":      func(a *HelloAck, _ *Expect) { a.Proto = "2.0" },
		"newer minor is fine?": nil,
		"no minor":             func(a *HelloAck, _ *Expect) { a.Proto = "1" },
		"garbage proto":        func(a *HelloAck, _ *Expect) { a.Proto = "one.zero" },
		"negative major":       func(a *HelloAck, _ *Expect) { a.Proto = "-1.0" },
		"wrong nonce":          func(a *HelloAck, _ *Expect) { a.Nonce = strings.Repeat("11", 16) },
		"empty nonce":          func(a *HelloAck, _ *Expect) { a.Nonce = "" },
		"different binary":     func(a *HelloAck, _ *Expect) { a.AgentSHA256 = strings.Repeat("cd", 32) },
		"wrong arch":           func(a *HelloAck, _ *Expect) { a.Arch = ArchAarch64 },
		"runs as root":         func(a *HelloAck, _ *Expect) { a.User = "root" },
		"tiny display":         func(a *HelloAck, _ *Expect) { a.Display = Display{10, 10} },
		"zero display":         func(a *HelloAck, _ *Expect) { a.Display = Display{} },
		"absurd display":       func(a *HelloAck, _ *Expect) { a.Display = Display{100000, 100000} },
		"missing capability":   func(a *HelloAck, _ *Expect) { a.Capabilities = []string{CapScreenshot} },
		"no capabilities":      func(a *HelloAck, _ *Expect) { a.Capabilities = nil },
		"empty version":        func(a *HelloAck, _ *Expect) { a.AgentVersion = "" },
		"huge version":         func(a *HelloAck, _ *Expect) { a.AgentVersion = strings.Repeat("v", 200) },
	}
	for name, mut := range tests {
		if mut == nil {
			continue
		}
		a, e := goodAck()
		mut(&a, &e)
		err := a.Validate(e)
		if err == nil || !errors.Is(err, ErrHandshake) {
			t.Errorf("%s: got %v", name, err)
		}
	}
	// A newer minor version of the same major is accepted: minors only add.
	a, e := goodAck()
	a.Proto = "1.7"
	if err := a.Validate(e); err != nil {
		t.Errorf("1.7 against 1.0: %v", err)
	}
	// Expectations that are left empty are not checked (arch and user are optional).
	a, e = goodAck()
	a.Arch, a.User = "whatever", "whoever"
	e.Arch, e.User = "", ""
	if err := a.Validate(e); err != nil {
		t.Errorf("unset expectations: %v", err)
	}
	if n, err := ProtoMajor("12.3"); err != nil || n != 12 {
		t.Errorf("%d %v", n, err)
	}
}

func TestHelloJSONShape(t *testing.T) {
	data, _ := json.Marshal(Hello{Proto: ProtoVersion, HostVersion: "v1", SessionID: "s_1", Nonce: nonce32, Features: Features{Net: true}})
	var m map[string]any
	_ = json.Unmarshal(data, &m)
	for _, k := range []string{"proto", "host_version", "session_id", "nonce", "features"} {
		if _, ok := m[k]; !ok {
			t.Errorf("hello lacks %q: %s", k, data)
		}
	}
	data, _ = json.Marshal(HelloAck{})
	_ = json.Unmarshal(data, &m)
	for _, k := range []string{"proto", "agent_version", "agent_sha256", "nonce", "arch", "user", "display", "capabilities"} {
		if _, ok := m[k]; !ok {
			t.Errorf("hello_ack lacks %q", k)
		}
	}
}

// --- conn ----------------------------------------------------------------------------------------------------

type pipeEnds struct {
	a, b net.Conn
	ca   *Conn
	cb   *Conn
}

func newPipe(t *testing.T) *pipeEnds {
	t.Helper()
	a, b := net.Pipe()
	t.Cleanup(func() { _ = a.Close(); _ = b.Close() })
	return &pipeEnds{a: a, b: b,
		ca: NewConn(bufio.NewReader(a), a, a), cb: NewConn(bufio.NewReader(b), b, b)}
}

func TestConnSendRecv(t *testing.T) {
	p := newPipe(t)
	go func() {
		f, _ := NewFrame(9, "ping", nil, []byte("hello"))
		if err := p.ca.Send(f); err != nil {
			t.Error(err)
		}
	}()
	f, err := p.cb.Recv()
	if err != nil || f.Header.ID != 9 || string(f.Blob) != "hello" {
		t.Fatalf("%+v %v", f, err)
	}
	if p.ca.Bad() || p.cb.Bad() {
		t.Error("healthy connections are not bad")
	}
}

func TestConnConcurrentSendsDoNotInterleave(t *testing.T) {
	p := newPipe(t)
	const senders, each = 8, 25
	var wg sync.WaitGroup
	for s := 0; s < senders; s++ {
		wg.Add(1)
		go func(s int) {
			defer wg.Done()
			for i := 0; i < each; i++ {
				f, _ := NewFrame(uint64(s*1000+i+1), "ping", map[string]int{"s": s}, bytes.Repeat([]byte{byte(s)}, 500))
				if err := p.ca.Send(f); err != nil {
					t.Error(err)
					return
				}
			}
		}(s)
	}
	seen := map[uint64]bool{}
	for i := 0; i < senders*each; i++ {
		f, err := p.cb.Recv()
		if err != nil {
			t.Fatalf("frame %d: %v", i, err)
		}
		want := byte((f.Header.ID - 1) / 1000)
		if len(f.Blob) != 500 || f.Blob[0] != want || f.Blob[499] != want {
			t.Fatalf("frame %d carries a torn blob", f.Header.ID)
		}
		seen[f.Header.ID] = true
	}
	wg.Wait()
	if len(seen) != senders*each {
		t.Errorf("got %d distinct frames, want %d", len(seen), senders*each)
	}
}

func TestConnClosesOnViolation(t *testing.T) {
	p := newPipe(t)
	go func() {
		_, _ = p.a.Write(rawFrame(`{"v":1,"id":1,"type":"ping","blob_len":99999999}`, nil))
	}()
	_, err := p.cb.Recv()
	if !errors.Is(err, ErrFrameTooLarge) {
		t.Fatalf("%v", err)
	}
	if !p.cb.Bad() {
		t.Fatal("a protocol violation must mark the connection bad")
	}
	f, _ := NewFrame(1, "ping", nil, nil)
	if err := p.cb.Send(f); err == nil {
		t.Error("a bad connection refuses to send")
	}
	// The peer sees the reset.
	if _, err := p.ca.Recv(); err == nil {
		t.Error("the other side must notice the closed stream")
	}
}

func TestConnKeepsBytesBufferedBehindTheBootstrap(t *testing.T) {
	// The guest answers the push and starts talking at once: the first frame arrives in the same read as the OK line.
	var wire bytes.Buffer
	wire.WriteString(Reply{OK: true, SHA256: sum64}.Line())
	f, _ := NewFrame(1, "hello_ack", HelloAck{Proto: "1.0"}, nil)
	if err := WriteFrame(&wire, f); err != nil {
		t.Fatal(err)
	}
	br := bufio.NewReader(&wire)
	line, err := ReadBootLine(br)
	if err != nil {
		t.Fatal(err)
	}
	if r, err := ParseReply(line); err != nil || !r.OK {
		t.Fatal(err)
	}
	c := NewConn(br, io.Discard, nil)
	got, err := c.Recv()
	if err != nil || got.Header.Type != "hello_ack" {
		t.Fatalf("%+v %v", got.Header, err)
	}
	if err := c.Close(); err != nil {
		t.Errorf("closing a Conn without a closer: %v", err)
	}
}

func TestFramesSurviveJSONRoundTripOfEveryMessage(t *testing.T) {
	// Every body type must encode and strictly decode back to itself.
	region := Rect{1, 2, 3, 4}
	bodies := []any{
		ScreenshotReq{Format: "webp", Quality: 70, MaxLongEdge: 1280, IfChangedSince: "h", MarksGeneration: "g1"},
		ScreenshotRes{W: 1, H: 2, Format: "png", Hash: "x", Cursor: Point{3, 4}, Scale: 0.5, Unchanged: true},
		ScreenInfoRes{Monitors: []Monitor{{ID: 0, Bounds: Rect{0, 0, 1280, 800}, Primary: true}}, Cursor: Point{1, 1}, ActiveWindow: &Window{ID: "0x1", Title: "t", Bounds: Rect{0, 0, 10, 10}, Active: true}},
		WindowsReq{Op: "focus", WindowID: "0x1"}, WindowsRes{Windows: []Window{{ID: "0x2", Title: "x"}}},
		InputReq{Events: []InputEvent{{Type: "click", X: 1, Y: 2, Button: "right", Double: true}, {Type: "scroll", X: 5, Y: 6, DY: -3}, {Type: "key", Keys: "ctrl+l", Repeat: 2}, {Type: "text", Text: "héllo"}, {Type: "down", DragTo: &Point{9, 9}}}},
		InputRes{ScreenHashAfter: "h"},
		A11yTreeReq{Scope: "desktop", Query: "ok", MaxNodes: 10, MaxDepth: 5},
		A11yTreeRes{Generation: "g12", Nodes: []A11yNode{{ID: "a1", Role: "push button", Name: "OK", States: []string{"showing"}, Bounds: Rect{1, 2, 3, 4}, Actions: []string{"click"}}}, Truncated: true},
		A11yActionReq{ID: "a1", Generation: "g12", Op: "set_text", Text: "x"},
		OCRReq{Region: &region, Lang: "eng"}, OCRRes{Generation: "g3", Lines: []OCRLine{{ID: "o1", Text: "Hi", Bounds: region, Conf: 91}}},
		ExecReq{Command: "ls", Cwd: "/tmp", TimeoutMS: 1000, Stdin: "in", MaxOutput: 4096},
		ExecRes{ExitCode: 1, DurationMS: 5, Stdout: "o", Stderr: "e", Truncated: true, TimedOut: true},
		BrowserReq{Op: "type", URL: "https://x", Target: "w3", Text: "t", Submit: true, Tab: 2},
		BrowserRes{Title: "t", URL: "u", Text: "x", Tabs: []BrowserTab{{Index: 0, Title: "a", URL: "b", Active: true}}, Generation: "g", Nodes: []A11yNode{{ID: "w1", Role: "link"}}},
		WaitForReq{Condition: "screen_idle", Value: "1000", TimeoutMS: 5000}, WaitForRes{Met: true, WaitedMS: 12},
		FilePeekReq{Path: "/home/cs/x.sh", MaxBytes: 65536}, FilePeekRes{Exists: true, Size: 99},
		ErrorBody{Code: CodeInvalidArgs, Message: "m"}, CancelBody{ID: 5}, EventBody{Name: "n", Data: "d"},
		Hello{Proto: ProtoVersion, Nonce: nonce32}, HelloAck{Proto: ProtoVersion, Capabilities: []string{"a"}},
	}
	for _, body := range bodies {
		f, err := NewFrame(1, "ping", body, nil)
		if err != nil {
			t.Fatalf("%T: %v", body, err)
		}
		dst := reflect.New(reflect.TypeOf(body))
		if err := DecodeBody(f, dst.Interface()); err != nil {
			t.Errorf("%T: %v", body, err)
			continue
		}
		if !reflect.DeepEqual(dst.Elem().Interface(), body) {
			t.Errorf("%T did not survive: %+v vs %+v", body, dst.Elem().Interface(), body)
		}
	}
}

// --- fuzzing --------------------------------------------------------------------------------------------------

func FuzzReadFrame(f *testing.F) {
	good, _ := NewFrame(3, "exec", ExecReq{Command: "ls"}, []byte("blob"))
	var buf bytes.Buffer
	_ = WriteFrame(&buf, good)
	f.Add(buf.Bytes())
	f.Add([]byte{0, 0, 0, 0})
	f.Add([]byte{0xff, 0xff, 0xff, 0xff})
	f.Add(rawFrame(`{"v":1,"id":1,"type":"ping","blob_len":8388609}`, nil))
	f.Add(rawFrame(`{"v":1,"id":1,"type":"ping","blob_len":-5}`, nil))
	f.Add(rawFrame(`{"v":1,"id":18446744073709551615,"type":"a.b"}`, nil))
	f.Fuzz(func(t *testing.T, data []byte) {
		fr, err := ReadFrame(bytes.NewReader(data))
		if err != nil {
			return
		}
		// Whatever was accepted is consistent and survives a rewrite.
		if err := fr.Validate(); err != nil {
			t.Fatalf("accepted a frame that does not validate: %v", err)
		}
		if len(fr.Blob) > MaxBlobBytes {
			t.Fatal("blob over the limit")
		}
		var out bytes.Buffer
		if err := WriteFrame(&out, fr); err != nil {
			t.Fatalf("an accepted frame cannot be written back: %v", err)
		}
		back, err := ReadFrame(&out)
		if err != nil || back.Header.ID != fr.Header.ID || back.Header.Type != fr.Header.Type || !bytes.Equal(back.Blob, fr.Blob) {
			t.Fatalf("rewrite changed the frame: %v", err)
		}
	})
}

func FuzzParseBootstrap(f *testing.F) {
	push, _ := Push{Arch: ArchX86_64, Size: 10, SHA256: sum64, Nonce: nonce32}.Line()
	f.Add(push)
	f.Add(Reply{OK: true, SHA256: sum64}.Line())
	f.Add(Reply{Reason: "bad"}.Line())
	f.Add("CSBOOT/1 PUSH arch=x86_64 size=-1")
	f.Add(strings.Repeat("A", 600))
	f.Fuzz(func(t *testing.T, line string) {
		if p, err := ParsePush(line); err == nil {
			// An accepted push re-renders to an equivalent line that parses to the same value.
			again, err := p.Line()
			if err != nil {
				t.Fatalf("accepted push does not render: %v", err)
			}
			if q, err := ParsePush(again); err != nil || q != p {
				t.Fatalf("round trip: %+v vs %+v (%v)", q, p, err)
			}
			if len(again) > MaxBootLine {
				t.Fatal("rendered push exceeds the line limit")
			}
		}
		if r, err := ParseReply(line); err == nil && r.OK {
			if !reHex256.MatchString(r.SHA256) {
				t.Fatalf("OK reply with a bad hash: %+v", r)
			}
		}
	})
}
