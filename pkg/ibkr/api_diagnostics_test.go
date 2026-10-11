package ibkr

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"strings"
	"testing"
	"time"
)

func diagnosticWireTest(t *testing.T, version int, response func(net.Conn)) APIConfigDiagnostic {
	t.Helper()
	client, server := net.Pipe()
	defer client.Close()
	_ = client.SetDeadline(time.Now().Add(2 * time.Second))
	_ = server.SetDeadline(time.Now().Add(2 * time.Second))
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer server.Close()
		magic := make([]byte, 4)
		if _, err := io.ReadFull(server, magic); err != nil || string(magic) != "API\x00" {
			t.Errorf("handshake magic: %q %v", magic, err)
			return
		}
		descriptor, err := readDiagnosticFrame(server)
		if err != nil || string(descriptor) != "v100..219\x00" {
			t.Errorf("descriptor: %q %v", descriptor, err)
			return
		}
		body := fmt.Appendf(nil, "%d\x00synthetic-time\x00", version)
		if err = writeDiagnosticBytes(server, append(binary.BigEndian.AppendUint32(nil, uint32(len(body))), body...)); err != nil {
			t.Error(err)
			return
		}
		if version != 219 {
			b := make([]byte, 1)
			if _, err = server.Read(b); err != io.EOF {
				t.Errorf("unsupported protocol sent additional bytes: %v", err)
			}
			return
		}
		start, err := readDiagnosticFrame(server)
		if err != nil || len(start) < 4 || binary.BigEndian.Uint32(start[:4]) != 271 {
			t.Errorf("unexpected StartAPI: %v", err)
			return
		}
		var clientID uint64
		if err = forEachProtoField(start[4:], func(f, w int, v []byte) error { var e error; clientID, e = protoVarintValue(f, w, v); return e }); err != nil || clientID < 1_000_000_000 {
			t.Errorf("unsafe diagnostic client ID %d %v", clientID, err)
			return
		}
		// Unsolicited account data is never retained or sent onward.
		if err = writeDiagnosticProto(server, 215, []byte("private-account-sentinel")); err != nil {
			t.Error(err)
			return
		}
		if err = writeDiagnosticProto(server, 209, protoAppendInt32(nil, 1, 400)); err != nil {
			t.Error(err)
			return
		}
		request, err := readDiagnosticFrame(server)
		if err != nil || len(request) != 6 || binary.BigEndian.Uint32(request[:4]) != 308 || request[4] != 8 || request[5] != 1 {
			t.Errorf("only ReqConfig is permitted: %x %v", request, err)
			return
		}
		response(server)
		b := make([]byte, 1)
		if _, err = server.Read(b); err != io.EOF {
			t.Errorf("diagnostic sent extra request: %v", err)
		}
	}()
	result := probeAPIConfig(client)
	_ = client.Close()
	<-done
	return result
}

func configReply(settings []byte) []byte {
	return protoAppendMessage(protoAppendInt32(nil, 1, 1), 4, protoAppendMessage(nil, 2, settings))
}

func TestAPIConfigReadOnlyWireAndCapabilityBoundary(t *testing.T) {
	if TradingProtocolMax != 203 {
		t.Fatal("main trading protocol must remain at 203")
	}
	for _, version := range []int{176, 203, 218, 220} {
		t.Run(fmt.Sprint(version), func(t *testing.T) {
			got := diagnosticWireTest(t, version, nil)
			if version <= 218 && got.State != "unsupported" {
				t.Fatalf("%+v", got)
			}
			if version == 220 && got.Reason != "invalid_protocol" {
				t.Fatalf("%+v", got)
			}
			if got.Settings != nil {
				t.Fatal("unsupported protocol invented settings")
			}
		})
	}
	got := diagnosticWireTest(t, 219, func(c net.Conn) {
		settings := protoAppendBool(nil, 1, false)
		settings = protoAppendBool(settings, 31, true)
		settings = protoAppendBool(settings, 24, false)
		settings = protoAppendInt32(settings, 8, 7496)
		settings = protoAppendString(settings, 36, "private-ip-sentinel")
		if err := writeDiagnosticProto(c, 310, configReply(settings)); err != nil {
			t.Error(err)
		}
	})
	if got.State != "observed" || got.Settings == nil {
		t.Fatalf("%+v", got)
	}
	s := got.Settings
	if s.ReadOnly == nil || *s.ReadOnly || s.RetainOrdersOnReconnect == nil || !*s.RetainOrdersOnReconnect || s.VolumeInLots == nil || *s.VolumeInLots || s.SocketPort == nil || *s.SocketPort != 7496 || s.DownloadOpenOrders != nil {
		t.Fatalf("lost presence or value: %+v", s)
	}
	if strings.Contains(fmt.Sprintf("%+v", got), "sentinel") {
		t.Fatal("retained private response")
	}
}

