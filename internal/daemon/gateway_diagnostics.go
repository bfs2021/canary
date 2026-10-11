package daemon

import (
	"context"

	"github.com/osauer/canary/v2/internal/rpc"
	ibkrlib "github.com/osauer/canary/v2/pkg/ibkr"
)

func gatewayAPIHealth() *rpc.GatewayAPIHealth {
	return &rpc.GatewayAPIHealth{TradingProtocolMax: ibkrlib.TradingProtocolMax, ConfigReadProtocol: ibkrlib.ConfigReadProtocol, ConfigState: "not_requested"}
}

func (s *Server) handleStatusHealthRequest(ctx context.Context, req *rpc.Request) (*rpc.HealthResult, error) {
	var params rpc.HealthParams
	if err := decodeParams(req.Params, &params); err != nil {
		return nil, err
	}
	res := s.handleStatusHealth()
	if !params.GatewayDiagnostics {
		return res, nil
	}
	if !s.gatewayDiagnosticsMu.TryLock() {
		res.GatewayAPI.ConfigState, res.GatewayAPI.Reason = "unavailable", "probe_in_progress"
		return res, nil
	}
	defer s.gatewayDiagnosticsMu.Unlock()
	s.mu.Lock()
	connector, epoch := s.connector, s.connectorEpoch
	s.mu.Unlock()
	if connector == nil || !res.Connected {
		res.GatewayAPI.ConfigState, res.GatewayAPI.Reason = "unavailable", "not_connected"
		return res, nil
	}
	session, ok := connector.CaptureSession()
	if !ok {
		res.GatewayAPI.ConfigState, res.GatewayAPI.Reason = "unavailable", "not_connected"
		return res, nil
	}
	diagnostic := connector.ReadAPIConfig(ctx)
	s.mu.Lock()
	current := s.connector == connector && s.connectorEpoch == epoch
	s.mu.Unlock()
	if !current || !connector.SessionReceiptCurrent(session) {
		res = s.statusHealthSnapshot()
		res.GatewayAPI.ConfigState, res.GatewayAPI.Reason = "unavailable", "session_changed"
		return res, nil
	}
	applyGatewayDiagnostic(res.GatewayAPI, diagnostic)
	return res, nil
}

func applyGatewayDiagnostic(out *rpc.GatewayAPIHealth, diagnostic ibkrlib.APIConfigDiagnostic) {
	out.ConfigState, out.Reason = diagnostic.State, diagnostic.Reason
	out.DiagnosticProtocol, out.AsOf = diagnostic.ServerVersion, diagnostic.AsOf
	if settings := diagnostic.Settings; settings != nil && diagnostic.State == "observed" {
		out.Settings = &rpc.GatewayAPISettings{
			ReadOnly: settings.ReadOnly, DownloadOpenOrders: settings.DownloadOpenOrders,
			VolumeInLots: settings.VolumeInLots, RetainOrdersOnReconnect: settings.RetainOrdersOnReconnect,
			RejectAboveRate: settings.RejectAboveRate, LocalhostOnly: settings.LocalhostOnly,
			SocketPort: settings.SocketPort, MasterClientID: settings.MasterClientID,
		}
	}
}
