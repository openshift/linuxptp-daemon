package intel

import (
	"errors"
	"fmt"
	"os"
	"slices"
	"strconv"
	"time"

	"github.com/golang/glog"
	dpll "github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/dpll-netlink"

	ptpv1 "github.com/k8snetworkplumbingwg/ptp-operator/api/v1"
)

// FileSystemInterface defines the interface for filesystem operations to enable mocking
type FileSystemInterface interface {
	ReadDir(dirname string) ([]os.DirEntry, error)
	WriteFile(filename string, data []byte, perm os.FileMode) error
}

// RealFileSystem implements FileSystemInterface using real OS operations
type RealFileSystem struct{}

// ReadDir reads the contents of the directory specified by dirname
func (fs *RealFileSystem) ReadDir(dirname string) ([]os.DirEntry, error) {
	return os.ReadDir(dirname)
}

// WriteFile writes the data to the file specified by filename
func (fs *RealFileSystem) WriteFile(filename string, data []byte, perm os.FileMode) error {
	return os.WriteFile(filename, data, perm)
}

// Default filesystem implementation
var filesystem FileSystemInterface = &RealFileSystem{}

type ClockChainType int
type ClockChain struct {
	Type       ClockChainType  `json:"clockChainType"`
	LeadingNIC CardInfo        `json:"leadingNIC"`
	OtherNICs  []CardInfo      `json:"otherNICs,omitempty"`
	DpllPins   []*dpll.PinInfo `json:"dpllPins"`
}
type CardInfo struct {
	Name        string `json:"name"`
	DpllClockID string `json:"dpllClockId"`
	// upstreamPort specifies the slave port in the T-BC case. For example, if the "name"
	// 	is ens4f0, the "upstreamPort" could be ens4f1, depending on ptp4l config
	UpstreamPort string                  `json:"upstreamPort"`
	Pins         map[string]dpll.PinInfo `json:"pins"`
}

const (
	ClockTypeUnset ClockChainType = iota
	ClockTypeTGM
	ClockTypeTBC
	PriorityEnabled  = 0
	PriorityDisabled = 255
)

var ClockTypesMap = map[string]ClockChainType{
	"":     ClockTypeUnset,
	"T-GM": ClockTypeTGM,
	"T-BC": ClockTypeTBC, // Use the same for T-TSC
}

const (
	sdp20          = "CVL-SDP20"
	sdp21          = "CVL-SDP21"
	sdp22          = "CVL-SDP22"
	sdp23          = "CVL-SDP23"
	gnss           = "GNSS-1PPS"
	sma1Input      = "SMA1"
	sma2Input      = "SMA2/U.FL2"
	c8270Rclka     = "C827_0-RCLKA"
	c8270Rclkb     = "C827_0-RCLKB"
	eecDpllIndex   = 0
	ppsDpllIndex   = 1
	sdp22PpsEnable = "2 0 0 1 0"
	sdp20PpsEnable = "1 0 0 1 0"
	sma2           = "SMA2"
)

type PinParentControl struct {
	EecPriority    uint8
	PpsPriority    uint8
	EecOutputState uint8
	PpsOutputState uint8
}
type PinControl struct {
	Label         string
	ParentControl PinParentControl
}

var configurablePins = []string{sdp20, sdp21, sdp22, sdp23, gnss, sma1Input, sma2Input, c8270Rclka, c8270Rclkb}

func (c *ClockChain) getLiveDpllPinsInfo() error {
	if !unitTest {
		conn, err := dpll.Dial(nil)
		if err != nil {
			return fmt.Errorf("failed to dial DPLL: %v", err)
		}
		//nolint:errcheck
		defer conn.Close()
		c.DpllPins, err = conn.DumpPinGet()
		if err != nil {
			return fmt.Errorf("failed to dump DPLL pins: %v", err)
		}
	} else {
		c.DpllPins = DpllPins
	}
	return nil
}

