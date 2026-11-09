package daemon

import (
	"strconv"
	"testing"

	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/clock"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/config"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/dpll"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/event"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/utils"
	ptpv1 "github.com/k8snetworkplumbingwg/ptp-operator/api/v1"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
)

func TestSourceQualificationThresholdMetrics(t *testing.T) {
	t.Parallel()
	gauge := prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "test_source_threshold"}, []string{thresholdMetric, "node", "profile"})
	policy := sourceQualificationPolicy{enabled: true, config: utils.QualificationConfig{
		QualifiedThreshold: 100, DisqualifiedThreshold: 500, QualifiedSamples: 5, DisqualifiedSamples: 7,
	}}
	updateSourceQualificationMetrics(gauge, "node", "profile0", policy)
	for name, value := range map[string]float64{
		thresholdSourceQualified: 100, thresholdSourceDisqualified: 500,
		thresholdSourceQualifiedSamples: 5, thresholdSourceDisqualifiedSamples: 7,
		thresholdSourceUseS3: 0,
	} {
		require.Equal(t, value, testutil.ToFloat64(gauge.WithLabelValues(name, "node", "profile0")))
	}
	require.Equal(t, 5, testutil.CollectAndCount(gauge))
	updateSourceQualificationMetrics(gauge, "node", "profile1", policy)
	updateSourceQualificationMetrics(gauge, "node", "profile0", sourceQualificationPolicy{})
	require.Equal(t, 5, testutil.CollectAndCount(gauge), "switching to legacy removes only that profile's source series")
	updateSourceQualificationMetrics(gauge, "node", "profile1", sourceQualificationPolicy{})
	require.Zero(t, testutil.CollectAndCount(gauge))
}

// Gather rather than WithLabelValues: checking an absent zero-valued metric must
// not accidentally create it and hide missing emission (notably ptpSourceUseS3).
func thresholdSamples(t *testing.T, gauge *prometheus.GaugeVec, node, profile string) map[string]float64 {
	t.Helper()
	registry := prometheus.NewRegistry()
	registry.MustRegister(gauge)
	families, err := registry.Gather()
	require.NoError(t, err)
	values := map[string]float64{}
	for _, family := range families {
		for _, metric := range family.Metric {
			labels := map[string]string{}
			for _, label := range metric.Label {
				labels[label.GetName()] = label.GetValue()
			}
			if labels["node"] == node && labels["profile"] == profile {
				values[labels[thresholdMetric]] = metric.GetGauge().GetValue()
			}
		}
	}
	return values
}

func newThresholdTestGauge() *prometheus.GaugeVec {
	return prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "test_ptp_threshold"}, []string{thresholdMetric, "node", "profile"})
}

