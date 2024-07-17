package synce

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/golang/glog"
	"github.com/openshift/linuxptp-daemon/pkg/event"
)

const (
	DEFAULT_QL    = 0xF
	DEFAULT_EXTQL = 0xff //**If extended SSM is not enabled, it's implicitly assumed as 0xFF
)

type LogType int

/*
synce4l[622796.479]: [synce4l.0.config] tx_rebuild_tlv: attached new TLV, QL=0xf on ens7f0",
synce4l[622796.479]: [synce4l.0.config] tx_rebuild_tlv: attached new extended TLV, EXT_QL=0xff on ens7f0
synce4l[627602.540]: [synce4l.0.config]EEC_LOCKED/EEC_LOCKED_HO_ACQ on GNSS of synce1
synce4l[627602.540]: [synce4l.0.config] EEC_HOLDOVER on synce1
synce4l[627602.593]: [synce4l.0.config] tx_rebuild_tlv: attached new TLV, QL=0xf on ens7f0
synce4l[627602.593]: [synce4l.0.config] tx_rebuild_tlv: attached new extended TLV, EXT_QL=0xff on ens7f0
synce4l[627685.138]: [synce4l.0.config] EEC_LOCKED/EEC_LOCKED_HO_ACQ on GNSS of synce1
synce4l[627685.138]: [synce4l.0.config] act on EEC_LOCKED/EEC_LOCKED_HO_ACQ for ens7f0
*/
const (
	SYNCE_STATE LogType = iota
	QL_STATE
	EXT_QL_STATE
)

var (
	// ""synce4l[627602.540]: [synce4l.0.config]EEC_LOCKED/EEC_LOCKED_HO_ACQ on GNSS of synce1",",
	stateRegexpOf = regexp.MustCompile(`(EEC_FREERUN|EEC_INVALID|EEC_LOCKED|EEC_HOLDOVER|EEC_LOCKED_HO_ACQ) on ([\w/]+) of ([\w/]+)`)
	// "synce4l[627602.540]: [synce4l.0.config] EEC_HOLDOVER on synce1",",
	stateRegexpOn = regexp.MustCompile(`(EEC_FREERUN|EEC_INVALID|EEC_LOCKED|EEC_HOLDOVER|EEC_LOCKED_HO_ACQ) on ([\w/]+)`)
	// synce4l[627685.138]: [synce4l.0.config] act on EEC_LOCKED/EEC_LOCKED_HO_ACQ for ens7f0",
	stateRegexpFor = regexp.MustCompile(`(EEC_FREERUN|EEC_INVALID|EEC_LOCKED|EEC_HOLDOVER|EEC_LOCKED_HO_ACQ) for ([\w/]+)`)
	qlRegexp       = regexp.MustCompile(` QL=0x([0-9a-fA-F]+) on (\w+)`)
	extQLRegexp    = regexp.MustCompile(`EXT_QL=0x([0-9a-fA-F]+) on (\w+)`)
)

// LogEntry structure to hold extracted data
type LogEntry struct {
	State     *string
	QL        byte
	ExtQl     byte
	ExtSource *string
	Device    *string
	Source    *string
	LogType   LogType
}

/*type Device struct {
	Name          string // synce1,synce2 etc
	Source        string // ens1fo, gnss, sm1 etc
	NetworkOption int64  //    1 or 2
	ExtQLEnabled  bool
}*/

type EECState int

const (
	EEC_UNKNOWN EECState = iota
	EEC_INVALID
	EEC_FREERUN
	EEC_LOCKED
	EEC_LOCKED_HO_ACQ
	EEC_HOLDOVER
)

func (e EECState) String() string {
	switch e {
	//	EEC_UNKNOWN
	case EEC_UNKNOWN:
		return "EEC_UNKNOWN"
	//	EEC_INVALID
	case EEC_INVALID:
		return "EEC_INVALID"
	//	EEC_FREERUN
	case EEC_FREERUN:
		return "EEC_FREERUN"
	//	EEC_LOCKED
	case EEC_LOCKED:
		return "EEC_LOCKED"
	//	EEC_LOCKED_HO_ACQ
	case EEC_LOCKED_HO_ACQ:
		return "EEC_LOCKED_HO_ACQ"
	//	EEC_HOLDOVER
	case EEC_HOLDOVER:
		return "EEC_HOLDOVER"
	default:
		return "EEC_UNKNOWN"
	}
}

