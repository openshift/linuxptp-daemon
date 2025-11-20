package intel

import (
	"slices"
	"testing"

	ptpv1 "github.com/k8snetworkplumbingwg/ptp-operator/api/v1"
	"github.com/stretchr/testify/assert"
)

func Test_E810(t *testing.T) {
	p, d := E810("e810")
	assert.NotNil(t, p)
	assert.NotNil(t, d)

	p, d = E810("not_e810")
	assert.Nil(t, p)
	assert.Nil(t, d)
}

func Test_AfterRunPTPCommandE810(t *testing.T) {
	unitTest = true
	profile, err := loadProfile("./testdata/profile-tgm.yaml")
	assert.NoError(t, err)
	p, d := E810("e810")
	data := (*d).(*E810PluginData)

	err = p.AfterRunPTPCommand(d, profile, "bad command")
	assert.NoError(t, err)

	mockExec, execRestore := setupExecMock()
	defer execRestore()
	mockExec.setDefaults("output", nil)
	err = p.AfterRunPTPCommand(d, profile, "gpspipe")
	assert.NoError(t, err)
	// Ensure all 9 required calls are the last 9:
	requiredUblxCmds := []string{
		"CFG-MSG,1,34,1",
		"CFG-MSG,1,3,1",
		"CFG-MSG,0xf0,0x02,0",
		"CFG-MSG,0xf0,0x03,0",
		"CFG-MSGOUT-NMEA_ID_VTG_I2C,0",
		"CFG-MSGOUT-NMEA_ID_GST_I2C,0",
		"CFG-MSGOUT-NMEA_ID_ZDA_I2C,0",
		"CFG-MSGOUT-NMEA_ID_GBS_I2C,0",
		"SAVE",
	}
	found := make([]string, 0, len(requiredUblxCmds))
	for _, call := range mockExec.actualCalls {
		if slices.Contains(requiredUblxCmds, call.args[1]) {
			found = append(found, call.args[1])
		}
	}
	assert.Equal(t, requiredUblxCmds, found)
	// And expect 3 of them to have produced output (as specified in the profile)
	assert.Equal(t, 3, len(*data.hwplugins))
}

func Test_PopulateHwConfdigE810(t *testing.T) {
	p, d := E810("e810")
	data := (*d).(*E810PluginData)
	err := p.PopulateHwConfig(d, nil)
	assert.NoError(t, err)

	output := []ptpv1.HwConfig{}
	err = p.PopulateHwConfig(d, &output)
	assert.NoError(t, err)
	assert.Equal(t, 0, len(output))

	data.hwplugins = &[]string{"A", "B", "C"}
	err = p.PopulateHwConfig(d, &output)
	assert.NoError(t, err)
	assert.Equal(t, []ptpv1.HwConfig{
		{
			DeviceID: "e810",
			Status:   "A",
		},
		{
			DeviceID: "e810",
			Status:   "B",
		},
		{
			DeviceID: "e810",
			Status:   "C",
		},
	},
		output)
}