func TestTBCAllThresholdReadback(t *testing.T) {
	t.Parallel()
	gauge := newThresholdTestGauge()
	profile := &ptpv1.PtpProfile{Name: utils.ToPtr("t-bc_01-tbc-tr"), Phc2sysOpts: utils.ToPtr("-a -r"),
		PtpSettings: map[string]string{"clockType": TBC, "inSyncConditionThreshold": "10", "inSyncConditionTimes": "12"},
		PtpClockThreshold: &ptpv1.PtpClockThreshold{
			PtpSourceQualifiedThreshold: utils.ToPtr(int64(20)), PtpSourceDisqualifiedThreshold: utils.ToPtr(int64(30)),
			PtpSourceQualifiedSamples: utils.ToPtr(int64(5)), PtpSourceDisqualifiedSamples: utils.ToPtr(int64(5)),
			SysOffsetInSyncThreshold: utils.ToPtr(int64(100)), SysOffsetOutOfSyncThreshold: utils.ToPtr(int64(500)), SysOffsetSamples: utils.ToPtr(int64(10)),
		},
	}
	// Simulate stale read-back from an older daemon/profile application.
	for _, name := range []string{"HoldOverTimeout", "MaxOffsetThreshold", "MinOffsetThreshold"} {
		gauge.WithLabelValues(name, "node", *profile.Name).Set(100)
	}
	updateProfileThresholdMetrics(gauge, "node", profile, resolvePTPThreshold(profile))
	updateOSClockThresholdMetrics(gauge, "node", *profile.Name, osClockFilterConfig(profile))
	limits := tbcDPLLThresholds{inSyncThreshold: 10, inSyncSamples: 12, maxInSpecOffset: 20, localMaxHoldoverOffset: 30, localHoldoverTimeout: 288}
	updateTBCDPLLThresholdMetrics(gauge, "node", profile, config.Iface{Source: event.PTP4l}, 0, limits)
	want := map[string]float64{
		"MaxInSpecOffset": 20, "LocalMaxHoldoverOffSet": 30, "LocalHoldoverTimeout": 288,
		"inSyncConditionThreshold": 10, "inSyncConditionTimes": 12,
		"ptpSourceQualifiedThreshold": 20, "ptpSourceDisqualifiedThreshold": 30,
		"ptpSourceQualifiedSamples": 5, "ptpSourceDisqualifiedSamples": 5, "ptpSourceUseS3": 0,
		"SysOffsetInSyncThreshold": 100, "SysOffsetOutOfSyncThreshold": 500, "SysOffsetSamples": 10,
	}
	require.Equal(t, want, thresholdSamples(t, gauge, "node", *profile.Name))
	// Phase-only followers must not replace the leader's 20/30/288 policy with
	// their deliberately disabled holdover values (0/0/1).
	updateTBCDPLLThresholdMetrics(gauge, "node", profile, config.Iface{Source: event.PPS}, dpll.FlagOnlyPhaseStatus, tbcDPLLThresholds{localHoldoverTimeout: 1})
	updateTBCDPLLThresholdMetrics(gauge, "node", profile, config.Iface{Source: event.PTP4l}, dpll.FlagOnlyPhaseStatus, tbcDPLLThresholds{localHoldoverTimeout: 1})
	require.Equal(t, want, thresholdSamples(t, gauge, "node", *profile.Name))
}

func TestTBCThresholdProfileCleanupAndLegacyCompatibility(t *testing.T) {
	t.Parallel()
	gauge := newThresholdTestGauge()
	profile := &ptpv1.PtpProfile{Name: utils.ToPtr("profile")}
	legacy := event.PtpClockThreshold{MaxOffsetThreshold: 100, MinOffsetThreshold: -100, HoldOverTimeout: 5}
	updateProfileThresholdMetrics(gauge, "node", profile, legacy)
	wantLegacy := map[string]float64{"MaxOffsetThreshold": 100, "MinOffsetThreshold": -100, "HoldOverTimeout": 5}
	require.Equal(t, wantLegacy, thresholdSamples(t, gauge, "node", *profile.Name))
	// Controlled TT detection is by role, not by a t-bc_* naming convention.
	profile.PtpSettings = map[string]string{"controllingProfile": "upstream"}
	updateProfileThresholdMetrics(gauge, "node", profile, legacy)
	require.Empty(t, thresholdSamples(t, gauge, "node", *profile.Name))
	profile.PtpSettings = map[string]string{"clockType": TBC}
	profile.PtpClockThreshold = &ptpv1.PtpClockThreshold{MaxOffsetThreshold: 100, PtpSourceUseS3: true}
	updateProfileThresholdMetrics(gauge, "node", profile, legacy)
	require.Equal(t, map[string]float64{
		thresholdSourceQualified: 100, thresholdSourceDisqualified: 100,
		thresholdSourceQualifiedSamples: 5, thresholdSourceDisqualifiedSamples: 5, thresholdSourceUseS3: 1,
	}, thresholdSamples(t, gauge, "node", *profile.Name))
	// Another profile/node is not deleted when this profile changes policy.
	other := &ptpv1.PtpProfile{Name: utils.ToPtr("other")}
	updateProfileThresholdMetrics(gauge, "node", other, legacy)
	updateProfileThresholdMetrics(gauge, "other-node", profile, legacy)
	profile.PtpSettings = nil
	profile.PtpClockThreshold = nil
	updateProfileThresholdMetrics(gauge, "node", profile, legacy)
	require.Equal(t, wantLegacy, thresholdSamples(t, gauge, "node", *profile.Name))
	require.Equal(t, wantLegacy, thresholdSamples(t, gauge, "node", *other.Name))
	require.Contains(t, thresholdSamples(t, gauge, "other-node", *profile.Name), thresholdSourceUseS3)
	// Removing phc2sys/source policy on reapply also removes their old series.
	profile.PtpSettings = map[string]string{"clockType": TBC}
	updateOSClockThresholdMetrics(gauge, "node", *profile.Name, clock.OSClockFilterConfig{InSyncThreshold: 10, OutOfSyncThreshold: 20, Samples: 5})
	updateProfileThresholdMetrics(gauge, "node", profile, legacy)
	require.Empty(t, thresholdSamples(t, gauge, "node", *profile.Name), "unset TBC policy must not advertise unused defaults")
}