func (e EECState) ToPTPState() event.PTPState {
	switch e {
	//	EEC_UNKNOWN
	case EEC_UNKNOWN:
		return event.PTP_UNKNOWN
	//	EEC_INVALID
	case EEC_INVALID:
		return event.PTP_UNKNOWN
	//	EEC_FREERUN
	case EEC_FREERUN:
		return event.PTP_FREERUN
	//	EEC_LOCKED
	case EEC_LOCKED:
		return event.PTP_LOCKED
	//	EEC_LOCKED_HO_ACQ
	case EEC_LOCKED_HO_ACQ:
		return event.PTP_LOCKED
	//	EEC_HOLDOVER
	case EEC_HOLDOVER:
		return event.PTP_HOLDOVER
	default:
		return event.PTP_UNKNOWN
	}
}

// StringToEECState converts a string to a EECState enum value
func StringToEECState(str string) EECState {
	switch str {
	case "EEC_FREERUN":
		return EEC_FREERUN
	case "EEC_LOCKED":
		return EEC_LOCKED
	case "EEC_INVALID":
		return EEC_INVALID
	case "EEC_LOCKED_HO_ACQ":
		return EEC_LOCKED_HO_ACQ
	case "EEC_HOLDOVER":
		return EEC_HOLDOVER
	default:
		return EEC_UNKNOWN
	}
}

// QualityLevel is an enum for the quality levels
type QualityLevel int

const (
	EPRTC QualityLevel = iota
	PRTC
	PRC
	SSUA
	SSUB
	EEC1
	PRS
	STU
	ST2
	TNC
	ST3E
	EEC2
	PROV
	UNKNOWN
)

func (q QualityLevel) String() string {
	switch q {
	//	EPRTC
	case EPRTC:
		return "EPRTC"
	//	PRTC
	case PRTC:
		return "PRTC"
	//	PRC
	case PRC:
		return "PRC"
	//	SSUA
	case SSUA:
		return "SSUA"
	//	SSUB
	case SSUB:
		return "SSUB"
	//	EEC1
	case EEC1:
		return "EEC1"
	//	PRS
	case PRS:
		return "PRS"
	//	STU
	case STU:
		return "STU"
	//	TNC
	case ST2:
		return "ST2"
	//	ST3E
	case ST3E:
		return "ST3E"
	//	EEC2
	case EEC2:
		return "EEC2"
	//	PROV
	case PROV:
		return "PROV"
	case UNKNOWN:
		return "UNKNOWN"

	default:
		return "UNKNOWN"
	}
}

// QualityLevelInfo holds the information for each quality level
type QualityLevelInfo struct {
	Priority    int
	SSM         byte
	ExtendedSSM byte
}

// Mapping of QualityLevel to its information in option 1 networks
var qualityLevelInfoOption1 = map[QualityLevel]QualityLevelInfo{
	EPRTC: {0, 0x2, 0x21},
	PRTC:  {1, 0x2, 0x20},
	PRC:   {2, 0x2, 0xFF},
	SSUA:  {3, 0x4, 0xFF},
	SSUB:  {4, 0x8, 0xFF},
	EEC1:  {5, 0xB, 0xFF},
}

// Compare compares two QualityLevelInfo objects based on their SSM and ExtendedSSM fields.
// It returns true if both fields are equal, otherwise false.
func (q *QualityLevelInfo) Compare(other QualityLevelInfo) bool {
	return q.SSM == other.SSM && (other.ExtendedSSM == DEFAULT_QL || q.ExtendedSSM == other.ExtendedSSM)
}

