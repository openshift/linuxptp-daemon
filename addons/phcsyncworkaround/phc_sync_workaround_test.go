package phcsyncworkaround

import (
	"strings"
	"testing"
	"time"

	ptpv1 "github.com/k8snetworkplumbingwg/ptp-operator/api/v1"
)

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

func TestTimeReceiverInterface(t *testing.T) {
	tests := []struct {
		name      string
		config    string
		wantIface string
		wantErr   bool
	}{
		{name: "interface masterOnly zero", config: "[global]\nmasterOnly 1\n[ens4f0]\nmasterOnly 0\n", wantIface: "ens4f0"},
		{name: "only global masterOnly zero", config: "[global]\nmasterOnly 0\n"},
		{name: "interface master only one", config: "[ens4f0]\nmasterOnly 1\n"},
		{name: "multiple time receiver ports", config: "[ens4f0]\nmasterOnly 0\n[ens5f0]\nmasterOnly 0\n", wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			profile := &ptpv1.PtpProfile{Ptp4lConf: &test.config}
			got, err := timeReceiverInterface(profile)
			if test.wantErr {
				if err == nil {
					t.Fatalf("timeReceiverInterface() expected error, got %q", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("timeReceiverInterface() error: %v", err)
			}
			if got != test.wantIface {
				t.Fatalf("timeReceiverInterface() = %q, want %q", got, test.wantIface)
			}
		})
	}
}

func TestRenderMeasurementConfigPreservesProfileOptions(t *testing.T) {
	config := "[eno8303]\nmasterOnly 0\n[global]\nslaveOnly 0\nfree_running 0\ndomainNumber 24\ndataset_comparison G.8275.x\nG.8275.defaultDS.localPriority 128\nG.8275.portDS.localPriority 128\nptp_dst_mac 01:1B:19:00:00:00\np2p_dst_mac 01:80:C2:00:00:0E\nnetwork_transport L2\nuds_address /old/socket\n"
	profile := &ptpv1.PtpProfile{Ptp4lConf: &config}
	got, err := renderMeasurementConfig(profile, "eno8303")
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
