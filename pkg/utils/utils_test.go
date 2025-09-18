package utils_test

import (
	"fmt"
	"os"
	"testing"

	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/testhelpers"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/utils"
	"github.com/stretchr/testify/assert"
)

func TestMain(m *testing.M) {
	teardownTests := testhelpers.SetupTests()
	defer teardownTests()
	os.Exit(m.Run())
}

type testCase struct {
	ifname        string
	expectedAlias string
}

func Test_GetAlias(t *testing.T) {
	testCases := []testCase{
		{"eth0", "ethx"},
		{"eth1.100", "ethx.100"},
		{"eth1.100.XYZ", "ethx.100.XYZ"},
		// Mellanox style naming
		{"enP2s2f0np0", "enP2s2fx"},
		{"enP1s1f1np1", "enP1s1fx"},
		{"enP10s5f3np2", "enP10s5fx"},
		{"ens1f3np3", "ens1fx"},
		// Mellanox style naming with VLAN
		{"enP2s2f0np0.100", "enP2s2fx.100"},
		{"enP1s1f1np1.200", "enP1s1fx.200"},
		{"enP10s5f3np2.300.XYZ", "enP10s5fx.300.XYZ"},
		// Already aliased interfaces (should return as-is)
		{"ens7fx", "ens7fx"},
		{"ethx", "ethx"},
		{"enP2s2fx", "enP2s2fx"},
		{"ens1fx.100", "ens1fx.100"},
		{"ens20f20.100", "ens20fx.100"},
		{"enP10s5fx.300.XYZ", "enP10s5fx.300.XYZ"},
		// Special master interface
		{"master", "master"},
		// Fallback cases (interfaces that don't match Intel or Mellanox format)
		{"wlan", "wlan"},
		{"lo", "lo"},
		{"virbr", "virbr"},
		{"docker", "docker"},
	}
	for _, tc := range testCases {
		t.Run(fmt.Sprintf("%s->%s", tc.ifname, tc.expectedAlias), func(t *testing.T) {
			assert.Equal(t, tc.expectedAlias, utils.GetAlias(tc.ifname))
		})
	}
}
