package ibkr

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"time"
)

// TradingProtocolMax is the tested ceiling for all normal broker connections.
const TradingProtocolMax = maxClientVersion

// ConfigReadProtocol is the first protocol with the read-only config request.
// It is used only on a short-lived diagnostic socket, never for trading/data.
const ConfigReadProtocol = 219

// APISettings contains only allowlisted operational settings. Nil means absent,
// not false. Trusted IPs, accounts, messages and order defaults are not retained.
type APISettings struct {
	ReadOnly                *bool
	DownloadOpenOrders      *bool
	VolumeInLots            *bool
	RetainOrdersOnReconnect *bool
	RejectAboveRate         *bool
	LocalhostOnly           *bool
	SocketPort              *int
	MasterClientID          *int
}

// APIConfigDiagnostic is an observation of a separate bounded read-only session.
// State is observed, unsupported, unavailable, or rejected. Reason is a stable
// local code, never raw broker text. ServerVersion is negotiated, not a build.
type APIConfigDiagnostic struct {
	State         string
	Reason        string
	ServerVersion int
	AsOf          time.Time
	Settings      *APISettings
}

// ReadAPIConfig reads settings without changing the main connection or TWS.
// Only StartAPI and ReqConfig can be sent. No subscription, binding, order,
// update-config, reconnect, or fallback-port path exists on this socket.
func (c *Connector) ReadAPIConfig(ctx context.Context) APIConfigDiagnostic {
	c.mu.RLock()
	conn := c.conn
	c.mu.RUnlock()
	if conn == nil || !conn.IsConnected() {
		return APIConfigDiagnostic{State: "unavailable", Reason: "not_connected", AsOf: time.Now().UTC()}
	}
	// Endpoint and TLS settings are immutable for the life of a Connection.
	cfg := &ConnectionConfig{Host: conn.config.Host, Port: conn.config.Port,
		TLSInsecureSkipVerify: conn.config.TLSInsecureSkipVerify, TLSServerName: conn.config.TLSServerName,
		ConnectTimeout: 5 * time.Second}
	return readAPIConfig(ctx, cfg, conn.UsingTLS())
}

func readAPIConfig(ctx context.Context, cfg *ConnectionConfig, tls bool) (out APIConfigDiagnostic) {
	out.State, out.Reason = "unavailable", "transport_failed"
	defer func() { out.AsOf = time.Now().UTC() }()
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	socket, err := (&Connection{config: cfg}).dialEndpoint(ctx, tls)
	if err != nil {
		return out
	}
	defer socket.Close()
	deadline, _ := ctx.Deadline()
	if err = socket.SetDeadline(deadline); err != nil {
		return out
	}
	stop := context.AfterFunc(ctx, func() { _ = socket.Close() })
	defer stop()
	return probeAPIConfig(socket)
}

func probeAPIConfig(socket net.Conn) (out APIConfigDiagnostic) {
	out.State, out.Reason = "unavailable", "transport_failed"
	defer func() { out.AsOf = time.Now().UTC() }()
	descriptor := []byte("v100..219\x00")
	hello := append([]byte("API\x00"), binary.BigEndian.AppendUint32(nil, uint32(len(descriptor)))...)
	hello = append(hello, descriptor...)
	if err := writeDiagnosticBytes(socket, hello); err != nil {
		return out
	}
	reply, err := readDiagnosticFrame(socket)
	if err != nil {
		return out
	}
	version, err := strconv.Atoi(strings.Split(string(reply), "\x00")[0])
	if err != nil || version < minServerVersionRequired || version > ConfigReadProtocol {
		out.Reason = "invalid_protocol"
		return out
	}
	out.ServerVersion = version
	if version < ConfigReadProtocol {
		out.State, out.Reason = "unsupported", "config_requires_protocol_219"
		return out
	}
	// A high random nonzero ID avoids client-0 order binding and the main client.
	// A collision is rejected, never retried with another ID.
	var entropy [4]byte
	if _, err = rand.Read(entropy[:]); err != nil {
		out.Reason = "client_id_unavailable"
		return out
	}
	id := int32(1_000_000_000 + binary.BigEndian.Uint32(entropy[:])%1_000_000_000)
	if err = writeDiagnosticProto(socket, 271, protoAppendInt32(nil, 1, id)); err != nil {
		return out
	}
	const requestID = 1
	requested := false
	// Bound memory, time and unsolicited traffic. Ignore account/order callbacks
	// completely; do not log, decode, bind, publish or retain them.
	for range 128 {
		reply, err = readDiagnosticFrame(socket)
		if err != nil {
			return out
		}
		if len(reply) < 4 {
			out.Reason = "malformed_response"
			return out
		}
		msg := binary.BigEndian.Uint32(reply[:4])
		body := reply[4:]
		switch msg {
		case 209: // NextValidId proves StartAPI was accepted.
			if !requested {
				if err = writeDiagnosticProto(socket, 308, protoAppendInt32(nil, 1, requestID)); err != nil {
					return out
				}
				requested = true
			}
		case 204: // ErrorMessage; farm status notifications are not request failures.
			req, code, parseErr := diagnosticError(body)
			if parseErr != nil {
				out.Reason = "malformed_response"
				return out
			}
			if code == 326 || (requested && req == requestID) {
				out.State, out.Reason = "rejected", "broker_rejected_config"
				return out
			}
		case 310:
			if !requested {
				out.Reason = "unexpected_config_response"
				return out
			}
			settings, err := parseAPIConfig(body, requestID)
			if err != nil {
				out.Reason = "malformed_config"
				return out
			}
			out.State, out.Reason, out.Settings = "observed", "", settings
			return out
		}
	}
	out.Reason = "response_limit"
	return out
}

