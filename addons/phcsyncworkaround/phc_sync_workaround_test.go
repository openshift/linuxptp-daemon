package phcsyncworkaround

import (
	"strings"
	"testing"
	"time"

	ptpv1 "github.com/k8snetworkplumbingwg/ptp-operator/api/v1"
	apiextensions "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
)

func TestMeasurementTimeout(t *testing.T) {
	tests := []struct {
		name    string
		profile *ptpv1.PtpProfile
		want    time.Duration
		wantErr bool
	}{
		{name: "nil profile", profile: nil, want: 0},
		{name: "no plugins", profile: &ptpv1.PtpProfile{}, want: 0},
		{name: "plugin not configured", profile: profileWithPlugin(`{}`), want: 0},
		{name: "configured", profile: profileWithPlugin(`{"timeout":"15s"}`), want: 15 * time.Second},
		{name: "empty string", profile: profileWithPlugin(`{"timeout":""}`), want: 0},
		{name: "invalid duration", profile: profileWithPlugin(`{"timeout":"nope"}`), wantErr: true},
		{name: "negative duration", profile: profileWithPlugin(`{"timeout":"-1s"}`), wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := measurementTimeout(test.profile)
			if test.wantErr {
				if err == nil {
					t.Fatalf("measurementTimeout() expected error, got %v", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("measurementTimeout() error: %v", err)
			}
			if got != test.want {
				t.Fatalf("measurementTimeout() = %v, want %v", got, test.want)
			}
		})
	}
}

func profileWithPlugin(options string) *ptpv1.PtpProfile {
	return &ptpv1.PtpProfile{
		Plugins: map[string]*apiextensions.JSON{
			pluginName: {Raw: []byte(options)},
		},
	}
}

func TestNewRegistersPlugin(t *testing.T) {
	plugin, data := New(pluginName)
	if plugin == nil || data == nil {
		t.Fatal("expected PHC sync workaround plugin registration")
	}
	if plugin.Name != pluginName || plugin.OnPTPConfigChange == nil {
		t.Fatalf("unexpected plugin registration: %+v", plugin)
	}
}

func TestNewRejectsWrongName(t *testing.T) {
	plugin, data := New("wrong-name")
	if plugin != nil || data != nil {
		t.Fatal("expected wrong plugin name to be rejected")
	}
}

func TestOnPTPConfigChangeIgnoresProfileWithoutTimeReceiverPort(t *testing.T) {
	start := time.Now()
	if err := onPTPConfigChange(nil, profileWithoutTRPort()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 100*time.Millisecond {
		t.Fatalf("non-TR profile was blocked for %s", elapsed)
	}
}

func TestTimeReceiverInterfaces(t *testing.T) {
	tests := []struct {
		name       string
		config     string
		wantIfaces []string
	}{
		{name: "interface masterOnly zero", config: "[global]\nmasterOnly 1\n[ens4f0]\nmasterOnly 0\n", wantIfaces: []string{"ens4f0"}},
		{name: "only global masterOnly zero", config: "[global]\nmasterOnly 0\n"},
		{name: "interface master only one", config: "[ens4f0]\nmasterOnly 1\n"},
		{name: "multiple time receiver ports", config: "[ens4f0]\nmasterOnly 0\n[ens5f0]\nmasterOnly 0\n", wantIfaces: []string{"ens4f0", "ens5f0"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			profile := &ptpv1.PtpProfile{Ptp4lConf: &test.config}
			got := timeReceiverInterfaces(profile)
			if len(got) != len(test.wantIfaces) {
				t.Fatalf("timeReceiverInterfaces() = %v, want %v", got, test.wantIfaces)
			}
			for i := range got {
				if got[i] != test.wantIfaces[i] {
					t.Fatalf("timeReceiverInterfaces() = %v, want %v", got, test.wantIfaces)
				}
			}
		})
	}
}

func TestRenderMeasurementConfigPreservesProfileOptions(t *testing.T) {
	config := "[eno8303]\nmasterOnly 0\n[global]\nslaveOnly 0\nfree_running 0\ndomainNumber 24\ndataset_comparison G.8275.x\nG.8275.defaultDS.localPriority 128\nG.8275.portDS.localPriority 128\nptp_dst_mac 01:1B:19:00:00:00\np2p_dst_mac 01:80:C2:00:00:0E\nnetwork_transport L2\nuds_address /old/socket\n"
	profile := &ptpv1.PtpProfile{Ptp4lConf: &config}
	got, err := renderMeasurementConfig(profile, []string{"eno8303"})
	if err != nil {
		t.Fatalf("renderMeasurementConfig() error: %v", err)
	}
	for _, want := range []string{
		"slaveOnly 1",
		"free_running 1",
		"domainNumber 24",
		"dataset_comparison G.8275.x",
		"G.8275.defaultDS.localPriority 128",
		"G.8275.portDS.localPriority 128",
		"ptp_dst_mac 01:1B:19:00:00:00",
		"network_transport L2",
		"[eno8303]",
		"masterOnly 0",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("rendered config missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "uds_address /old/socket") {
		t.Errorf("rendered config retained the old UDS address:\n%s", got)
	}
}

func TestRenderMeasurementConfigMultipleInterfaces(t *testing.T) {
	config := "[eno8303]\nmasterOnly 0\n[eno8403]\nmasterOnly 0\n[global]\ndomainNumber 24\nnetwork_transport L2\n"
	profile := &ptpv1.PtpProfile{Ptp4lConf: &config}
	got, err := renderMeasurementConfig(profile, []string{"eno8403", "eno8303"})
	if err != nil {
		t.Fatalf("renderMeasurementConfig() error: %v", err)
	}
	for _, want := range []string{"[eno8403]", "[eno8303]", "masterOnly 0"} {
		if !strings.Contains(got, want) {
			t.Errorf("rendered config missing %q:\n%s", want, got)
		}
	}
	if strings.Count(got, "masterOnly 0") != 2 {
		t.Errorf("expected both TR interface sections:\n%s", got)
	}
}

func TestParseSample(t *testing.T) {
	tests := []struct {
		name   string
		line   string
		offset int64
		ok     bool
	}{
		{
			name:   "zero offset with non-zero path delay is valid",
			line:   "ptp4l[544.425]: master offset          0 s0 freq     +48 path delay        80",
			offset: 0,
			ok:     true,
		},
		{
			name: "zero path delay is invalid",
			line: "ptp4l[544.487]: master offset          0 s0 freq     -16 path delay         0",
		},
		{
			name: "signed zero path delay is invalid",
			line: "ptp4l[544.487]: master offset          0 s0 freq     -16 path delay        +0",
		},
		{
			name:   "negative offset is valid",
			line:   "ptp4l[1.0]: master offset -37000472295 s2 freq      +0 path delay       137",
			offset: -37000472295,
			ok:     true,
		},
		{
			name:   "multi-port line with interface prefix",
			line:   "ptp4l[6480.421]: [ptp4l.1.config:6] eno8303 master offset -19 s0 freq +1 path delay 83",
			offset: -19,
			ok:     true,
		},
		{
			name: "no path delay is invalid",
			line: "ptp4l[17062.940]: port 1 (eno8303): LISTENING to UNCALIBRATED on RS_SLAVE",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			offset, ok := parseSample(test.line)
			if ok != test.ok {
				t.Fatalf("parseSample() ok = %v, want %v", ok, test.ok)
			}
			if ok && offset != test.offset {
				t.Fatalf("parseSample() offset = %d, want %d", offset, test.offset)
			}
		})
	}
}

func TestPHCTimeCorrection(t *testing.T) {
	tests := []struct {
		name   string
		phc    int64
		offset int64
		want   int64
	}{
		{name: "positive offset", phc: 1_788_982_998_578_115_938, offset: 318_984_375_136, want: 1_788_982_679_593_740_802},
		{name: "negative offset", phc: 1_788_982_998_578_115_938, offset: -318_984_375_136, want: 1_788_983_317_562_491_074},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := correctedTime(test.phc, test.offset)
			if err != nil {
				t.Fatalf("correctedTime() error: %v", err)
			}
			if got != test.want {
				t.Fatalf("correctedTime() = %d, want %d", got, test.want)
			}
		})
	}
}

func TestPHCSecondsToNS(t *testing.T) {
	got, err := phcSecondsToNS("1788982998.578115938")
	if err != nil {
		t.Fatalf("phcSecondsToNS() error: %v", err)
	}
	if got != 1_788_982_998_578_115_938 {
		t.Fatalf("phcSecondsToNS() = %d", got)
	}
}

func profileWithoutTRPort() *ptpv1.PtpProfile {
	config := "[ens4f0]\nmasterOnly 1\n"
	return &ptpv1.PtpProfile{Ptp4lConf: &config}
}
