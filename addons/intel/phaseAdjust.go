package intel

import (
	"encoding/binary"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/golang/glog"
	dpll "github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/dpll-netlink"
	ptpv1 "github.com/k8snetworkplumbingwg/ptp-operator/api/v1"
	"sigs.k8s.io/yaml"
)

// InputConnector is a connector on the input side of the card
type InputConnector struct {
	Connector string `json:"connector"`
	DelayPs   int    `json:"delayPs,omitempty"`
	// DelayVariationPs int    `json:"delayVariationPs"`
}

// PhaseInputs is a list of phase inputs for a card
type PhaseInputs struct {
	ID                    string         `json:"id"`
	Part                  string         `json:"Part"`
	Input                 InputConnector `json:"inputConnector"`
	GnssInput             bool           `json:"gnssInput"`
	PhaseOutputConnectors []string       `json:"phaseOutputConnectors"`
	UpstreamPort          string         `json:"upstreamPort"`
}

// InternalLink is a link between pin and connector
type InternalLink struct {
	Connector string `yaml:"connector"`
	Pin       string `yaml:"pin"`
	DelayPs   int32  `yaml:"delayPs,omitempty"`
	// DelayVariationPs uint32 `yaml:"delayVariationPs"`
}

// InternalDelays is a list of internal delays for a card
type InternalDelays struct {
	PartType        string         `yaml:"partType"`
	ExternalInputs  []InternalLink `yaml:"externalInputs"`
	ExternalOutputs []InternalLink `yaml:"externalOutputs"`
	GnssInput       InternalLink   `yaml:"gnssInput"`
}

type delayCompensation struct {
	DelayPs   int32
	pinLabel  string
	iface     string
	direction string
	clockID   string
}

var hardware = map[string]string{
	"E810-XXVDA4T": `
partType: E810-XXVDA4T
externalInputs: # This always goes from connector to pin
- connector: SMA1
  pin: SMA1
  delayPs: 7658
- connector: SMA2
  pin: SMA2/U.FL2
  delayPs: 7385
- connector: u.FL2
  pin: SMA2/U.FL2
  delayPs: 9795
externalOutputs:  # This always goes from pin to connector
- pin: REF-SMA1
  connector: u.FL1
  delayPs: 1274
- pin: REF-SMA1
  connector: SMA1
  delayPs: 1376
- pin: REF-SMA2/U.FL2
  connector: SMA2
  delayPs: 2908
gnssInput:
  connector: GNSS
  pin: GNSS-1PPS
  delayPs: 6999
`,
}

func InitInternalDelays(part string) (*InternalDelays, error) {
	for k, v := range hardware {
		if k == part {
			delays := InternalDelays{}
			b := []byte(v)
			err := yaml.Unmarshal(b, &delays)
			if err != nil {
				return nil, err
			}
			return &delays, nil
		}
	}
	return nil, fmt.Errorf("can't find delays for %s", part)
}

func sendDelayCompensation(comp *[]delayCompensation, DpllPins []*dpll.PinInfo) error {
	glog.Info(comp)
	conn, err := dpll.Dial(nil)
	if err != nil {
		return fmt.Errorf("failed to dial DPLL: %v", err)
	}
	//nolint:errcheck
	defer conn.Close()

	for _, pin := range DpllPins {
		for _, dc := range *comp {
			var desiredClockID uint64
			desiredClockID, err = strconv.ParseUint(dc.clockID, 10, 64)
			if err != nil {
				return fmt.Errorf("failed to parse clock id %s: %v", dc.clockID, err)
			}
			if desiredClockID == pin.ClockID && strings.EqualFold(pin.BoardLabel, dc.pinLabel) {
				err = conn.PinPhaseAdjust(dpll.PinPhaseAdjustRequest{ID: pin.ID, PhaseAdjust: dc.DelayPs})
				if err != nil {
					return fmt.Errorf("failed to send phase adjustment to %s clock id %d: %v",
						pin.BoardLabel, desiredClockID, err)
				}
				glog.Infof("set phaseAdjust of pin %s at clock ID %x to %d ps", pin.BoardLabel, pin.ClockID, dc.DelayPs)
			}
		}
	}
	return nil
}