func TestOSClockThresholdMetricsUseResolvedDefaults(t *testing.T) {
	t.Parallel()
	gauge := newThresholdTestGauge()
	profile := &ptpv1.PtpProfile{Name: utils.ToPtr("tr"), PtpSettings: map[string]string{"clockType": TBC}}
	updateOSClockThresholdMetrics(gauge, "node", *profile.Name, osClockFilterConfig(profile))
	require.Equal(t, map[string]float64{thresholdSysInSync: 100, thresholdSysOutOfSync: 100, thresholdSysSamples: 10}, thresholdSamples(t, gauge, "node", *profile.Name))
	profile.PtpClockThreshold = &ptpv1.PtpClockThreshold{MaxOffsetThreshold: 75, SysOffsetInSyncThreshold: utils.ToPtr(int64(0)), SysOffsetSamples: utils.ToPtr(int64(0))}
	updateOSClockThresholdMetrics(gauge, "node", *profile.Name, osClockFilterConfig(profile))
	require.Equal(t, map[string]float64{thresholdSysInSync: 0, thresholdSysOutOfSync: 75, thresholdSysSamples: 10}, thresholdSamples(t, gauge, "node", *profile.Name), "zero threshold is valid; invalid zero sample count resolves to the runtime default")
}

func TestTBCDPLLThresholdMetricsDefaultsAndDisabledHoldover(t *testing.T) {
	t.Parallel()
	gauge := newThresholdTestGauge()
	profile := &ptpv1.PtpProfile{Name: utils.ToPtr("tr"), PtpSettings: map[string]string{"clockType": TBC}}
	limits := tbcDPLLThresholds{
		inSyncThreshold: dpll.MaxInSpecOffset, inSyncSamples: 1,
		maxInSpecOffset: dpll.MaxInSpecOffset, localMaxHoldoverOffset: dpll.LocalMaxHoldoverOffSet,
		localHoldoverTimeout: dpll.LocalHoldoverTimeout,
	}
	updateTBCDPLLThresholdMetrics(gauge, "node", profile, config.Iface{Source: event.PTP4l}, 0, limits)
	values := thresholdSamples(t, gauge, "node", *profile.Name)
	require.Len(t, values, 5)
	require.Equal(t, float64(dpll.MaxInSpecOffset), values[thresholdInSync])
	require.Equal(t, float64(1), values[thresholdInSyncTimes])
	require.Equal(t, float64(dpll.LocalHoldoverTimeout), values[dpll.LocalHoldoverTimeoutStr])
	limits.localHoldoverTimeout = 0
	updateTBCDPLLThresholdMetrics(gauge, "node", profile, config.Iface{Source: event.PTP4l}, 0, limits)
	values = thresholdSamples(t, gauge, "node", *profile.Name)
	require.Contains(t, values, dpll.LocalHoldoverTimeoutStr)
	require.Zero(t, values[dpll.LocalHoldoverTimeoutStr], "disabled holdover is zero, not a missing/defaulted timeout")
	limits.localHoldoverTimeout = 200
	updateTBCDPLLThresholdMetrics(gauge, "node", profile, config.Iface{Source: event.PTP4l}, dpll.FlagNoFreqencyStatus, limits)
	values = thresholdSamples(t, gauge, "node", *profile.Name)
	require.Equal(t, float64(200), values[dpll.LocalHoldoverTimeoutStr], "a partial monitoring flag is not the composite phase-only follower flag")
	profile.PtpSettings["leadingInterface"] = "legacy-leader"
	limits.localHoldoverTimeout = 288
	updateTBCDPLLThresholdMetrics(gauge, "node", profile, config.Iface{Name: "legacy-leader", Source: event.PPS}, 0, limits)
	values = thresholdSamples(t, gauge, "node", *profile.Name)
	require.Equal(t, float64(288), values[dpll.LocalHoldoverTimeoutStr])
	updateTBCDPLLThresholdMetrics(gauge, "node", profile, config.Iface{Name: "follower", Source: event.PPS}, 0, tbcDPLLThresholds{})
	require.Equal(t, values, thresholdSamples(t, gauge, "node", *profile.Name))
}