func (c *ClockChain) resolveInterconnections(e810Opts E810Opts, nodeProfile *ptpv1.PtpProfile) (*[]delayCompensation, error) {
	compensations := []delayCompensation{}
	var clockID *string
	for _, card := range e810Opts.PhaseInputs {
		delays, err := InitInternalDelays(card.Part)
		if err != nil {
			return nil, err
		}
		glog.Infof("card: %+v", card)
		if !card.GnssInput && card.UpstreamPort == "" {
			externalDelay := card.Input.DelayPs
			connector := card.Input.Connector
			link := findInternalLink(delays.ExternalInputs, connector)
			if link == nil {
				return nil, fmt.Errorf("plugin E810 error: can't find connector %s in the card %s spec", connector, card.Part)
			}
			var pinLabel string
			var internalDelay int32

			pinLabel = link.Pin
			internalDelay = link.DelayPs
			clockID, err = addClockID(card.ID, nodeProfile)
			if err != nil {
				return nil, err
			}

			compensations = append(compensations, delayCompensation{
				DelayPs:   int32(externalDelay) + internalDelay,
				pinLabel:  pinLabel,
				iface:     card.ID,
				direction: "input",
				clockID:   *clockID,
			})
			// Track non-leading NICs
			c.OtherNICs = append(c.OtherNICs, CardInfo{
				Name:         card.ID,
				DpllClockID:  *clockID,
				UpstreamPort: card.UpstreamPort,
				Pins:         make(map[string]dpll.PinInfo, 0),
			})
		} else {
			c.LeadingNIC.Name = card.ID
			c.LeadingNIC.UpstreamPort = card.UpstreamPort
			clockID, err = addClockID(card.ID, nodeProfile)
			if err != nil {
				return nil, err
			}
			c.LeadingNIC.DpllClockID = *clockID
			if card.GnssInput {
				c.Type = ClockTypeTGM
				gnssLink := &delays.GnssInput
				compensations = append(compensations, delayCompensation{
					DelayPs:   gnssLink.DelayPs,
					pinLabel:  gnssLink.Pin,
					iface:     card.ID,
					direction: "input",
					clockID:   *clockID,
				})
			} else {
				// if no GNSS and no external, then ptp4l input
				c.Type = ClockTypeTBC
			}
		}
		for _, outputConn := range card.PhaseOutputConnectors {
			link := findInternalLink(delays.ExternalOutputs, outputConn)
			if link == nil {
				return nil, fmt.Errorf("plugin E810 error: can't find connector %s in the card %s spec", outputConn, card.Part)
			}
			clockID, err = addClockID(card.ID, nodeProfile)
			if err != nil {
				return nil, err
			}
			compensations = append(compensations, delayCompensation{
				DelayPs:   link.DelayPs,
				pinLabel:  link.Pin,
				iface:     card.ID,
				direction: "output",
				clockID:   *clockID,
			})
		}
	}
	return &compensations, nil
}

func InitClockChain(e810Opts E810Opts, nodeProfile *ptpv1.PtpProfile) (*ClockChain, error) {
	var chain = &ClockChain{
		LeadingNIC: CardInfo{
			Pins: make(map[string]dpll.PinInfo, 0),
		},
		OtherNICs: make([]CardInfo, 0),
	}

	err := chain.getLiveDpllPinsInfo()
	if err != nil {
		return chain, err
	}

	comps, err := chain.resolveInterconnections(e810Opts, nodeProfile)
	if err != nil {
		glog.Errorf("fail to get delay compensations, %s", err)
	}
	if !unitTest {
		err = sendDelayCompensation(comps, chain.DpllPins)
		if err != nil {
			glog.Errorf("fail to send delay compensations, %s", err)
		}
	}
	err = chain.getLeadingCardSDP()
	if err != nil {
		return chain, err
	}
	// Populate pins for other NICs, if any
	err = chain.getOtherCardsSDP()
	if err != nil {
		return chain, err
	}
	glog.Info("about to set DPLL pin priorities to defaults")
	_, err = chain.SetPinDefaults()
	if err != nil {
		return chain, err
	}
	if chain.Type == ClockTypeTBC {
		(*nodeProfile).PtpSettings["clockType"] = "T-BC"
		glog.Info("about to init TBC pins")
		_, err = chain.InitPinsTBC()
		if err != nil {
			return chain, fmt.Errorf("failed to initialize pins for T-BC operation: %s", err.Error())
		}
		glog.Info("about to enter TBC Normal mode")
		_, err = chain.EnterNormalTBC()
		if err != nil {
			return chain, fmt.Errorf("failed to enter T-BC normal mode: %s", err.Error())
		}
	}
	return chain, err
}

func (c *ClockChain) getLeadingCardSDP() error {
	clockID, err := strconv.ParseUint(c.LeadingNIC.DpllClockID, 10, 64)
	if err != nil {
		return err
	}
	for _, pin := range c.DpllPins {
		if pin.ClockID == clockID && slices.Contains(configurablePins, pin.BoardLabel) {
			c.LeadingNIC.Pins[pin.BoardLabel] = *pin
		}
	}
	return nil
}

