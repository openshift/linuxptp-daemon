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

func Test_ProcessProfile(t *testing.T) {
	profile, err := loadProfile("./testdata/profile-with-delays.yaml")
	assert.NoError(t, err)
	err = OnPTPConfigChangeE810(nil, profile)
	assert.NoError(t, err)
}
