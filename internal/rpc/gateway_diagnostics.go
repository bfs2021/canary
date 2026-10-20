package rpc

import "time"

// HealthParams optionally requests an isolated read-only broker config probe.
// Normal health polling stays on the existing connection and cached state.
type HealthParams struct {
	GatewayDiagnostics bool `json:"gateway_diagnostics,omitempty"`
}

// GatewayAPIHealth separates negotiated protocol from the application's build.
// Settings are diagnostic observations only and never grant trading authority.
type GatewayAPIHealth struct {
	TradingProtocolMax int                 `json:"trading_protocol_max"`
	ConfigReadProtocol int                 `json:"config_read_protocol"`
	ConfigState        string              `json:"config_state"`
	Reason             string              `json:"reason,omitempty"`
	DiagnosticProtocol int                 `json:"diagnostic_protocol,omitempty"`
	AsOf               time.Time           `json:"as_of,omitzero"`
	Settings           *GatewayAPISettings `json:"settings,omitempty"`
	// Precautions and LockAndExit are present once a read observed them.
	Precautions *GatewayAPIPrecautions `json:"precautions,omitempty"`
	LockAndExit *GatewayLockAndExit    `json:"lock_and_exit,omitempty"`
}

// GatewayAPISettings preserves absent versus explicitly disabled settings.
// The broker settings are never modified by Canary diagnostics.
type GatewayAPISettings struct {
	ReadOnly                *bool `json:"read_only,omitempty"`
	DownloadOpenOrders      *bool `json:"download_open_orders,omitempty"`
	VolumeInLots            *bool `json:"volume_in_lots,omitempty"`
	RetainOrdersOnReconnect *bool `json:"retain_orders_on_reconnect,omitempty"`
	RejectAboveRate         *bool `json:"reject_above_rate,omitempty"`
	LocalhostOnly           *bool `json:"localhost_only,omitempty"`
	// LoopbackTrusted is the one fact kept from the trusted-IP list: whether a
	// loopback address is on it. The list itself is never retained.
	LoopbackTrusted    *bool   `json:"loopback_trusted,omitempty"`
	CreateAPILog       *bool   `json:"create_api_log,omitempty"`
	MarketDataInAPILog *bool   `json:"market_data_in_api_log,omitempty"`
	LoggingLevel       *string `json:"logging_level,omitempty"`
	SocketPort         *int    `json:"socket_port,omitempty"`
	MasterClientID     *int    `json:"master_client_id,omitempty"`
}

// GatewayAPIPrecautions names the order-precaution bypasses that are on.
// An empty list means every bypass is off.
type GatewayAPIPrecautions struct {
	Bypassed []string `json:"bypassed"`
}

// GatewayLockAndExit is the application's daily log-off or restart as it
// reports it: Type "restart" or "logoff", a 12-hour Time and an "am"/"pm"
// Period. The broker reports no time zone for it.
type GatewayLockAndExit struct {
	Type   string `json:"type"`
	Time   string `json:"time"`
	Period string `json:"period"`
}

// GatewaySetupHealth is the daemon's plain-language reading of the broker
// application's settings against what Canary needs, from the config read it
// makes once after each connect. State is as_expected, attention or unknown.
// Notes are short sentences for people; nothing here grants or withdraws
// trading authority, and Canary never changes a setting.
type GatewaySetupHealth struct {
	State  string    `json:"state"`
	Reason string    `json:"reason,omitempty"`
	AsOf   time.Time `json:"as_of,omitzero"`
	Notes  []string  `json:"notes,omitempty"`
}

// GatewaySetupHealth states: every checked setting matches, at least one
// differs, or the read has not landed or failed (Reason says why).
const (
	GatewaySetupAsExpected = "as_expected"
	GatewaySetupAttention  = "attention"
	GatewaySetupUnknown    = "unknown"
)