func TestTBCThresholdMetricsConfigurationWiring(t *testing.T) {
	// These are the real configuration entry points, using the existing test
	// daemon. Do not run in parallel: constructors use shared config/metric state.
	for _, followerFirst := range []bool{false, true} {
		name := "metrics-leader-first"
		if followerFirst {
			name = "metrics-follower-first"
		}
		t.Run(name, func(t *testing.T) {
			ct := NewCondiitonsTester(t, event.TBC)
			dn := NewDaemonForTests(&ReadyTracker{}, ct.pm)
			t.Cleanup(dn.cancel)
			profile := &ptpv1.PtpProfile{Name: utils.ToPtr(name), Ptp4lOpts: utils.ToPtr(""), Phc2sysOpts: utils.ToPtr("-a -r"),
				PtpSettings: map[string]string{
					"clockType": TBC, "inSyncConditionThreshold": "10", "inSyncConditionTimes": "12",
					dpll.MaxInSpecOffsetStr: "20", dpll.LocalMaxHoldoverOffSetStr: "30", dpll.LocalHoldoverTimeoutStr: "288",
					"clockId[leader]": "1", "clockId[follower]": "2",
					dpll.PtpSettingsDpllFlagsKey("follower"): strconv.FormatUint(uint64(dpll.FlagOnlyPhaseStatus), 10),
				},
				PtpClockThreshold: &ptpv1.PtpClockThreshold{
					MaxOffsetThreshold:          100,
					PtpSourceQualifiedThreshold: utils.ToPtr(int64(20)), PtpSourceDisqualifiedThreshold: utils.ToPtr(int64(30)),
					PtpSourceQualifiedSamples: utils.ToPtr(int64(5)), PtpSourceDisqualifiedSamples: utils.ToPtr(int64(5)),
					SysOffsetInSyncThreshold: utils.ToPtr(int64(100)), SysOffsetOutOfSyncThreshold: utils.ToPtr(int64(500)), SysOffsetSamples: utils.ToPtr(int64(10)),
				},
			}
			t.Cleanup(func() { Threshold.DeletePartialMatch(prometheus.Labels{"node": NodeName, "profile": name}) })
			updatePTPThresholdMetrics(profile, resolvePTPThreshold(profile))
			require.NoError(t, dn.createProcesses(0, profile, &ProfileTopology{}, event.TBC, "leader", nil))
			ifaces := config.IFaces{{Name: "leader", Source: event.PTP4l}, {Name: "follower", Source: event.PPS}}
			if followerFirst {
				ifaces[0], ifaces[1] = ifaces[1], ifaces[0]
			}
			proc := &ptpProcess{nodeProfile: profile, clockType: event.TBC, ifaces: ifaces, ptpClockThreshold: getPTPThreshold(profile)}
			require.NoError(t, dn.setupDPLL(profile, event.TBC, proc))
			values := thresholdSamples(t, Threshold, NodeName, name)
			require.Len(t, values, 13)
			require.Equal(t, float64(100), values[thresholdSysInSync])
			require.Equal(t, float64(500), values[thresholdSysOutOfSync])
			require.Equal(t, float64(10), values[thresholdSysSamples])
			require.Equal(t, float64(10), values[thresholdInSync])
			require.Equal(t, float64(12), values[thresholdInSyncTimes])
			require.NotContains(t, values, "HoldOverTimeout")
			require.NotContains(t, values, "MaxOffsetThreshold")
			require.NotContains(t, values, "MinOffsetThreshold")
			for i, iface := range ifaces {
				device := proc.depProcess[i].(*dpll.DpllConfig)
				if iface.Source == event.PTP4l {
					require.Equal(t, float64(device.MaxInSpecOffset), values["MaxInSpecOffset"])
					require.Equal(t, float64(device.LocalMaxHoldoverOffSet), values["LocalMaxHoldoverOffSet"])
					require.Equal(t, float64(device.LocalHoldoverTimeout), values["LocalHoldoverTimeout"])
				}
			}
		})
	}
}