// Mapping of QualityLevel to its information in option 2 networks
var qualityLevelInfoOption2 = map[QualityLevel]QualityLevelInfo{
	EPRTC: {0, 0x1, 0x21},
	PRTC:  {1, 0x1, 0x20},
	PRS:   {2, 0x1, 0xFF},
	STU:   {3, 0x0, 0xFF},
	ST2:   {4, 0x7, 0xFF},
	TNC:   {5, 0x4, 0xFF},
	ST3E:  {6, 0xD, 0xFF},
	EEC2:  {7, 0xA, 0xFF},
	PROV:  {8, 0xE, 0xFF},
}

type Config struct {
	Name           string
	Ifaces         []string
	ClockId        string
	NetworkOption  int // default 1: 1 or 2
	ExtendedTlv    int // default 0 : 0 or 1
	ExternalSource string
	LastQLState    map[string]*QualityLevelInfo
	LastClockState event.PTPState
}

type Relations struct {
	Devices []*Config
}

func (r *Relations) AddDeviceConfig(config Config) {
	r.Devices = append(r.Devices, &config)
}
func (r *Relations) AddClockIds(ptpSettings map[string]string) {
	for k, v := range ptpSettings {
		glog.Info(k, " ", v)
		if strings.HasPrefix(k, "clockId") {
			iface := strings.ReplaceAll(k, "clockId[", "")
			iface = strings.ReplaceAll(iface, "]", "")
			for _, d := range r.Devices {
				for _, i := range d.Ifaces {
					if i == iface {
						d.ClockId = v
						goto found
					}
				}
				glog.Errorf("clock ID not found for syncE device %s - no interfaces provided. Check synce4lConf section",
					d.Name)
			}
		}
	found:
	}
}
func (r *Relations) AppendDeviceConfig(ifaces []string, devName string, networkOption int, extendedTlv int) {
	if len(ifaces) > 0 {
		binding := Config{
			Name:          devName,
			Ifaces:        ifaces,
			NetworkOption: networkOption,
			ExtendedTlv:   extendedTlv,
		}
		r.Devices = append(r.Devices, &binding)
	}
}

func (r *Relations) GetSyncERelation(deviceName, extSourceName, iface string) (networkOption, extTvlEnabled int, device, extSource string, ifaces []string) {
	if len(r.Devices) == 0 {
		return
	}
	for _, v := range r.Devices {
		switch {
		case v.Name == deviceName || v.ExternalSource == extSourceName:
			device = v.Name
			networkOption = v.NetworkOption
			extTvlEnabled = v.ExtendedTlv
			extSource = v.ExternalSource
			ifaces = v.Ifaces
		default:
			for _, i := range v.Ifaces {
				if i == iface {
					device = v.Name
					networkOption = v.NetworkOption
					extTvlEnabled = v.ExtendedTlv
					extSource = v.ExternalSource
					ifaces = v.Ifaces
				}
			}
		}
	}
	return
}

func GetQualityLevelInfoOption2() map[QualityLevel]QualityLevelInfo {
	return deepCopyQualityLevelMap(qualityLevelInfoOption2)
}
func GetQualityLevelInfoOption1() map[QualityLevel]QualityLevelInfo {
	return deepCopyQualityLevelMap(qualityLevelInfoOption1)
}

// deepCopyQualityLevelMap creates a deep copy of a map[QualityLevel]QualityLevelInfo.
func deepCopyQualityLevelMap(original map[QualityLevel]QualityLevelInfo) map[QualityLevel]QualityLevelInfo {
	copyMap := make(map[QualityLevel]QualityLevelInfo)

	for key, value := range original {
		copyMap[key] = value // Since QualityLevelInfo contains only basic types, this is a deep copy
	}

	return copyMap
}

func PrintOption1Networks() {
	fmt.Println("Option 1 Networks:")
	for ql, info := range qualityLevelInfoOption1 {
		fmt.Printf("Quality Level: %d, Priority: %d, SSM: 0x%X, Extended SSM: 0x%X\n", ql, info.Priority, info.SSM, info.ExtendedSSM)
	}
}

func PrintOption2Networks() {
	fmt.Println("\nOption 2 Networks:")
	for ql, info := range qualityLevelInfoOption2 {
		fmt.Printf("Quality Level: %d, Priority: %d, SSM: 0x%X, Extended SSM: 0x%X\n", ql, info.Priority, info.SSM, info.ExtendedSSM)
	}
}

