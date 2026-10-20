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
// not false. The trusted-IP list, accounts, messages and order defaults are not
// retained; the list is reduced to whether a loopback address is on it.
type APISettings struct {
	ReadOnly                *bool
	DownloadOpenOrders      *bool
	VolumeInLots            *bool
	RetainOrdersOnReconnect *bool
	RejectAboveRate         *bool
	LocalhostOnly           *bool
	LoopbackTrusted         *bool
	CreateAPILog            *bool
	MarketDataInAPILog      *bool
	LoggingLevel            *string
	SocketPort              *int
	MasterClientID          *int
}

// APIPrecautions names the order-precaution bypasses the application has on,
// in the broker's own order. Empty means every bypass is off.
type APIPrecautions struct {
	Bypassed []string
}

// precautionBypassNames maps ApiPrecautionsConfig field numbers to short names.
var precautionBypassNames = map[int]string{
	1: "order precautions", 2: "bond warning", 3: "negative-yield confirmation",
	4: "called-bond warning", 5: "same-action pair-trade warning", 6: "flagged-accounts warning",
	7: "price-based volatility warning", 8: "redirect-order warning", 9: "no-overfill protection",
	10: "route marketable to BBO",
}

// LockAndExit is the application's daily log-off or restart as it reports it:
// Type is "restart" or "logoff", Time is 12-hour "HH:MM" and Period "AM" or "PM".
// The broker reports no time zone for it.
type LockAndExit struct {
	Type   string
	Time   string
	Period string
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
	Precautions   *APIPrecautions
	LockAndExit   *LockAndExit
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
			parsed, err := parseAPIConfig(body, requestID)
			if err != nil {
				out.Reason = "malformed_config"
				return out
			}
			out.State, out.Reason = "observed", ""
			out.Settings, out.Precautions, out.LockAndExit = parsed.settings, parsed.precautions, parsed.lockAndExit
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

type parsedAPIConfig struct {
	settings    *APISettings
	precautions *APIPrecautions
	lockAndExit *LockAndExit
}

// parseAPIConfig reads ConfigResponse: reqId (1), lockAndExit (2) and api (4),
// whose precautions (1) and settings (2) are the allowlisted messages. The
// messages (3) and orders (5) sections are skipped unread.
func parseAPIConfig(body []byte, requestID int) (parsedAPIConfig, error) {
	var out parsedAPIConfig
	id := -1
	err := diagnosticFields(body, func(field, wire int, value []byte) error {
		switch field {
		case 1:
			v, e := protoVarintValue(field, wire, value)
			if e != nil {
				return e
			}
			id = int(v)
		case 2:
			if wire != protoWireBytes {
				return fmt.Errorf("invalid lock-and-exit wire type")
			}
			var e error
			out.lockAndExit, e = parseLockAndExit(value)
			return e
		case 4:
			if wire != protoWireBytes {
				return fmt.Errorf("invalid api config wire type")
			}
			return diagnosticFields(value, func(f, w int, v []byte) error {
				if f != 1 && f != 2 {
					return nil
				}
				if w != protoWireBytes {
					return fmt.Errorf("invalid api settings wire type")
				}
				var e error
				if f == 1 {
					out.precautions, e = parseAPIPrecautions(v)
				} else {
					out.settings, e = parseAPISettings(v)
				}
				return e
			}, 1, 2)
		}
		return nil
	}, 1, 2, 4)
	if err != nil {
		return parsedAPIConfig{}, err
	}
	if id != requestID || out.settings == nil {
		return parsedAPIConfig{}, fmt.Errorf("missing or uncorrelated config")
	}
	return out, nil
}

// maxDiagnosticText bounds every retained string: a level name or a clock
// reading is short, and anything longer is not the field it claims to be.
const maxDiagnosticText = 32

func diagnosticText(field, wire int, value []byte) (string, error) {
	if wire != protoWireBytes {
		return "", fmt.Errorf("invalid wire type for text field %d", field)
	}
	if len(value) > maxDiagnosticText {
		return "", fmt.Errorf("text field %d too long", field)
	}
	return strings.TrimSpace(string(value)), nil
}

func parseLockAndExit(body []byte) (*LockAndExit, error) {
	out := &LockAndExit{}
	err := diagnosticFields(body, func(field, wire int, value []byte) error {
		var target *string
		switch field {
		case 1:
			target = &out.Time
		case 2:
			target = &out.Period
		case 3:
			target = &out.Type
		default:
			return nil
		}
		text, err := diagnosticText(field, wire, value)
		if err != nil {
			return err
		}
		if field != 1 {
			text = strings.ToLower(text)
		}
		*target = text
		return nil
	}, 1, 2, 3)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func parseAPIPrecautions(body []byte) (*APIPrecautions, error) {
	out := &APIPrecautions{}
	seen := map[int]bool{}
	err := forEachProtoField(body, func(field, wire int, value []byte) error {
		name, known := precautionBypassNames[field]
		if !known {
			return nil
		}
		if seen[field] {
			return fmt.Errorf("duplicate precaution")
		}
		seen[field] = true
		v, err := protoVarintValue(field, wire, value)
		if err != nil {
			return err
		}
		if v > 1 {
			return fmt.Errorf("invalid precaution boolean")
		}
		if v == 1 {
			out.Bypassed = append(out.Bypassed, name)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// loopbackTrusted reports whether the broker's comma- or space-separated
// trusted-IP list names a loopback address. The list itself is never retained.
func loopbackTrusted(list string) bool {
	for entry := range strings.FieldsFuncSeq(list, func(r rune) bool { return r == ',' || r == ';' || r == ' ' || r == '\t' }) {
		switch strings.Trim(strings.ToLower(entry), "[]") {
		case "127.0.0.1", "::1", "localhost":
			return true
		}
	}
	return false
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
		case 10:
			target = &out.CreateAPILog
		case 11:
			target = &out.MarketDataInAPILog
		case 24:
			target = &out.VolumeInLots
		case 26:
			target = &out.RejectAboveRate
		case 31:
			target = &out.RetainOrdersOnReconnect
		case 35:
			target = &out.LocalhostOnly
		case 8, 17, 18, 36:
		default:
			return nil
		}
		if seen[field] {
			return fmt.Errorf("duplicate api setting")
		}
		seen[field] = true
		if field == 17 || field == 36 {
			// Text fields: the logging level is kept as a lower-case name; the
			// trusted-IP list is reduced to one fact and dropped.
			if field == 36 {
				if wire != protoWireBytes {
					return fmt.Errorf("invalid wire type for text field %d", field)
				}
				out.LoopbackTrusted = new(loopbackTrusted(string(value)))
				return nil
			}
			text, err := diagnosticText(field, wire, value)
			if err != nil {
				return err
			}
			out.LoggingLevel = new(strings.ToLower(text))
			return nil
		}
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
