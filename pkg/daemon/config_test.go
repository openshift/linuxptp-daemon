package daemon

import (
	"testing"

	"github.com/openshift/linuxptp-daemon/pkg/event"
	"github.com/stretchr/testify/assert"
)

func TestPopulatePtp4lConf_ClockType_GM(t *testing.T) {
	tests := []struct {
		name         string
		config       string
		expectedType event.ClockType
	}{
		{
			name: "Global masterOnly with interface having no role config",
			config: `[global]
verbose 1
masterOnly 1

[eth0]
network_transport UDPv4`,
			expectedType: event.GM,
		},
		{
			name: "Interface with masterOnly 1 and interface with serverOnly 1",
			config: `[global]
verbose 1

[eth0]
masterOnly 1
network_transport UDPv4

[eth1]
serverOnly 1
network_transport UDPv4`,
			expectedType: event.GM,
		},
		{
			name: "Interface with masterOnly 1 and no global section",
			config: `[eth0]
masterOnly 1
network_transport UDPv4`,
			expectedType: event.GM,
		},
		{
			name: "Interface with both masterOnly 1 and slaveOnly 1",
			config: `[global]
verbose 1

[eth0]
masterOnly 1
slaveOnly 1
network_transport UDPv4`,
			expectedType: event.GM,
		},
		{
			name: "Interface with masterOnly 0 plus interfaces with masterOnly 1",
			config: `[global]
verbose 1

[eth0]
masterOnly 0
network_transport UDPv4

[eth1]
masterOnly 1
network_transport UDPv4

[eth2]
masterOnly 1
network_transport UDPv4`,
			expectedType: event.GM,
		},
		{
			name: "Interface with serverOnly 0 plus interfaces with masterOnly 1",
			config: `[global]
verbose 1

[eth0]
serverOnly 0
network_transport UDPv4

[eth1]
masterOnly 1
network_transport UDPv4

[eth2]
masterOnly 1
network_transport UDPv4`,
			expectedType: event.GM,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var conf ptp4lConf
			err := conf.populatePtp4lConf(&tt.config)
			assert.NoError(t, err, "populatePtp4lConf should not return error")
			assert.Equal(t, tt.expectedType, conf.clock_type, "clock_type should be %s", tt.expectedType)
		})
	}

	// Test GM alias equivalence: masterOnly = serverOnly
	aliasTests := []struct {
		name        string
		config1     string
		config2     string
		description string
	}{
		{
			name: "masterOnly 1 equivalent to serverOnly 1",
			config1: `[global]
verbose 1

[eth0]
masterOnly 1
network_transport UDPv4`,
			config2: `[global]
verbose 1

[eth0]
serverOnly 1
network_transport UDPv4`,
			description: "masterOnly and serverOnly should be equivalent",
		},
		{
			name: "Global masterOnly equivalent to global serverOnly",
			config1: `[global]
masterOnly 1

[eth0]
network_transport L2

[eth1]
network_transport L2`,
			config2: `[global]
serverOnly 1

[eth0]
network_transport L2

[eth1]
network_transport L2`,
			description: "Global master aliases should be equivalent",
		},
	}

	for _, tt := range aliasTests {
		t.Run(tt.name, func(t *testing.T) {
			// Test config1
			var conf1 ptp4lConf
			err1 := conf1.populatePtp4lConf(&tt.config1)
			assert.NoError(t, err1, "populatePtp4lConf should not return error for config1")
			assert.Equal(t, event.GM, conf1.clock_type, "Config1 should produce GM: %s", tt.description)

			// Test config2 (with aliases)
			var conf2 ptp4lConf
			err2 := conf2.populatePtp4lConf(&tt.config2)
			assert.NoError(t, err2, "populatePtp4lConf should not return error for config2")
			assert.Equal(t, event.GM, conf2.clock_type, "Config2 should produce GM: %s", tt.description)

			// Most importantly, both configs should produce the same result
			assert.Equal(t, conf1.clock_type, conf2.clock_type, "Both configs should produce GM: %s", tt.description)
		})
	}
}

func TestPopulatePtp4lConf_ClockType_BC(t *testing.T) {
	tests := []struct {
		name         string
		config       string
		expectedType event.ClockType
	}{
		{
			name: "Two interfaces with masterOnly 1 and one interface with slaveOnly 1",
			config: `[global]
verbose 1

[eth0]
masterOnly 1
network_transport UDPv4

[eth1]
slaveOnly 1
network_transport UDPv4

[eth2]
masterOnly 1
network_transport UDPv4`,
			expectedType: event.BC,
		},
		{
			name: "Two interfaces with slaveOnly 1 and one interface with masterOnly 1",
			config: `[global]
verbose 1

[eth0]
slaveOnly 1
network_transport UDPv4

[eth1]
slaveOnly 1
network_transport UDPv4

[eth2]
masterOnly 1
network_transport UDPv4`,
			expectedType: event.BC,
		},
		{
			name: "Interface with clientOnly 1 and two interfaces with masterOnly 1",
			config: `[global]
verbose 1

[eth0]
clientOnly 1
network_transport UDPv4

[eth1]
masterOnly 1
network_transport UDPv4

[eth2]
masterOnly 1
network_transport UDPv4`,
			expectedType: event.BC,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var conf ptp4lConf
			err := conf.populatePtp4lConf(&tt.config)
			assert.NoError(t, err, "populatePtp4lConf should not return error")
			assert.Equal(t, tt.expectedType, conf.clock_type, "clock_type should be %s", tt.expectedType)
		})
	}

	// Test BC alias equivalence: masterOnly = serverOnly, slaveOnly = clientOnly
	aliasTests := []struct {
		name        string
		config1     string
		config2     string
		description string
	}{
		{
			name: "Mixed aliases in BC configuration",
			config1: `[global]
verbose 1

[eth0]
masterOnly 1
network_transport UDPv4

[eth1]
slaveOnly 1
network_transport UDPv4`,
			config2: `[global]
verbose 1

[eth0]
serverOnly 1
network_transport UDPv4

[eth1]
clientOnly 1
network_transport UDPv4`,
			description: "Mixed master/slave aliases should produce BC",
		},
	}

	for _, tt := range aliasTests {
		t.Run(tt.name, func(t *testing.T) {
			// Test config1
			var conf1 ptp4lConf
			err1 := conf1.populatePtp4lConf(&tt.config1)
			assert.NoError(t, err1, "populatePtp4lConf should not return error for config1")
			assert.Equal(t, event.BC, conf1.clock_type, "Config1 should produce BC: %s", tt.description)

			// Test config2 (with aliases)
			var conf2 ptp4lConf
			err2 := conf2.populatePtp4lConf(&tt.config2)
			assert.NoError(t, err2, "populatePtp4lConf should not return error for config2")
			assert.Equal(t, event.BC, conf2.clock_type, "Config2 should produce BC: %s", tt.description)

			// Most importantly, both configs should produce the same result
			assert.Equal(t, conf1.clock_type, conf2.clock_type, "Both configs should produce BC: %s", tt.description)
		})
	}
}