func TestAPIConfigRejectsMalformedAndUncorrelatedEvidence(t *testing.T) {
	for name, body := range map[string][]byte{
		"wrong request":     protoAppendMessage(protoAppendInt32(nil, 1, 2), 4, protoAppendMessage(nil, 2, nil)),
		"missing settings":  protoAppendInt32(nil, 1, 1),
		"invalid boolean":   configReply(protoAppendInt32(nil, 31, 2)),
		"duplicate setting": configReply(protoAppendBool(protoAppendBool(nil, 31, true), 31, false)),
		"wrong wire":        configReply(protoAppendString(nil, 24, "false")),
		"duplicate config":  protoAppendMessage(configReply(nil), 4, protoAppendMessage(nil, 2, nil)),
		"invalid port":      configReply(protoAppendInt32(nil, 8, 65536)),
		"truncated":         {0x22, 0xff},
	} {
		t.Run(name, func(t *testing.T) {
			got := diagnosticWireTest(t, 219, func(c net.Conn) {
				if err := writeDiagnosticProto(c, 310, body); err != nil {
					t.Error(err)
				}
			})
			if got.State != "unavailable" || got.Settings != nil || got.Reason != "malformed_config" {
				t.Fatalf("%+v", got)
			}
		})
	}
	got := diagnosticWireTest(t, 219, func(c net.Conn) {
		errBody := protoAppendInt32(protoAppendInt32(nil, 1, 1), 3, 321)
		errBody = protoAppendString(errBody, 4, "private-error-sentinel")
		if err := writeDiagnosticProto(c, 204, errBody); err != nil {
			t.Error(err)
		}
	})
	if got.State != "rejected" || got.Settings != nil || got.Reason != "broker_rejected_config" {
		t.Fatalf("%+v", got)
	}
}

func TestDiagnosticFrameBoundAndNoPartialWriteRetry(t *testing.T) {
	if _, err := readDiagnosticFrame(strings.NewReader(string(binary.BigEndian.AppendUint32(nil, 1<<20+1)))); err == nil {
		t.Fatal("accepted oversized frame")
	}
	writer := &diagnosticPartialWriter{}
	if err := writeDiagnosticProto(writer, 308, []byte{8, 1}); err != io.ErrShortWrite || writer.calls != 1 {
		t.Fatalf("%v calls=%d", err, writer.calls)
	}
}

type diagnosticPartialWriter struct{ calls int }

func (w *diagnosticPartialWriter) Write(b []byte) (int, error) { w.calls++; return len(b) - 1, nil }

func TestAPIConfigCancellationClosesOnlyTheDiagnosticSocket(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	accepted := make(chan struct{})
	closed := make(chan struct{})
	go func() {
		defer close(closed)
		socket, err := listener.Accept()
		if err != nil {
			return
		}
		defer socket.Close()
		_ = socket.SetDeadline(time.Now().Add(2 * time.Second))
		close(accepted)
		_, _ = io.Copy(io.Discard, socket)
	}()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan APIConfigDiagnostic, 1)
	go func() {
		done <- readAPIConfig(ctx, &ConnectionConfig{Host: "127.0.0.1", Port: listener.Addr().(*net.TCPAddr).Port, ConnectTimeout: time.Second}, false)
	}()
	<-accepted
	cancel()
	select {
	case result := <-done:
		if result.State != "unavailable" || result.Settings != nil {
			t.Fatalf("%+v", result)
		}
	case <-time.After(time.Second):
		t.Fatal("cancel did not bound config read")
	}
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("diagnostic socket leaked after cancellation")
	}
}
