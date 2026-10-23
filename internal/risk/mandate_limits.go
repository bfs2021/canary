package risk

import "strconv"

// MandateAdditionsOverrideControl is the one [mandate] key that takes a
// one-shot override (canary policy override --control mandate.vol_target_pct
// --reason TEXT --hours N): while it is active the mandate's volatility
// budget no longer refuses the owner's additions (owner decision 2026-10-23
// 18:22 CEST). What still bounds risk meanwhile: the drawdown brake at the
// 30% maximum loss and the mandate's own no-room hold, the owner sleeve's
// drawdown stop, every order cap, the premium limits on option additions
// (option_name_act_pct per name, the premium budget's act level for the
// book), the device confirmation of every order, and the override's own
// expiry under override.max_duration_hours. The other mandate keys change
// only by a policy revision.
const MandateAdditionsOverrideControl = "mandate.vol_target_pct"

// mandateLimits renders the [mandate] keys as constitution limits once the
// table is written, so `canary policy show --explain` names them and the
// override control is a known key. A file without the table lists none,
// as it carries no mandate.
func mandateLimits(c *Constitution, get func(key, value, source, meaning, enforcement string) ConstitutionLimit,
	pct func(v *float64) (string, string), money func(v *float64) (string, string)) []ConstitutionLimit {
	if c == nil || c.Mandate == nil {
		return nil
	}
	m := c.Mandate
	lossVal, lossSrc := pct(m.MaxLossPct)
	volVal, volSrc := pct(m.VolTargetPct)
	reserveVal, reserveSrc := money(m.TaxReserveBase)
	gapVal, gapSrc := pct(m.GapShockPct)
	roomVal, roomSrc := pct(m.GapRoomPct)
	startVal, startSrc := "unapproved", "unapproved"
	if m.StartedAt != "" {
		startVal, startSrc = strconv.Quote(m.StartedAt), "file"
	}
	return []ConstitutionLimit{
		get("mandate.max_loss_pct", lossVal, lossSrc,
			"The largest loss of the budget base the owner tolerates, in percent: room runs from 1 at a new high to 0 at this loss, where the book goes to its hedges. Changes only by a policy revision.", "hard"),
		get(MandateAdditionsOverrideControl, volVal, volSrc,
			"The yearly volatility budget in percent of the budget base at full room. It gates stock and ETF additions; option additions are gated by the premium limits instead and read it as a warning. The one mandate key that takes a one-shot override: while one is active the volatility budget refuses no addition of yours; the maximum loss, the owner sleeve's stop, the order caps, the premium limits and the device confirmation still bind.", "hard"),
		get("mandate.tax_reserve_base", reserveVal, reserveSrc,
			"The part of equity that is not risk base, in the base currency. Changes only by a policy revision.", "advisory"),
		get("mandate.started_at", startVal, startSrc,
			"The date the high-water mark counts from; a change starts the series again.", "advisory"),
		get("mandate.gap_shock_pct", gapVal, gapSrc,
			"The one-name gap each underlying is stressed by, in percent of its price. Changes only by a policy revision.", "hard"),
		get("mandate.gap_room_pct", roomVal, roomSrc,
			"The share of the remaining loss room one name may lose in that gap, in percent. Changes only by a policy revision.", "hard"),
	}
}