// getOtherCardsSDP populates configurable pins for all non-leading NICs
func (c *ClockChain) getOtherCardsSDP() error {
	if len(c.OtherNICs) == 0 {
		return nil
	}
	for idx := range c.OtherNICs {
		clockID, err := strconv.ParseUint(c.OtherNICs[idx].DpllClockID, 10, 64)
		if err != nil {
			return err
		}
		for _, pin := range c.DpllPins {
			if pin.ClockID == clockID && slices.Contains(configurablePins, pin.BoardLabel) {
				if c.OtherNICs[idx].Pins == nil {
					c.OtherNICs[idx].Pins = make(map[string]dpll.PinInfo, 0)
				}
				c.OtherNICs[idx].Pins[pin.BoardLabel] = *pin
			}
		}
	}
	return nil
}

func writeSysFs(path string, val string) error {
	glog.Infof("writing " + val + " to " + path)
	err := filesystem.WriteFile(path, []byte(val), 0666)
	if err != nil {
		return fmt.Errorf("e810 failed to write " + val + " to " + path + ": " + err.Error())
	}
	return nil
}

func (c *ClockChain) SetPinsControl(pins []PinControl) (*[]dpll.PinParentDeviceCtl, error) {
	pinCommands := []dpll.PinParentDeviceCtl{}
	for _, pinCtl := range pins {
		dpllPin, found := c.LeadingNIC.Pins[pinCtl.Label]
		if !found {
			glog.Errorf("%s pin not found in the card", pinCtl.Label)
			continue
		}
		pinCommand := SetPinControlData(dpllPin, pinCtl.ParentControl)
		pinCommands = append(pinCommands, *pinCommand)
	}
	return &pinCommands, nil
}

// SetPinsControlForAllNICs sets pins across all NICs (leading + other NICs)
// This is used specifically for initialization functions like SetPinDefaults
func (c *ClockChain) SetPinsControlForAllNICs(pins []PinControl) (*[]dpll.PinParentDeviceCtl, error) {
	pinCommands := []dpll.PinParentDeviceCtl{}

	for _, pinCtl := range pins {
		// Search for the pin across all NICs
		found := false

		// Check leading NIC first
		if pin, exists := c.LeadingNIC.Pins[pinCtl.Label]; exists {
			pinCommand := SetPinControlData(pin, pinCtl.ParentControl)
			pinCommands = append(pinCommands, *pinCommand)
			found = true
		}

		// Check all other NICs
		for _, nic := range c.OtherNICs {
			if pin, exists := nic.Pins[pinCtl.Label]; exists {
				pinCommand := SetPinControlData(pin, pinCtl.ParentControl)
				pinCommands = append(pinCommands, *pinCommand)
				found = true
			}
		}

		if !found {
			return nil, fmt.Errorf("%s pin not found in any NIC", pinCtl.Label)
		}
	}

	return &pinCommands, nil
}

func SetPinControlData(pin dpll.PinInfo, control PinParentControl) *dpll.PinParentDeviceCtl {
	Pin := dpll.PinParentDeviceCtl{
		ID:           pin.ID,
		PinParentCtl: make([]dpll.PinControl, 0),
	}

	for deviceIndex, parentDevice := range pin.ParentDevice {
		var prio uint32
		var outputState uint32
		pc := dpll.PinControl{}
		pc.PinParentID = parentDevice.ParentID
		switch deviceIndex {
		case eecDpllIndex:
			prio = uint32(control.EecPriority)
			outputState = uint32(control.EecOutputState)
		case ppsDpllIndex:
			prio = uint32(control.PpsPriority)
			outputState = uint32(control.PpsOutputState)
		}
		if parentDevice.Direction == dpll.PinDirectionInput {
			pc.Prio = &prio
		} else {
			pc.State = &outputState
		}
		Pin.PinParentCtl = append(Pin.PinParentCtl, pc)
	}
	return &Pin
}

