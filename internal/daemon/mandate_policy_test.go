package daemon

import (
	"strings"
	"testing"
	"time"

	"github.com/osauer/canary/v2/internal/config"
	"github.com/osauer/canary/v2/internal/rpc"
)

const mandateTableTOML = `
[mandate]
max_loss_pct = 30.0
vol_target_pct = 15.0
tax_reserve_base = 70000.0
started_at = "2026-10-14"
gap_shock_pct = 25.0
gap_room_pct = 33.3

[mandate.sleeves]
owner_vol_pct = 7.0
core_vol_pct = 6.0
overlay_vol_pct = 4.0
`

// schema2ConstitutionTOML is a current-format constitution the mandate can
// be appended to.
func schema2ConstitutionTOML() string {
	s := strings.Replace(validRiskPolicyV3TOML(), "schema_version = 1", "schema_version = 2", 1)
	s = strings.Replace(s, `kind = "ibkr.risk_policy"`, `kind = "canary.risk_policy"`, 1)
	return strings.Replace(s, "policy_version = 3", "policy_version = 5", 1)
}

// A file written before [mandate] existed keeps both fingerprints; writing
// the table changes them, and the manager adopts it only with a higher
// policy_version.
func TestMandateTableFingerprintAndAdoption(t *testing.T) {
	m, path := newTestRiskPolicyManager(t, schema2ConstitutionTOML())
	before := m.snapshot()
	if before.status != rpc.RiskPolicyStatusActive || before.policy.Mandate != nil {
		t.Fatalf("fixture: %+v", before)
	}
	withoutTable := before.policy.FingerprintKey()
	effectiveWithout := before.policy.EffectiveFingerprintKey()
	c := *before.policy
	c.Mandate = nil
	if c.FingerprintKey() != withoutTable {
		t.Fatal("a nil mandate table changed the fingerprint")
	}

	writePolicyTestFile(t, path, schema2ConstitutionTOML()+mandateTableTOML)
	m.reload()
	if got := m.snapshot(); got.status != rpc.RiskPolicyStatusDrift || got.policy.Mandate != nil {
		t.Fatalf("the table was adopted without a version bump: %s %+v", got.status, got.policy.Mandate)
	}
	writePolicyTestFile(t, path, strings.Replace(schema2ConstitutionTOML(), "policy_version = 5", "policy_version = 6", 1)+mandateTableTOML)
	m.reload()
	got := m.snapshot()
	if got.status != rpc.RiskPolicyStatusActive || got.policy.Mandate == nil || !got.policy.MandateActive() || *got.policy.Mandate.MaxLossPct != 30 || got.policy.Mandate.Sleeves == nil || *got.policy.Mandate.Sleeves.OwnerVolPct != 7 {
		t.Fatalf("the table was not adopted: %s %+v", got.status, got.policy.Mandate)
	}
	if got.policy.FingerprintKey() == withoutTable || got.policy.EffectiveFingerprintKey() == effectiveWithout {
		t.Fatal("writing the table left a fingerprint unchanged")
	}
	if keys := got.policy.UnapprovedKeys(); len(keys) != 0 {
		t.Fatalf("a complete table reads unapproved keys %v", keys)
	}
	if start, ok := got.policy.Mandate.MandateStart(); !ok || start != time.Date(2026, 10, 14, 0, 0, 0, 0, time.UTC) {
		t.Fatalf("start %v %v", start, ok)
	}
}

// A legacy revision cannot name the table, an unknown mandate key is
// refused, and a file carrying either keeps the last good policy in force.
func TestMandateTableRefusedByOlderSemanticsAndUnknownKeys(t *testing.T) {
	m, path := newTestRiskPolicyManager(t, schema2ConstitutionTOML())
	good := m.snapshot().policy
	for name, body := range map[string]string{
		"legacy v3":        validRiskPolicyV3TOML() + mandateTableTOML,
		"unknown key":      strings.Replace(schema2ConstitutionTOML(), "policy_version = 5", "policy_version = 6", 1) + mandateTableTOML + "unknown_mandate_key = 1\n",
		"bad start":        strings.Replace(schema2ConstitutionTOML(), "policy_version = 5", "policy_version = 6", 1) + strings.Replace(mandateTableTOML, `"2026-10-14"`, `"14.10.2026"`, 1),
		"target above 100": strings.Replace(schema2ConstitutionTOML(), "policy_version = 5", "policy_version = 6", 1) + strings.Replace(mandateTableTOML, "vol_target_pct = 15.0", "vol_target_pct = 150.0", 1),
	} {
		t.Run(name, func(t *testing.T) {
			writePolicyTestFile(t, path, body)
			m.reload()
			got := m.snapshot()
			if got.status != rpc.RiskPolicyStatusError || got.policy != good {
				t.Fatalf("status %s message %q; last good policy kept: %v", got.status, got.message, got.policy == good)
			}
			switch name {
			case "legacy v3":
				if !strings.Contains(got.message, "schema 2 or legacy policy_version >= 4") {
					t.Fatalf("message %q", got.message)
				}
			case "unknown key":
				if !strings.Contains(got.message, "unknown risk policy key") || !strings.Contains(got.message, "unknown_mandate_key") {
					t.Fatalf("message %q", got.message)
				}
			case "bad start":
				if !strings.Contains(got.message, "started_at") {
					t.Fatalf("message %q", got.message)
				}
			case "target above 100":
				if !strings.Contains(got.message, "vol_target_pct") {
					t.Fatalf("message %q", got.message)
				}
			}
		})
	}
}