func writeDiagnosticBytes(w io.Writer, b []byte) error {
	n, err := w.Write(b)
	if err == nil && n != len(b) {
		err = io.ErrShortWrite
	}
	return err // No retries: an uncertain write closes this diagnostic socket.
}

func writeDiagnosticProto(w io.Writer, id uint32, body []byte) error {
	frame := binary.BigEndian.AppendUint32(nil, uint32(4+len(body)))
	frame = binary.BigEndian.AppendUint32(frame, id)
	return writeDiagnosticBytes(w, append(frame, body...))
}

func readDiagnosticFrame(r io.Reader) ([]byte, error) {
	var size [4]byte
	if _, err := io.ReadFull(r, size[:]); err != nil {
		return nil, err
	}
	n := binary.BigEndian.Uint32(size[:])
	if n == 0 || n > 1<<20 {
		return nil, fmt.Errorf("invalid diagnostic frame length")
	}
	payload := make([]byte, int(n))
	_, err := io.ReadFull(r, payload)
	return payload, err
}

func diagnosticError(body []byte) (id, code int, err error) {
	id = -1
	err = forEachProtoField(body, func(field, wire int, value []byte) error {
		if field != 1 && field != 3 {
			return nil
		}
		v, e := protoVarintValue(field, wire, value)
		if e != nil {
			return e
		}
		if field == 1 {
			id = int(int32(v))
		} else {
			code = int(v)
		}
		return nil
	})
	return
}

func parseAPIConfig(body []byte, requestID int) (*APISettings, error) {
	var settings *APISettings
	id := -1
	err := diagnosticFields(body, func(field, wire int, value []byte) error {
		switch field {
		case 1:
			v, e := protoVarintValue(field, wire, value)
			if e != nil {
				return e
			}
			id = int(v)
		case 4:
			if wire != protoWireBytes {
				return fmt.Errorf("invalid api config wire type")
			}
			return diagnosticFields(value, func(f, w int, v []byte) error {
				if f != 2 {
					return nil
				}
				if w != protoWireBytes {
					return fmt.Errorf("invalid api settings wire type")
				}
				var e error
				settings, e = parseAPISettings(v)
				return e
			}, 2)
		}
		return nil
	}, 1, 4)
	if err != nil {
		return nil, err
	}
	if id != requestID || settings == nil {
		return nil, fmt.Errorf("missing or uncorrelated config")
	}
	return settings, nil
}

// Repeated ignored fields are permitted; retained singular fields must agree
// on a single value rather than hiding contradictory evidence.
func diagnosticFields(body []byte, visit func(int, int, []byte) error, unique ...int) error {
	seen := map[int]bool{}
	return forEachProtoField(body, func(f, w int, v []byte) error {
		for _, field := range unique {
			if f != field {
				continue
			}
			if seen[f] {
				return fmt.Errorf("duplicate config field")
			}
			seen[f] = true
		}
		return visit(f, w, v)
	})
}

func parseAPISettings(body []byte) (*APISettings, error) {
	out := &APISettings{}
	seen := map[int]bool{}
	err := forEachProtoField(body, func(field, wire int, value []byte) error {
		var target **bool
		switch field {
		case 1:
			target = &out.ReadOnly
		case 3:
			target = &out.DownloadOpenOrders
		case 24:
			target = &out.VolumeInLots
		case 26:
			target = &out.RejectAboveRate
		case 31:
			target = &out.RetainOrdersOnReconnect
		case 35:
			target = &out.LocalhostOnly
		case 8, 18:
		default:
			return nil
		}
		if seen[field] {
			return fmt.Errorf("duplicate api setting")
		}
		seen[field] = true
		v, err := protoVarintValue(field, wire, value)
		if err != nil {
			return err
		}
		if target != nil {
			if v > 1 {
				return fmt.Errorf("invalid api setting boolean")
			}
			*target = new(v == 1)
		} else {
			if v > maxProtoInt32 || (field == 8 && (v == 0 || v > 65535)) {
				return fmt.Errorf("invalid api setting integer")
			}
			n := int(v)
			if field == 8 {
				out.SocketPort = &n
			} else {
				out.MasterClientID = &n
			}
		}
		return nil
	})
	return out, err
}