func (c *ClockChain) EnableE810Outputs() error {
	// # echo 2 2 > /sys/class/net/$ETH/device/ptp/ptp*/pins/SMA2
	// # echo 2 0 0 1 0 > /sys/class/net/$ETH/device/ptp/ptp*/period
	var pinPath string

	deviceDir := fmt.Sprintf("/sys/class/net/%s/device/ptp/", c.LeadingNIC.Name)
	phcs, err := filesystem.ReadDir(deviceDir)
	if err != nil {
		return fmt.Errorf("e810 failed to read " + deviceDir + ": " + err.Error())
	}
	if len(phcs) > 1 {
		glog.Error("e810 cards should have one PHC per NIC, but %s has %d",
			c.LeadingNIC.Name, len(phcs))
	}
	if len(phcs) == 0 {
		e := fmt.Sprintf("e810 cards should have one PHC per NIC, but %s has 0",
			c.LeadingNIC.Name)
		glog.Error(e)
		return errors.New(e)
	}
	// Enable SMA2 output as a workaround for https://issues.redhat.com/browse/RHEL-110297
	smaPath := fmt.Sprintf("%s%s/pins/%s", deviceDir, phcs[0].Name(), sma2)
	err = writeSysFs(smaPath, "2 2")
	if err != nil {
		glog.Errorf("failed to write 2 2 to %s: %v", smaPath, err)
		return err
	}
	pinPath = fmt.Sprintf("%s%s/period", deviceDir, phcs[0].Name())
	err = writeSysFs(pinPath, sdp22PpsEnable)
	if err != nil {
		glog.Errorf("failed to write " + sdp22PpsEnable + " to " + pinPath + ": " + err.Error())
	}

	return nil
}

// InitPinsTBC initializes the leading card E810 and DPLL pins for T-BC operation
func (c *ClockChain) InitPinsTBC() (*[]dpll.PinParentDeviceCtl, error) {
	// Enable 1PPS output on SDP22
	// (To synchronize the DPLL1 to the E810 PHC synced by ptp4l):
	err := c.EnableE810Outputs()
	if err != nil {
		glog.Error("failed to enable E810 outputs: ", err)
	}
	// Disable GNSS-1PPS (all cards), SDP20 and SDP21
	commandsGnss, err := c.SetPinsControlForAllNICs([]PinControl{
		{
			Label: gnss,
			ParentControl: PinParentControl{
				EecPriority: PriorityDisabled,
				PpsPriority: PriorityDisabled,
			},
		},
	})
	if err != nil {
		glog.Error("failed to disable GNSS: ", err)
	}
	commands, err := c.SetPinsControl([]PinControl{

		{
			Label: sdp22,
			ParentControl: PinParentControl{
				EecPriority: PriorityDisabled,
				PpsPriority: PriorityDisabled,
			},
		},
		{
			Label: sdp20,
			ParentControl: PinParentControl{
				EecPriority: PriorityDisabled,
				PpsPriority: PriorityDisabled,
			},
		},
		{
			Label: sdp21,
			ParentControl: PinParentControl{
				EecOutputState: dpll.PinStateDisconnected,
				PpsOutputState: dpll.PinStateDisconnected,
			},
		},
		{
			Label: sdp23,
			ParentControl: PinParentControl{
				EecOutputState: dpll.PinStateDisconnected,
				PpsOutputState: dpll.PinStateDisconnected,
			},
		},
	})
	if err != nil {
		glog.Error("failed to set pins control: ", err)
	}
	*commands = append(*commands, *commandsGnss...)
	return commands, BatchPinSet(commands)
}

// EnterHoldoverTBC configures the leading card DPLL pins for T-BC holdover
func (c *ClockChain) EnterHoldoverTBC() (*[]dpll.PinParentDeviceCtl, error) {
	// Disable DPLL inputs from e810 (SDP22)
	// Enable DPLL Outputs to e810 (SDP21, SDP23)
	commands, err := c.SetPinsControl([]PinControl{
		{
			Label: sdp22,
			ParentControl: PinParentControl{
				EecPriority: PriorityDisabled,
				PpsPriority: PriorityDisabled,
			},
		},
		{
			Label: sdp23,
			ParentControl: PinParentControl{
				EecOutputState: dpll.PinStateConnected,
				PpsOutputState: dpll.PinStateConnected,
			},
		},
	})
	if err != nil {
		return nil, err
	}
	return commands, BatchPinSet(commands)
}

// EnterNormalTBC configures the leading card DPLL pins for regular T-BC operation
func (c *ClockChain) EnterNormalTBC() (*[]dpll.PinParentDeviceCtl, error) {
	// Disable DPLL Outputs to e810 (SDP23, SDP21)
	// Enable DPLL inputs from e810 (SDP22)
	commands, err := c.SetPinsControl([]PinControl{
		{
			Label: sdp22,
			ParentControl: PinParentControl{
				EecPriority: PriorityDisabled,
				PpsPriority: PriorityEnabled,
			},
		},
		{
			Label: sdp23,
			ParentControl: PinParentControl{
				EecOutputState: dpll.PinStateDisconnected,
				PpsOutputState: dpll.PinStateDisconnected,
			},
		},
	})
	if err != nil {
		return nil, err
	}
	return commands, BatchPinSet(commands)
}

