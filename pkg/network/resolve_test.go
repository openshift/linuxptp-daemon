package network

import (
	"encoding/json"
	"testing"

	ptpv1 "github.com/k8snetworkplumbingwg/ptp-operator/api/v1"
	ptpv2alpha1 "github.com/k8snetworkplumbingwg/ptp-operator/api/v2alpha1"
	apiextensions "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	testIfOld   = "ens5f0"
	testIfNew   = "ens5f0np0"
	testIf2Old  = "ens5f1"
	testIf2New  = "ens5f1np1"
	testProfile = "test"
)

func TestResolve_NameExistsAsIs(t *testing.T) {
	r := NewInterfaceResolverWithInterfaces([]string{testIfOld, testIf2Old})
	resolved, remapped := r.Resolve(testIfOld)
	if remapped {
		t.Error("expected no remap when name exists")
	}
	if resolved != testIfOld {
		t.Errorf("expected %s, got %s", testIfOld, resolved)
	}
}

func TestResolve_NpNSuffix(t *testing.T) {
	r := NewInterfaceResolverWithInterfaces([]string{testIfNew, testIf2New})
	resolved, remapped := r.Resolve(testIfOld)
	if !remapped {
		t.Error("expected remap")
	}
	if resolved != testIfNew {
		t.Errorf("expected %s, got %s", testIfNew, resolved)
	}
}

func TestResolve_MultipleCandidates(t *testing.T) {
	// Insert higher-numbered suffix first so map insertion order cannot
	// accidentally match the expected lowest-numbered result.
	r := NewInterfaceResolverWithInterfaces([]string{"ens5f0np1", "ens5f0np10", testIfNew})
	resolved, remapped := r.Resolve(testIfOld)
	if !remapped {
		t.Error("expected remap")
	}
	if resolved != testIfNew {
		t.Errorf("expected lowest-numbered candidate %s, got %s", testIfNew, resolved)
	}
}

func TestResolve_NoMatch(t *testing.T) {
	r := NewInterfaceResolverWithInterfaces([]string{"eth0", "eth1"})
	resolved, remapped := r.Resolve(testIfOld)
	if remapped {
		t.Error("expected no remap when no match")
	}
	if resolved != testIfOld {
		t.Errorf("expected original %s, got %s", testIfOld, resolved)
	}
}

func TestResolve_Empty(t *testing.T) {
	r := NewInterfaceResolverWithInterfaces([]string{testIfNew})
	resolved, remapped := r.Resolve("")
	if remapped {
		t.Error("expected no remap for empty")
	}
	if resolved != "" {
		t.Errorf("expected empty, got %s", resolved)
	}
}

func TestResolve_Caching(t *testing.T) {
	r := NewInterfaceResolverWithInterfaces([]string{testIfNew})

	r1, _ := r.Resolve(testIfOld)
	r2, _ := r.Resolve(testIfOld)
	if r1 != r2 {
		t.Errorf("cache inconsistency: %s vs %s", r1, r2)
	}
}

func TestResolveProfileInterfaces_InterfaceField(t *testing.T) {
	r := NewInterfaceResolverWithInterfaces([]string{testIfNew})
	iface := testIfOld
	name := testProfile
	profile := &ptpv1.PtpProfile{
		Name:      &name,
		Interface: &iface,
	}
	r.ResolveProfileInterfaces(profile)
	if *profile.Interface != testIfNew {
		t.Errorf("expected %s, got %s", testIfNew, *profile.Interface)
	}
}