func TestPopulatePtp4lConf_ClockType_OC(t *testing.T) {
	tests := []struct {
		name         string
		config       string
		expectedType event.ClockType
	}{
		{
			name: "Single interface with slaveOnly 1",
			config: `[global]
verbose 1

[eth0]
slaveOnly 1
network_transport UDPv4`,
			expectedType: event.OC,
		},
		{
			name: "Single interface with clientOnly 1",
			config: `[global]
verbose 1

[eth0]
clientOnly 1
network_transport UDPv4`,
			expectedType: event.OC,
		},
		{
			name: "Single interface with masterOnly 0",
			config: `[global]
verbose 1

[eth0]
masterOnly 0
network_transport UDPv4`,
			expectedType: event.OC,
		},
		{
			name: "Single interface with serverOnly 0",
			config: `[global]
verbose 1

[eth0]
serverOnly 0
network_transport UDPv4`,
			expectedType: event.OC,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var conf ptp4lConf
			err := conf.populatePtp4lConf(&tt.config)
			assert.NoError(t, err, "populatePtp4lConf should not return error")
			assert.Equal(t, tt.expectedType, conf.clock_type, "clock_type should be %s", tt.expectedType)
		})
	}

	// Test OC alias equivalence: masterOnly = serverOnly, slaveOnly = clientOnly
	aliasTests := []struct {
		name        string
		config1     string
		config2     string
		description string
	}{
		{
			name: "slaveOnly 1 equivalent to clientOnly 1",
			config1: `[global]
verbose 1

[eth0]
slaveOnly 1
network_transport UDPv4`,
			config2: `[global]
verbose 1

[eth0]
clientOnly 1
network_transport UDPv4`,
			description: "slaveOnly and clientOnly should be equivalent",
		},
		{
			name: "masterOnly 0 equivalent to serverOnly 0",
			config1: `[global]
verbose 1

[eth0]
masterOnly 0
network_transport UDPv4`,
			config2: `[global]
verbose 1

[eth0]
serverOnly 0
network_transport UDPv4`,
			description: "masterOnly=0 and serverOnly=0 should be equivalent",
		},
		{
			name: "Global slaveOnly equivalent to global clientOnly",
			config1: `[global]
slaveOnly 1

[eth0]
network_transport L2

[eth1]
network_transport L2`,
			config2: `[global]
clientOnly 1

[eth0]
network_transport L2

[eth1]
network_transport L2`,
			description: "Global slave aliases should be equivalent",
		},
	}

	for _, tt := range aliasTests {
		t.Run(tt.name, func(t *testing.T) {
			// Test config1
			var conf1 ptp4lConf
			err1 := conf1.populatePtp4lConf(&tt.config1)
			assert.NoError(t, err1, "populatePtp4lConf should not return error for config1")
			assert.Equal(t, event.OC, conf1.clock_type, "Config1 should produce OC: %s", tt.description)

			// Test config2 (with aliases)
			var conf2 ptp4lConf
			err2 := conf2.populatePtp4lConf(&tt.config2)
			assert.NoError(t, err2, "populatePtp4lConf should not return error for config2")
			assert.Equal(t, event.OC, conf2.clock_type, "Config2 should produce OC: %s", tt.description)

			// Most importantly, both configs should produce the same result
			assert.Equal(t, conf1.clock_type, conf2.clock_type, "Both configs should produce OC: %s", tt.description)
		})
	}
}

func TestPopulatePtp4lConf_GlobalOverrides(t *testing.T) {
	// Test cases that focus on global defaults being overridden by interface-specific settings
	tests := []struct {
		name         string
		config       string
		expectedType event.ClockType
	}{
		{
			name: "Global slaveOnly 1 with interface having masterOnly 1 override",
			config: `[global]
slaveOnly 1

[eth0]
masterOnly 1

[eth1]
network_transport L2`,
			expectedType: event.BC,
		},
		{
			name: "Global masterOnly 1 with interface having slaveOnly 1 override",
			config: `[global]
masterOnly 1

[eth0]
slaveOnly 1

[eth1]
network_transport L2`,
			expectedType: event.BC,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var conf ptp4lConf
			err := conf.populatePtp4lConf(&tt.config)
			assert.NoError(t, err, "populatePtp4lConf should not return error")
			assert.Equal(t, tt.expectedType, conf.clock_type,
				"Expected %s for config with global override", tt.expectedType)
		})
	}
}
