package pmc

import "github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/protocol"

// Client abstracts the PMC I/O layer so that callers in the event and daemon
// packages can be tested without spawning real pmc processes.
type Client interface {
	GetGMSettings(cfgName string) (protocol.GrandmasterSettings, error)
	SetGMSettings(cfgName string, g protocol.GrandmasterSettings) error
	GetParentDS(cfgName string) (protocol.ParentDataSet, error)
	GetParentTimeAndCurrentDS(cfgName string) (ParentTimeCurrentDS, error)
	SetExternalGMPropertiesNP(cfgName string, egp protocol.ExternalGrandmasterProperties) error
}

// defaultClient delegates every call to the real RunPMCExp* functions.
type defaultClient struct{}

func (defaultClient) GetGMSettings(cfgName string) (protocol.GrandmasterSettings, error) {
	return RunPMCExpGetGMSettings(cfgName)
}

func (defaultClient) GetParentDS(cfgName string) (protocol.ParentDataSet, error) {
	return RunPMCExpGetParentDS(cfgName, false)
}

func (defaultClient) SetGMSettings(cfgName string, g protocol.GrandmasterSettings) error {
	return RunPMCExpSetGMSettings(cfgName, g)
}

func (defaultClient) GetParentTimeAndCurrentDS(cfgName string) (ParentTimeCurrentDS, error) {
	return RunPMCExpGetParentTimeAndCurrentDataSets(cfgName)
}

func (defaultClient) SetExternalGMPropertiesNP(cfgName string, egp protocol.ExternalGrandmasterProperties) error {
	return RunPMCExpSetExternalGMPropertiesNP(cfgName, egp)
}

var activeClient Client = defaultClient{}

// SetMock replaces the package-level client with a test double.
// Call ResetMock (or defer it) when done.
func SetMock(c Client) { activeClient = c }

// ResetMock restores the real defaultClient.
func ResetMock() { activeClient = defaultClient{} }

// GetGMSettings retrieves the current GRANDMASTER_SETTINGS_NP from ptp4l.
func GetGMSettings(cfgName string) (protocol.GrandmasterSettings, error) {
	return activeClient.GetGMSettings(cfgName)
}

// SetGMSettings applies GRANDMASTER_SETTINGS_NP to ptp4l.
func SetGMSettings(cfgName string, g protocol.GrandmasterSettings) error {
	return activeClient.SetGMSettings(cfgName, g)
}

// GetParentDS retrieves the PARENT_DATA_SET from ptp4l.
func GetParentDS(cfgName string) (protocol.ParentDataSet, error) {
	return activeClient.GetParentDS(cfgName)
}

// GetParentTimeAndCurrentDS retrieves PARENT_DATA_SET, TIME_PROPERTIES_DS,
// and CURRENT_DATA_SET in a single PMC session.
func GetParentTimeAndCurrentDS(cfgName string) (ParentTimeCurrentDS, error) {
	return activeClient.GetParentTimeAndCurrentDS(cfgName)
}

// SetExternalGMPropertiesNP applies EXTERNAL_GRANDMASTER_PROPERTIES_NP to ptp4l.
func SetExternalGMPropertiesNP(cfgName string, egp protocol.ExternalGrandmasterProperties) error {
	return activeClient.SetExternalGMPropertiesNP(cfgName, egp)
}