func TestResolveProfileInterfaces_PtpSettingsValues(t *testing.T) {
	r := NewInterfaceResolverWithInterfaces([]string{testIfNew, testIf2New})
	name := testProfile
	profile := &ptpv1.PtpProfile{
		Name: &name,
		PtpSettings: map[string]string{
			"leadingInterface": testIfOld,
			"upstreamPort":     "ens5f0,ens5f1",
		},
	}
	r.ResolveProfileInterfaces(profile)

	if profile.PtpSettings["leadingInterface"] != testIfNew {
		t.Errorf("leadingInterface: expected %s, got %s", testIfNew, profile.PtpSettings["leadingInterface"])
	}
	if profile.PtpSettings["upstreamPort"] != "ens5f0np0,ens5f1np1" {
		t.Errorf("upstreamPort: expected ens5f0np0,ens5f1np1, got %s", profile.PtpSettings["upstreamPort"])
	}
}

func TestResolveProfileInterfaces_PtpSettingsKeys(t *testing.T) {
	r := NewInterfaceResolverWithInterfaces([]string{testIfNew})
	name := testProfile
	profile := &ptpv1.PtpProfile{
		Name: &name,
		PtpSettings: map[string]string{
			"dpll.ens5f0.ignore": "true",
			"dpll.ens5f0.flags":  "0x1",
			"clockId[ens5f0]":    "0x1234",
			"someOtherSetting":   "value",
		},
	}
	r.ResolveProfileInterfaces(profile)

	if _, ok := profile.PtpSettings["dpll.ens5f0np0.ignore"]; !ok {
		t.Error("dpll.ens5f0.ignore should be renamed to dpll.ens5f0np0.ignore")
	}
	if _, ok := profile.PtpSettings["dpll.ens5f0np0.flags"]; !ok {
		t.Error("dpll.ens5f0.flags should be renamed to dpll.ens5f0np0.flags")
	}
	if _, ok := profile.PtpSettings["clockId[ens5f0np0]"]; !ok {
		t.Error("clockId[ens5f0] should be renamed to clockId[ens5f0np0]")
	}
	if v := profile.PtpSettings["someOtherSetting"]; v != "value" {
		t.Errorf("unrelated setting should be unchanged, got %s", v)
	}
}

func TestResolveProfileInterfaces_PluginDevices(t *testing.T) {
	r := NewInterfaceResolverWithInterfaces([]string{testIfNew})
	name := testProfile

	pluginOpts := map[string]any{
		"devices": []any{testIfOld},
		"pins": map[string]any{
			testIfOld: []any{map[string]any{"name": "SMA1"}},
		},
	}
	raw, _ := json.Marshal(pluginOpts)
	profile := &ptpv1.PtpProfile{
		Name: &name,
		Plugins: map[string]*apiextensions.JSON{
			"e810": {Raw: raw},
		},
	}
	r.ResolveProfileInterfaces(profile)

	var result map[string]any
	_ = json.Unmarshal(profile.Plugins["e810"].Raw, &result)

	devices := result["devices"].([]any)
	if devices[0].(string) != testIfNew {
		t.Errorf("plugin device: expected %s, got %s", testIfNew, devices[0])
	}

	pins := result["pins"].(map[string]any)
	if _, ok := pins[testIfNew]; !ok {
		t.Error("plugin pins key should be renamed to ens5f0np0")
	}
	if _, ok := pins[testIfOld]; ok {
		t.Error("old plugin pins key ens5f0 should be removed")
	}
}

func TestResolveProfileInterfaces_PluginInterconnections(t *testing.T) {
	r := NewInterfaceResolverWithInterfaces([]string{testIfNew, testIf2New})
	name := testProfile

	pluginOpts := map[string]any{
		"interconnections": []any{
			map[string]any{
				"id":           testIfOld,
				"upstreamPort": testIfOld + "," + testIf2Old,
			},
		},
	}
	raw, _ := json.Marshal(pluginOpts)
	profile := &ptpv1.PtpProfile{
		Name: &name,
		Plugins: map[string]*apiextensions.JSON{
			"e810": {Raw: raw},
		},
	}
	r.ResolveProfileInterfaces(profile)

	var result map[string]any
	_ = json.Unmarshal(profile.Plugins["e810"].Raw, &result)

	interconnections := result["interconnections"].([]any)
	elem := interconnections[0].(map[string]any)
	if elem["id"].(string) != testIfNew {
		t.Errorf("interconnections id: expected %s, got %s", testIfNew, elem["id"])
	}
	if elem["upstreamPort"].(string) != testIfNew+","+testIf2New {
		t.Errorf("interconnections upstreamPort: expected %s, got %s", testIfNew+","+testIf2New, elem["upstreamPort"])
	}
}