// SetPinDefaults initializes DPLL pins to default recommended values
func (c *ClockChain) SetPinDefaults() (*[]dpll.PinParentDeviceCtl, error) {
	// DPLL Priority List:
	//
	//	Recommended | Pin Index | EEC-DPLL0                    | PPS-DPLL1
	//	Priority    |           | (Frequency/Glitchless)       | (Phase/Glitch Allowed)
	//	------------|-----------|------------------------------|-----------------------------
	//	0           | 6         | 1PPS from GNSS (GNSS-1PPS)  | 1PPS from GNSS (GNSS-1PPS)
	//	2           | 5         | 1PPS from SMA2 (SMA2)       | 1PPS from SMA2 (SMA2)
	//	3           | 4         | 1PPS from SMA1 (SMA1)       | 1PPS from SMA1 (SMA1)
	//	4           | 1         | Reserved                     | 1PPS from E810 (CVL-SDP20)
	//	5           | 0         | Reserved                     | 1PPS from E810 (CVL-SDP22)
	//	6           | --        | Reserved                     | Reserved
	//	7           | --        | Reserved                     | Reserved
	//	8           | 2         | Recovered CLK1 (C827_0-RCLKA) | Recovered CLK1 (C827_0-RCLKA)
	//	9           | 3         | Recovered CLK2 (C827_0-RCLKB) | Recovered CLK2 (C827_0-RCLKB)
	//	10          | --        | OCXO                         | OCXO

	// Also, Enable DPLL Outputs to e810 (SDP21, SDP23)
	commands, err := c.SetPinsControlForAllNICs([]PinControl{
		{
			Label: gnss,
			ParentControl: PinParentControl{
				EecPriority: 0,
				PpsPriority: 0,
			},
		},
		{
			Label: sma1Input,
			ParentControl: PinParentControl{
				EecPriority: 3,
				PpsPriority: 3,
			},
		},
		{
			Label: sma2Input,
			ParentControl: PinParentControl{
				EecPriority: 2,
				PpsPriority: 2,
			},
		},
		{
			Label: sdp20,
			ParentControl: PinParentControl{
				EecPriority: PriorityDisabled,
				PpsPriority: 4,
			},
		},
		{
			Label: sdp22,
			ParentControl: PinParentControl{
				EecPriority: PriorityDisabled,
				PpsPriority: 5,
			},
		},
		{
			Label: sdp21,
			ParentControl: PinParentControl{
				EecOutputState: dpll.PinStateDisconnected,
				PpsOutputState: dpll.PinStateConnected,
			},
		},
		{
			Label: sdp23,
			ParentControl: PinParentControl{
				EecOutputState: dpll.PinStateDisconnected,
				PpsOutputState: dpll.PinStateConnected,
			},
		},
		{
			Label: c8270Rclka,
			ParentControl: PinParentControl{
				EecPriority: 8,
				PpsPriority: 8,
			},
		},
		{
			Label: c8270Rclkb,
			ParentControl: PinParentControl{
				EecPriority: 9,
				PpsPriority: 9,
			},
		},
	})
	if err != nil {
		return nil, err
	}
	return commands, BatchPinSet(commands)
}

func BatchPinSet(commands *[]dpll.PinParentDeviceCtl) error {
	if unitTest {
		return nil
	}
	conn, err := dpll.Dial(nil)
	if err != nil {
		return fmt.Errorf("failed to dial DPLL: %v", err)
	}
	//nolint:errcheck
	defer conn.Close()
	for _, command := range *commands {
		glog.Infof("DPLL pin command %#v", command)
		b, err := dpll.EncodePinControl(command)
		if err != nil {
			return err
		}
		err = conn.SendCommand(dpll.DpllCmdPinSet, b)
		if err != nil {
			glog.Error("failed to send pin command: ", err)
			return err
		}
		info, err := conn.DoPinGet(dpll.DoPinGetRequest{ID: command.ID})
		if err != nil {
			glog.Error("failed to get pin: ", err)
			return err
		}
		reply, err := dpll.GetPinInfoHR(info, time.Now())
		if err != nil {
			glog.Error("failed to convert pin reply to human readable: ", err)
			return err
		}
		glog.Info("pin reply: ", string(reply))
	}
	return nil
}
