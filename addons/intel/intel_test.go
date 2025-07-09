package intel

import (
	"os"
	"testing"

	ptpv1 "github.com/k8snetworkplumbingwg/ptp-operator/api/v1"
	"github.com/stretchr/testify/assert"
	"sigs.k8s.io/yaml"
)

func loadProfile(path string) (*ptpv1.PtpProfile, error) {
	profileData, err := os.ReadFile(path)
	if err != nil {
		return &ptpv1.PtpProfile{}, err
	}
	profile := ptpv1.PtpProfile{}
	err = yaml.Unmarshal(profileData, &profile)
	if err != nil {
		return &ptpv1.PtpProfile{}, err
	}
	return &profile, nil
}

func Test_initInternalDelays(t *testing.T) {
	delays, err := InitInternalDelays("E810-XXVDA4T")
	assert.NoError(t, err)
	assert.Equal(t, "E810-XXVDA4T", delays.PartType)
	assert.Len(t, delays.ExternalInputs, 3)
	assert.Len(t, delays.ExternalOutputs, 3)
}

func Test_initInternalDelays_BadPart(t *testing.T) {
	_, err := InitInternalDelays("Dummy")
	assert.Error(t, err)
}
func Test_ParseVpd(t *testing.T) {
	b, err := os.ReadFile("./testdata/vpd.bin")
	assert.NoError(t, err)
	vpd := ParseVpd(b)
	assert.Equal(t, "Intel(R) Ethernet Network Adapter E810-XXVDA4T", vpd.VendorSpecific1)
	assert.Equal(t, "2422", vpd.VendorSpecific2)
	assert.Equal(t, "M56954-005", vpd.PartNumber)
	assert.Equal(t, "507C6F1FB174", vpd.SerialNumber)
}

func Test_ProcessProfileTGMNew(t *testing.T) {
	unitTest = true
	profile, err := loadProfile("./testdata/profile-tgm.yaml")
	assert.NoError(t, err)
	err = OnPTPConfigChangeE810(nil, profile)
	assert.NoError(t, err)
}

func Test_ProcessProfilesTbcTtsc(t *testing.T) {
	unitTest = true
	for _, config := range []string{"./testdata/profile-tbc.yaml", "./testdata/profile-t-tsc.yaml"} {
		// Can read test profile
		profile, err := loadProfile(config)
		assert.NoError(t, err)

		// Can run PTP config change handler without errors
		err = OnPTPConfigChangeE810(nil, profile)
		assert.NoError(t, err)
		assert.Equal(t, ClockTypeTBC, clockChain.Type, "identified a wrong clock type")
		assert.Equal(t, "5799633565432596414", clockChain.LeadingNIC.DpllClockId, "identified a wrong clock ID ")
		assert.Equal(t, 9, len(clockChain.LeadingNIC.Pins), "wrong number of configurable pins")
		assert.Equal(t, "ens4f1", clockChain.LeadingNIC.UpstreamPort, "wrong upstream port")
		// Test holdover entry
		commands, err := clockChain.EnterHoldoverTBC()
		assert.NoError(t, err)
		assert.Equal(t, 2, len(*commands))
		// Test holdover exit
		commands, err = clockChain.EnterNormalTBC()
		assert.NoError(t, err)
		assert.Equal(t, 2, len(*commands))
		// Test error cases
		unitTest = false
		err = writeSysFs("/sys/0/dummy", "dummy")
		assert.Error(t, err)
		err = clockChain.GetLiveDpllPinsInfo()
		assert.Error(t, err)
		_, err = clockChain.SetPinsControl([]PinControl{
			{
				Label: "1",
				ParentControl: PinParentControl{
					EecPriority: PriorityDisabled,
					PpsPriority: PriorityDisabled,
				},
			}})
		assert.Error(t, err, "1 pin not found in the leading card")
		err = clockChain.EnableE810Outputs()
		assert.Error(t, err, "e810 failed to write 1 0 0 0 100 to /sys/class/net/ens4f0/device/ptp/ptp*/period")
		_, err = clockChain.InitPinsTBC()
		assert.Error(t, err, "failed to write...")
	}
}

func Test_ProcessProfileTGMOld(t *testing.T) {
	unitTest = true
	profile, err := loadProfile("./testdata/profile-tgm-old.yaml")
	assert.NoError(t, err)
	err = OnPTPConfigChangeE810(nil, profile)
	assert.NoError(t, err)
}