func TestResolveProfileInterfaces_NoChangeWhenNamesExist(t *testing.T) {
	r := NewInterfaceResolverWithInterfaces([]string{testIfOld, testIf2Old})
	name := testProfile
	iface := testIfOld
	conf := "[global]\ndomainNumber 24\n[ens5f0]\nmasterOnly 1"
	originalConf := conf
	profile := &ptpv1.PtpProfile{
		Name:      &name,
		Interface: &iface,
		Ptp4lConf: &conf,
		PtpSettings: map[string]string{
			"leadingInterface": testIfOld,
		},
	}
	r.ResolveProfileInterfaces(profile)

	if *profile.Interface != testIfOld {
		t.Errorf("interface should be unchanged, got %s", *profile.Interface)
	}
	if *profile.Ptp4lConf != originalConf {
		t.Errorf("ptp4lConf should be unchanged")
	}
	if profile.PtpSettings["leadingInterface"] != testIfOld {
		t.Errorf("leadingInterface should be unchanged")
	}
}

func TestResolveProfileInterfaces_NilFields(_ *testing.T) {
	r := NewInterfaceResolverWithInterfaces([]string{testIfNew})
	name := testProfile
	profile := &ptpv1.PtpProfile{
		Name: &name,
	}
	r.ResolveProfileInterfaces(profile)
}

func TestResolveHardwareConfigInterfaces(t *testing.T) {
	r := NewInterfaceResolverWithInterfaces([]string{testIfNew, testIf2New})

	hwConfigs := []ptpv2alpha1.HardwareConfig{
		{
			ObjectMeta: metav1.ObjectMeta{Name: "test-hw"},
			Spec: ptpv2alpha1.HardwareConfigSpec{
				Profile: ptpv2alpha1.HardwareProfile{
					ClockChain: &ptpv2alpha1.ClockChain{
						Structure: []ptpv2alpha1.Subsystem{
							{
								Name: "sub1",
								DPLL: ptpv2alpha1.DPLL{NetworkInterface: testIfOld},
								Ethernet: []ptpv2alpha1.Ethernet{
									{Ports: []string{testIfOld, testIf2Old}},
								},
							},
						},
						Behavior: &ptpv2alpha1.Behavior{
							Sources: []ptpv2alpha1.SourceConfig{
								{
									Name:             "ptp",
									SourceType:       "ptpTimeReceiver",
									PTPTimeReceivers: []string{testIf2Old},
								},
							},
						},
					},
				},
			},
		},
	}

	r.ResolveHardwareConfigInterfaces(hwConfigs)

	sub := hwConfigs[0].Spec.Profile.ClockChain.Structure[0]
	if sub.DPLL.NetworkInterface != testIfNew {
		t.Errorf("DPLL.NetworkInterface: expected %s, got %s", testIfNew, sub.DPLL.NetworkInterface)
	}
	if sub.Ethernet[0].Ports[0] != testIfNew {
		t.Errorf("Ethernet.Ports[0]: expected %s, got %s", testIfNew, sub.Ethernet[0].Ports[0])
	}
	if sub.Ethernet[0].Ports[1] != testIf2New {
		t.Errorf("Ethernet.Ports[1]: expected %s, got %s", testIf2New, sub.Ethernet[0].Ports[1])
	}

	src := hwConfigs[0].Spec.Profile.ClockChain.Behavior.Sources[0]
	if src.PTPTimeReceivers[0] != testIf2New {
		t.Errorf("PTPTimeReceivers[0]: expected %s, got %s", testIf2New, src.PTPTimeReceivers[0])
	}
}