// ClockQuality ... return ClockQuality details
func (c *Config) ClockQuality(qualityInfo QualityLevelInfo) (clock string, ql QualityLevelInfo) {
	if c.ExtendedTlv == 0 {
		qualityInfo.ExtendedSSM = 0xFF //**If extended SSM is not enabled, it's implicitly assumed as 0xFF
	}
	if c.NetworkOption == 1 {
		for q, info := range qualityLevelInfoOption1 {
			if info.Compare(qualityInfo) {
				return q.String(), info
			}
		}
	} else if c.NetworkOption == 2 {
		for q, info := range qualityLevelInfoOption2 {
			if info.Compare(qualityInfo) {
				return q.String(), info
			}
		}
	}

	return UNKNOWN.String(), QualityLevelInfo{
		Priority:    0,
		SSM:         DEFAULT_QL,
		ExtendedSSM: DEFAULT_QL,
	}
}

func ParseLog(output string) LogEntry {
	// Regular expressions for extracting data

	// Slices to store extracted data
	logEntry := LogEntry{
		State:     nil,
		QL:        DEFAULT_QL,
		ExtQl:     DEFAULT_QL,
		ExtSource: nil,
		Device:    nil,
		Source:    nil,
		LogType:   0,
	}

	// Extracting states and 'on of ' values
	stateMatches := stateRegexpOf.FindAllStringSubmatch(output, -1)
	for _, match := range stateMatches {
		if len(match) > 3 {
			return LogEntry{State: strPtr(match[1]), ExtSource: strPtr(match[2]), Device: strPtr(match[3]), LogType: SYNCE_STATE}
		}
	}
	// Extracting states and 'on' values
	stateMatches = stateRegexpOn.FindAllStringSubmatch(output, -1)
	for _, match := range stateMatches {
		if len(match) > 2 {
			return LogEntry{State: strPtr(match[1]), Device: strPtr(match[2]), LogType: SYNCE_STATE}
		}
	}

	// Extracting states and 'for' values
	stateMatches = stateRegexpFor.FindAllStringSubmatch(output, -1)
	for _, match := range stateMatches {
		if len(match) > 2 {
			return LogEntry{State: strPtr(match[1]), Source: strPtr(match[2]), LogType: SYNCE_STATE}
		}
	}

	// Extracting EXT_QL values and 'on' values
	extQLMatches := extQLRegexp.FindAllStringSubmatch(output, -1)
	for _, match := range extQLMatches {
		if len(match) > 2 {
			extQLValue, err := strconv.ParseUint(match[1], 16, 8) // Parse as 8-bit unsigned int
			if err == nil {
				return LogEntry{ExtQl: byte(extQLValue), Source: strPtr(match[2]), LogType: EXT_QL_STATE}
			}
		}
	}

	// Extracting QL values and 'on' values
	qlMatches := qlRegexp.FindAllStringSubmatch(output, -1)
	for _, match := range qlMatches {
		if len(match) > 2 {
			qLValue, err := strconv.ParseUint(match[1], 16, 8) // Parse as 8-bit unsigned int
			if err == nil {
				return LogEntry{ExtQl: DEFAULT_QL, QL: byte(qLValue), Source: strPtr(match[2]), LogType: QL_STATE}
			}
		}
	}

	return logEntry
}

func (l *LogEntry) String() string {
	s := strings.Builder{}
	s.WriteString("state: " + ToString(l.State) + "\n")
	s.WriteString("Device: " + ToString(l.Device) + "\n")
	s.WriteString("Source: " + ToString(l.Source) + "\n")
	s.WriteString("ExtSource: " + ToString(l.ExtSource) + "\n")
	s.WriteString("ql: " + string(l.QL) + "\n")
	s.WriteString("extql: " + string(l.ExtQl) + "\n")
	return s.String()
}
func ToString(s *string) string {
	if s == nil {
		return ""
	} else {
		return *s
	}
}

func strPtr(s string) *string {
	ptString := s
	return &ptString
}
