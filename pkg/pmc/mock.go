package pmc

import (
	"sync"

	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/protocol"
)

// GetCall records a single getter invocation on the MockClient.
type GetCall struct {
	Method  string
	CfgName string
}

// SetCall records a single setter invocation on the MockClient, including
// the full payload that would have been applied to ptp4l.
type SetCall struct {
	Method                 string
	CfgName                string
	GMSettings             *protocol.GrandmasterSettings
	ExternalGMPropertiesNP *protocol.ExternalGrandmasterProperties
}

// MockClient is a spy that records every PMC call along with its full
// payload. All methods are safe for concurrent use. Tests should read
// recorded calls via SnapshotGetCalls / SnapshotSetCalls.
type MockClient struct {
	mu       sync.Mutex
	getCalls []GetCall
	setCalls []SetCall

	// Canned return values for getters. Tests set these before exercising
	// the code under test.
	GMSettingsResult          protocol.GrandmasterSettings
	GMSettingsErr             error
	ParentDSResult            protocol.ParentDataSet
	ParentDSErr               error
	ParentTimeCurrentDSResult ParentTimeCurrentDS
	ParentTimeCurrentDSErr    error

	// Canned errors for setters (nil = success).
	SetGMSettingsErr             error
	SetExternalGMPropertiesNPErr error
}

var _ Client = (*MockClient)(nil)

// SnapshotGetCalls returns a copy of the recorded getter calls.
func (m *MockClient) SnapshotGetCalls() []GetCall {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]GetCall, len(m.getCalls))
	copy(out, m.getCalls)
	return out
}

// SnapshotSetCalls returns a copy of the recorded setter calls.
func (m *MockClient) SnapshotSetCalls() []SetCall {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]SetCall, len(m.setCalls))
	copy(out, m.setCalls)
	return out
}

// GetGMSettings implements Client.
func (m *MockClient) GetGMSettings(cfgName string) (protocol.GrandmasterSettings, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.getCalls = append(m.getCalls, GetCall{Method: "GetGMSettings", CfgName: cfgName})
	return m.GMSettingsResult, m.GMSettingsErr
}

// SetGMSettings implements Client.
func (m *MockClient) SetGMSettings(cfgName string, g protocol.GrandmasterSettings) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.setCalls = append(m.setCalls, SetCall{
		Method:     "SetGMSettings",
		CfgName:    cfgName,
		GMSettings: &g,
	})
	return m.SetGMSettingsErr
}

// GetParentDS implements Client.
func (m *MockClient) GetParentDS(cfgName string) (protocol.ParentDataSet, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.getCalls = append(m.getCalls, GetCall{Method: "GetParentDS", CfgName: cfgName})
	return m.ParentDSResult, m.ParentDSErr
}

// GetParentTimeAndCurrentDS implements Client.
func (m *MockClient) GetParentTimeAndCurrentDS(cfgName string) (ParentTimeCurrentDS, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.getCalls = append(m.getCalls, GetCall{Method: "GetParentTimeAndCurrentDS", CfgName: cfgName})
	return m.ParentTimeCurrentDSResult, m.ParentTimeCurrentDSErr
}

// SetExternalGMPropertiesNP implements Client.
func (m *MockClient) SetExternalGMPropertiesNP(cfgName string, egp protocol.ExternalGrandmasterProperties) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.setCalls = append(m.setCalls, SetCall{
		Method:                 "SetExternalGMPropertiesNP",
		CfgName:                cfgName,
		ExternalGMPropertiesNP: &egp,
	})
	return m.SetExternalGMPropertiesNPErr
}
