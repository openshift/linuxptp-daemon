package alias_test

import (
	"os"
	"strings"
	"testing"

	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/alias"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/testhelpers"
	"github.com/stretchr/testify/assert"
)

func TestMain(m *testing.M) {
	teardownTests := testhelpers.SetupTests()
	defer teardownTests()
	os.Exit(m.Run())
}

type testCase struct {
	name            string
	input           map[string][]string
	expectedResults map[string]string
}

func Test_GetAlias(t *testing.T) {
	testCases := []testCase{
		{
			"Good single group single value",
			map[string][]string{"1": {"eth0"}},
			map[string]string{"eth0": "ethx"},
		},
		{
			"Good  single group multiple values",
			map[string][]string{"1": {"eth0", "eth1", "eth2"}},
			map[string]string{"eth0": "ethx", "eth1": "ethx", "eth2": "ethx"},
		},
		{
			"Good multiple groups multiple values",
			map[string][]string{
				"1": {"eth0", "eth1", "eth2"},
				"2": {"eno0", "eno1", "eno2"}},
			map[string]string{
				"eth0": "ethx", "eth1": "ethx", "eth2": "ethx",
				"eno0": "enox", "eno1": "enox", "eno2": "enox",
			},
		},
		{
			"Fallback single group multiple values",
			map[string][]string{
				"1": {"eth0", "eth1", "eth2", "eno1"},
			},
			map[string]string{
				"eth0": "eth0", "eth1": "eth1", "eth2": "eth2", "eno1": "eno1",
			},
		},
		{
			"Fallback conlliding groups multiple values",
			map[string][]string{
				"1": {"eno0", "eno1"},
				"2": {"eno2", "eno3"},
			},
			map[string]string{
				"eno0": "eno0", "eno1": "eno1",
				"eno2": "eno2", "eno3": "eno3",
			},
		},
		{
			"Fallback conlliding groups multiple values with a third uneffected",
			map[string][]string{
				"1": {"eno0", "eno1"},
				"2": {"eno2", "eno3"},
				"3": {"eth0", "eth1"},
			},
			map[string]string{
				"eno0": "eno0", "eno1": "eno1",
				"eno2": "eno2", "eno3": "eno3",
				"eth0": "ethx", "eth1": "ethx",
			},
		},
	}
	for _, tc := range testCases {
		alias.ClearAliases()
		t.Run(strings.ReplaceAll(tc.name, " ", "_"), func(t *testing.T) {
			for phc, ifNames := range tc.input {
				for _, ifname := range ifNames {
					alias.AddInterface(phc, ifname)
				}
			}
			alias.CalculateAliases()
			for ifName, expaectedAlias := range tc.expectedResults {
				assert.Equal(t, expaectedAlias, alias.GetAlias(ifName))
			}
		})
	}
}