func addClockID(iface string, nodeProfile *ptpv1.PtpProfile) (*string, error) {
	dpllClockIDStr := fmt.Sprintf("clockId[%s]", iface)
	clockID, found := (*nodeProfile).PtpSettings[dpllClockIDStr]
	if !found {
		return nil, fmt.Errorf("plugin E810 error: can't find clock ID for interface %s - are all pins configured?", iface)
	}
	return &clockID, nil
}

func findInternalLink(links []InternalLink, connector string) *InternalLink {
	for _, link := range links {
		if strings.EqualFold(link.Connector, connector) {
			return &link
		}
	}
	return nil
}

// Vital Product Data
type Vpd struct {
	IdentifierStringDescriptor string
	PartNumber                 string
	SerialNumber               string
	VendorSpecific1            string
	VendorSpecific2            string
}

const (
	PCI_VPD_ID_STRING_TAG        = 0x82
	PCI_VPD_RO_TAG               = 0x90
	PCI_VPD_RW_TAG               = 0x91
	PCI_VPD_END_TAG              = 0x78
	PCI_VPD_BLOCK_DESCRIPTOR_LEN = 3
	PCI_VPD_KEYWORD_LEN          = 2
)

// ParseVpd extracts some of the product data
func ParseVpd(vpdFile []byte) *Vpd {
	vpd := &Vpd{}
	lenFile := len(vpdFile)
	offset := 0
	for offset < lenFile {
		blockDesc := vpdFile[offset : offset+PCI_VPD_BLOCK_DESCRIPTOR_LEN]
		tag := vpdFile[offset]
		l := blockDesc[1:PCI_VPD_BLOCK_DESCRIPTOR_LEN]
		lenBlock := binary.LittleEndian.Uint16(l)
		block := vpdFile[offset+PCI_VPD_BLOCK_DESCRIPTOR_LEN : offset+PCI_VPD_BLOCK_DESCRIPTOR_LEN+int(lenBlock)]
		offset += int(lenBlock + PCI_VPD_BLOCK_DESCRIPTOR_LEN)
		switch tag {
		case PCI_VPD_ID_STRING_TAG:
			vpd.IdentifierStringDescriptor = string(block)
		case PCI_VPD_RO_TAG:
			ro := parseVpdBlock(block)
			for k, v := range *ro {
				switch k {
				case "SN":
					vpd.SerialNumber = v
				case "PN":
					vpd.PartNumber = v
				case "V1":
					vpd.VendorSpecific1 = v
				case "V2":
					vpd.VendorSpecific2 = v
				}
			}
		case PCI_VPD_END_TAG:
			goto done
		default:
			continue
		}
	}
done:
	return vpd
}

func parseVpdBlock(block []byte) *map[string]string {
	rv := map[string]string{}
	lenBlock := len(block)
	offset := 0
	for offset < lenBlock {
		kw := string(block[offset : offset+PCI_VPD_KEYWORD_LEN])
		ln := block[offset+PCI_VPD_KEYWORD_LEN]
		data := block[offset+PCI_VPD_KEYWORD_LEN+1 : offset+int(ln)+PCI_VPD_BLOCK_DESCRIPTOR_LEN]
		if strings.HasPrefix(kw, "V") || kw == "PN" || kw == "SN" {
			rv[kw] = string(data)
		}
		offset += int(ln) + PCI_VPD_BLOCK_DESCRIPTOR_LEN
	}
	return &rv
}

// GetHardwareFingerprint returns the card identity for the purpose of
// matching to the correct internal delay profile
// Currently the fingerprint is extracted from the "Vendor Information V1"
// in the hardware Vital Product Data (VPD). With more cards with different
// delay profiles are available, this function might need to change depending on
// how manufacturers expose data relevant for delay profiles in the VPD file
func GetHardwareFingerprint(device string) string {
	b, err := os.ReadFile(fmt.Sprintf("/sys/class/net/%s/device/vpd", device))
	if err != nil {
		glog.Error(err)
		return ""
	}
	vpd := ParseVpd(b)
	split := strings.Fields(vpd.VendorSpecific1)
	return split[len(split)-1]
}