// An incomplete table is active and names its unapproved keys; a schema-1
// file at revision 4 carries the same semantics and may name the table.
func TestMandateTableIncompleteAndLegacyV4(t *testing.T) {
	partial := strings.Replace(schema2ConstitutionTOML(), "policy_version = 5", "policy_version = 6", 1) + "\n[mandate]\nmax_loss_pct = 30.0\n"
	m, _ := newTestRiskPolicyManager(t, partial)
	got := m.snapshot()
	if got.status != rpc.RiskPolicyStatusActive || !got.policy.MandateActive() {
		t.Fatalf("partial table: %s %q", got.status, got.message)
	}
	if keys := got.policy.Mandate.UnapprovedKeys(); strings.Join(keys, " ") != "mandate.vol_target_pct mandate.tax_reserve_base mandate.started_at mandate.gap_shock_pct mandate.gap_room_pct" {
		t.Fatalf("unapproved %v", keys)
	}
	if all := got.policy.UnapprovedKeys(); !strings.Contains(strings.Join(all, " "), "mandate.started_at") {
		t.Fatalf("the constitution's unapproved list lacks the mandate keys: %v", all)
	}
	v4 := strings.Replace(validRiskPolicyV3TOML(), "policy_version = 3", "policy_version = 4", 1) + mandateTableTOML
	m, _ = newTestRiskPolicyManager(t, v4)
	if got := m.snapshot(); got.status != rpc.RiskPolicyStatusActive || got.policy.Mandate == nil {
		t.Fatalf("legacy v4 with the table: %s %q", got.status, got.message)
	}
}

// The ensure migration never writes the table and leaves a written one as
// it is: the policy in force is unchanged by a conversion.
func TestMandateTableSurvivesMigrationUnchanged(t *testing.T) {
	k := policyFileKind{name: PolicyFileConstitution, migrate: migrateConstitutionPolicyFileFrom(config.Trading{}, true)}
	complete := strings.Replace(schema2ConstitutionTOML(), "policy_version = 5", "policy_version = 6", 1) + testOrderLimitsTOML + mandateTableTOML
	out, changes, _, err := previewPolicyFileMigration(k, []byte(complete), "test")
	if err != nil || len(changes) != 0 || string(out) != complete {
		t.Fatalf("a complete constitution with the table migrated: %v %v\n%s", changes, err, out)
	}
	// A file that still lacks [order_limits] gains them and nothing else:
	// the mandate table is byte for byte what the owner wrote.
	lacking := strings.Replace(schema2ConstitutionTOML(), "policy_version = 5", "policy_version = 6", 1) + mandateTableTOML
	out, changes, _, err = previewPolicyFileMigration(k, []byte(lacking), "test")
	if err != nil || len(changes) == 0 || !strings.Contains(string(out), mandateTableTOML) {
		t.Fatalf("migration touched the mandate table: %v %v\n%s", changes, err, out)
	}
	before, after := decodeTestConstitution(t, []byte(lacking)), decodeTestConstitution(t, out)
	if *before.Mandate.MaxLossPct != *after.Mandate.MaxLossPct || *before.Mandate.GapRoomPct != *after.Mandate.GapRoomPct || before.Mandate.StartedAt != after.Mandate.StartedAt {
		t.Fatalf("mandate numbers changed: %+v → %+v", before.Mandate, after.Mandate)
	}
	// The template shows every mandate key, commented out, and parses.
	template := string(ConstitutionPolicyTemplate("test"))
	for _, key := range []string{"# [mandate]", "# max_loss_pct = 0.0", "# gap_shock_pct = 25.0", "# gap_room_pct = 33.3", "# [mandate.sleeves]", "# overlay_vol_pct = 0.0"} {
		if !strings.Contains(template, key) {
			t.Fatalf("template lacks %q", key)
		}
	}
}
