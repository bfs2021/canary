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
	SocketPort              *int  `json:"socket_port,omitempty"`
	MasterClientID          *int  `json:"master_client_id,omitempty"`
}
