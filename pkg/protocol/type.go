package protocol

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/golang/glog"

	"github.com/facebook/time/ptp/protocol"
)

// extend fbprotocol.ClockClass according to https://www.itu.int/rec/T-REC-G.8275.1-202211-I/en section 6.4 table 3
const (
	ClockClassFreerun       protocol.ClockClass = 248
	ClockClassUninitialized protocol.ClockClass = 0
	ClockClassOutOfSpec     protocol.ClockClass = 140
)

// DataSet is an interface for PTP data sets that can be parsed from PMC output.
type DataSet interface {
	Keys() []string
	Update(key string, value string)
	ValueRegEx() map[string]string
	RegEx() string
	String() string
}

func buildDataSetRegex(keys []string, valuePatterns map[string]string, capture bool, optionalKeys []string) string {
	regex := ""
	for _, k := range keys {
		isOptional := slices.Contains(optionalKeys, k)
		if isOptional {
			regex += "(?:"
		}
		regex += `[[:space:]]+` + k + `[[:space:]]+`

		if capture {
			regex += `(`
		}
		regex += valuePatterns[k]
		if capture {
			regex += `)`
		}

		if isOptional {
			regex += ")?"
		}
	}
	return regex
}

type GrandmasterSettings struct {
	ClockQuality     protocol.ClockQuality
	TimePropertiesDS TimePropertiesDS
}

type TimePropertiesDS struct {
	CurrentUtcOffset      int32
	CurrentUtcOffsetValid bool
	Leap59                bool
	Leap61                bool
	TimeTraceable         bool
	FrequencyTraceable    bool
	PtpTimescale          bool
	TimeSource            protocol.TimeSource
}

// ParentDataSet defines IEEE 1588 ParentDS data set
type ParentDataSet struct {
	ParentPortIdentity                    string
	ParentStats                           uint8
	ObservedParentOffsetScaledLogVariance uint16
	ObservedParentClockPhaseChangeRate    uint32
	GrandmasterPriority1                  uint8
	GrandmasterClockClass                 uint8
	GrandmasterClockAccuracy              uint8
	GrandmasterOffsetScaledLogVariance    uint16
	GrandmasterPriority2                  uint8
	GrandmasterIdentity                   string
}

// ExternalGrandmasterProperties defines T-BC downstream ports
// grandmaster properties that can be modified externally through
// management commands
type ExternalGrandmasterProperties struct {
	GrandmasterIdentity string
	StepsRemoved        uint16
}

// CurrentDS defines IEEE 1588 CurrentDS data set
type CurrentDS struct {
	StepsRemoved     uint16
	offsetFromMaster float64
	meanPathDelay    float64
}

func (g *GrandmasterSettings) String() string {
	if g == nil {
		glog.Error("returned empty grandmasterSettings")
		return ""

	}
	result := fmt.Sprintf(" clockClass              %d\n", g.ClockQuality.ClockClass)
	result += fmt.Sprintf(" clockAccuracy           0x%x\n", g.ClockQuality.ClockAccuracy)
	result += fmt.Sprintf(" offsetScaledLogVariance 0x%x\n", g.ClockQuality.OffsetScaledLogVariance)
	result += fmt.Sprintf(" currentUtcOffset        %d\n", g.TimePropertiesDS.CurrentUtcOffset)
	result += fmt.Sprintf(" leap61                  %d\n", btoi(g.TimePropertiesDS.Leap61))
	result += fmt.Sprintf(" leap59                  %d\n", btoi(g.TimePropertiesDS.Leap59))
	result += fmt.Sprintf(" currentUtcOffsetValid   %d\n", btoi(g.TimePropertiesDS.CurrentUtcOffsetValid))
	result += fmt.Sprintf(" ptpTimescale            %d\n", btoi(g.TimePropertiesDS.PtpTimescale))
	result += fmt.Sprintf(" timeTraceable           %d\n", btoi(g.TimePropertiesDS.TimeTraceable))
	result += fmt.Sprintf(" frequencyTraceable      %d\n", btoi(g.TimePropertiesDS.FrequencyTraceable))
	result += fmt.Sprintf(" timeSource              0x%x\n", uint(g.TimePropertiesDS.TimeSource))
	return result
}

// Keys returns variables names in order of pmc command results
func (g *GrandmasterSettings) Keys() []string {
	return []string{"clockClass", "clockAccuracy", "offsetScaledLogVariance",
		"currentUtcOffset", "leap61", "leap59", "currentUtcOffsetValid",
		"ptpTimescale", "timeTraceable", "frequencyTraceable", "timeSource"}
}

func (g *GrandmasterSettings) ValueRegEx() map[string]string {
	return map[string]string{
		"clockClass":              `\d+`,
		"clockAccuracy":           `0x[\da-f]+`,
		"offsetScaledLogVariance": `0x[\da-f]+`,
		"currentUtcOffset":        `\d+`,
		"currentUtcOffsetValid":   `[01]`,
		"leap59":                  `[01]`,
		"leap61":                  `[01]`,
		"timeTraceable":           `[01]`,
		"frequencyTraceable":      `[01]`,
		"ptpTimescale":            `[01]`,
		"timeSource":              `0x[\da-f]+`,
	}
}

func (g *GrandmasterSettings) RegEx() string {
	return buildDataSetRegex(g.Keys(), g.ValueRegEx(), true, []string{})
}

// MonitorRegEx generates the GrandmasterSettings regex without capture groups.
func (g *GrandmasterSettings) MonitorRegEx() string {
	return buildDataSetRegex(g.Keys(), g.ValueRegEx(), false, []string{})
}

func (g *GrandmasterSettings) Update(key string, value string) {
	switch key {
	case "clockClass":
		g.ClockQuality.ClockClass = protocol.ClockClass(stou8(value))
	case "clockAccuracy":
		g.ClockQuality.ClockAccuracy = protocol.ClockAccuracy(stou8h(value))
	case "offsetScaledLogVariance":
		g.ClockQuality.OffsetScaledLogVariance = stou16h(value)
	case "currentUtcOffset":
		g.TimePropertiesDS.CurrentUtcOffset = stoi32(value)
	case "currentUtcOffsetValid":
		g.TimePropertiesDS.CurrentUtcOffsetValid = stob(value)
	case "leap59":
		g.TimePropertiesDS.Leap59 = stob(value)
	case "leap61":
		g.TimePropertiesDS.Leap61 = stob(value)
	case "timeTraceable":
		g.TimePropertiesDS.TimeTraceable = stob(value)
	case "frequencyTraceable":
		g.TimePropertiesDS.FrequencyTraceable = stob(value)
	case "ptpTimescale":
		g.TimePropertiesDS.PtpTimescale = stob(value)
	case "timeSource":
		g.TimePropertiesDS.TimeSource = protocol.TimeSource(stou8h(value))
	}
}

func btoi(b bool) uint8 {
	if b {
		return 1
	}
	return 0
}

func stob(s string) bool {
	return s == "1"
}

// ootob on|off to bool
func ootob(s string) bool {
	return s == "on"
}

// btooo bool to on|off
func btooo(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

func stou8(s string) uint8 {
	uint64Value, err := strconv.ParseUint(s, 10, 8)
	if err != nil {
		fmt.Printf("%v\n", err)
	}
	return uint8(uint64Value)
}

func stou8h(s string) uint8 {
	uint64Value, err := strconv.ParseUint(strings.Replace(s, "0x", "", 1), 16, 8)
	if err != nil {
		fmt.Printf("%v\n", err)
	}
	return uint8(uint64Value)
}

func stou16h(s string) uint16 {
	uint64Value, err := strconv.ParseUint(strings.Replace(s, "0x", "", 1), 16, 16)
	if err != nil {
		fmt.Printf("%v\n", err)
	}
	return uint16(uint64Value)
}

func stou16(s string) uint16 {
	uint64Value, err := strconv.ParseUint(s, 10, 16)
	if err != nil {
		fmt.Printf("%v\n", err)
	}
	return uint16(uint64Value)
}

func stoi32(s string) int32 {
	int64Value, err := strconv.ParseInt(s, 10, 32)
	if err != nil {
		fmt.Printf("%v\n", err)
	}
	return int32(int64Value)
}

func stof64(s string) float64 {
	f64val, err := strconv.ParseFloat(s, 64)
	if err != nil {
		fmt.Printf("%v\n", err)
	}
	return f64val
}
func stou32h(s string) uint32 {
	uint64Value, err := strconv.ParseUint(strings.Replace(s, "0x", "", 1), 16, 32)
	if err != nil {
		fmt.Printf("%v\n", err)
	}
	return uint32(uint64Value)
}

// ValueRegEx provides the regex method for the ParentDS values matching
func (p *ParentDataSet) ValueRegEx() map[string]string {
	return map[string]string{
		"parentPortIdentity":                    `.*`,
		"parentStats":                           `\d+`,
		"observedParentOffsetScaledLogVariance": `0x[\da-f]+`,
		"observedParentClockPhaseChangeRate":    `0x[\da-f]+`,
		"grandmasterPriority1":                  `\d+`,
		"gm.ClockClass":                         `\d+`,
		"gm.ClockAccuracy":                      `0x[\da-f]+`,
		"gm.OffsetScaledLogVariance":            `0x[\da-f]+`,
		"grandmasterPriority2":                  `\d+`,
		"grandmasterIdentity":                   `.*`,
	}
}

// RegEx generates the ParentDataSet command regex
func (p *ParentDataSet) RegEx() string {
	return buildDataSetRegex(p.Keys(), p.ValueRegEx(), true, []string{})
}

// MonitorRegEx generates the ParentDataSet regex without capture groups.
func (p *ParentDataSet) MonitorRegEx() string {
	return buildDataSetRegex(p.Keys(), p.ValueRegEx(), false, []string{})
}

// Keys provides the keys method for the ParentDS values
func (p *ParentDataSet) Keys() []string {
	return []string{
		"parentPortIdentity",
		"parentStats",
		"observedParentOffsetScaledLogVariance",
		"observedParentClockPhaseChangeRate",
		"grandmasterPriority1",
		"gm.ClockClass",
		"gm.ClockAccuracy",
		"gm.OffsetScaledLogVariance",
		"grandmasterPriority2",
		"grandmasterIdentity",
	}
}

// Update provides the Update method for the ParentDS values
func (p *ParentDataSet) Update(key string, value string) {
	switch key {
	case "parentPortIdentity":
		p.ParentPortIdentity = value
	case "parentStats":
		p.ParentStats = (stou8(value))
	case "observedParentOffsetScaledLogVariance":
		p.ObservedParentOffsetScaledLogVariance = stou16h(value)
	case "observedParentClockPhaseChangeRate":
		p.ObservedParentClockPhaseChangeRate = stou32h(value)
	case "grandmasterPriority1":
		p.GrandmasterPriority1 = stou8(value)
	case "gm.ClockClass":
		p.GrandmasterClockClass = stou8(value)
	case "gm.ClockAccuracy":
		p.GrandmasterClockAccuracy = stou8h(value)
	case "gm.OffsetScaledLogVariance":
		p.GrandmasterOffsetScaledLogVariance = stou16h(value)
	case "grandmasterPriority2":
		p.GrandmasterPriority2 = stou8(value)
	case "grandmasterIdentity":
		p.GrandmasterIdentity = value
	}
}

func (p *ParentDataSet) String() string {
	if p == nil {
		glog.Error("returned empty parentDataSet")
		return ""
	}
	result := fmt.Sprintf("parentPortIdentity                    %s\n", p.ParentPortIdentity)
	result += fmt.Sprintf("parentStats                           %d\n", p.ParentStats)
	result += fmt.Sprintf("observedParentOffsetScaledLogVariance 0x%x\n", p.ObservedParentOffsetScaledLogVariance)
	result += fmt.Sprintf("observedParentClockPhaseChangeRate    0x%x\n", p.ObservedParentClockPhaseChangeRate)
	result += fmt.Sprintf("grandmasterPriority1                  %d\n", p.GrandmasterPriority1)
	result += fmt.Sprintf("gm.ClockClass                         %d\n", p.GrandmasterClockClass)
	result += fmt.Sprintf("gm.ClockAccuracy                      0x%x\n", p.GrandmasterClockAccuracy)
	result += fmt.Sprintf("gm.OffsetScaledLogVariance            0x%x\n", p.GrandmasterOffsetScaledLogVariance)
	result += fmt.Sprintf("grandmasterPriority2                  %d\n", p.GrandmasterPriority2)
	result += fmt.Sprintf("grandmasterIdentity                   %s\n", p.GrandmasterIdentity)
	return result
}

// Equal compares two ParentDataSet instances for equality
func (p *ParentDataSet) Equal(other *ParentDataSet) bool {
	if p == nil && other == nil {
		return true
	}
	if p == nil || other == nil {
		return false
	}

	return p.ParentPortIdentity == other.ParentPortIdentity &&
		p.ParentStats == other.ParentStats &&
		p.ObservedParentOffsetScaledLogVariance == other.ObservedParentOffsetScaledLogVariance &&
		p.ObservedParentClockPhaseChangeRate == other.ObservedParentClockPhaseChangeRate &&
		p.GrandmasterPriority1 == other.GrandmasterPriority1 &&
		p.GrandmasterClockClass == other.GrandmasterClockClass &&
		p.GrandmasterClockAccuracy == other.GrandmasterClockAccuracy &&
		p.GrandmasterOffsetScaledLogVariance == other.GrandmasterOffsetScaledLogVariance &&
		p.GrandmasterPriority2 == other.GrandmasterPriority2 &&
		p.GrandmasterIdentity == other.GrandmasterIdentity
}

// ValueRegEx provides the regex method for the ExternalGrandmasterProperties values matching
func (e *ExternalGrandmasterProperties) ValueRegEx() map[string]string {
	return map[string]string{
		"gmIdentity":   `\d*\.\d*\.\d*`,
		"stepsRemoved": `\d+`,
	}
}

// RegEx generates the ExternalGrandmasterProperties command regex
func (e *ExternalGrandmasterProperties) RegEx() string {
	return buildDataSetRegex(e.Keys(), e.ValueRegEx(), true, []string{})
}

// MonitorRegEx generates the ExternalGrandmasterProperties regex without capture groups.
func (e *ExternalGrandmasterProperties) MonitorRegEx() string {
	return buildDataSetRegex(e.Keys(), e.ValueRegEx(), false, []string{})
}

// Keys provides the keys method for the ExternalGrandmasterProperties values
func (e *ExternalGrandmasterProperties) Keys() []string {
	return []string{"gmIdentity", "stepsRemoved"}
}

// Update provides the Update method for the ExternalGrandmasterProperties values
func (e *ExternalGrandmasterProperties) Update(key string, value string) {
	switch key {
	case "gmIdentity":
		e.GrandmasterIdentity = value
	case "stepsRemoved":
		e.StepsRemoved = stou16(value)
	}
}

// String returns the ExternalGrandmasterProperties as a string
func (e *ExternalGrandmasterProperties) String() string {
	if e == nil {
		glog.Error("returned empty grandmasterSettings")
		return ""
	}
	result := fmt.Sprintf(" gmIdentity %s\n", e.GrandmasterIdentity)
	result += fmt.Sprintf(" stepsRemoved        %d\n", e.StepsRemoved)
	return result
}

// ValueRegEx provides the regex method for the CurrentDS values matching
func (c *CurrentDS) ValueRegEx() map[string]string {
	return map[string]string{
		"stepsRemoved":     `\d+`,
		"offsetFromMaster": `-?\d+\.\d+`,
		"meanPathDelay":    `\d+\.\d+`,
	}
}

// RegEx generates the CurrentDS command regex
func (c *CurrentDS) RegEx() string {
	return buildDataSetRegex(c.Keys(), c.ValueRegEx(), true, []string{})
}

// MonitorRegEx generates the CurrentDS regex without capture groups.
func (c *CurrentDS) MonitorRegEx() string {
	return buildDataSetRegex(c.Keys(), c.ValueRegEx(), false, []string{})
}

// Keys provides the keys method for the CurrentDS values
func (c *CurrentDS) Keys() []string {
	return []string{"stepsRemoved", "offsetFromMaster", "meanPathDelay"}
}

// Update provides the Update method for the CurrentDS values
func (c *CurrentDS) Update(key string, value string) {
	switch key {
	case "stepsRemoved":
		c.StepsRemoved = stou16(value)
	case "offsetFromMaster":
		c.offsetFromMaster = stof64(value)
	case "meanPathDelay":
		c.meanPathDelay = stof64(value)
	}
}

func (c *CurrentDS) String() string {
	if c == nil {
		glog.Error("returned empty SubscribedEvents")
		return ""
	}
	result := fmt.Sprintf(" stepsRemoved     %d\n", c.StepsRemoved)
	result += fmt.Sprintf(" offsetFromMaster %.1f\n", c.offsetFromMaster)
	result += fmt.Sprintf(" meanPathDelay    %.1f\n", c.meanPathDelay)
	return result
}

// ValueRegEx provides the regex method for the CurrentDS values matching
func (tp *TimePropertiesDS) ValueRegEx() map[string]string {
	return map[string]string{
		"currentUtcOffset":      `\d+`,
		"currentUtcOffsetValid": `[01]`,
		"leap59":                `[01]`,
		"leap61":                `[01]`,
		"timeTraceable":         `[01]`,
		"frequencyTraceable":    `[01]`,
		"ptpTimescale":          `[01]`,
		"timeSource":            `0x[\da-f]+`,
	}
}

// RegEx generates the TimePropertiesDS command regex
func (tp *TimePropertiesDS) RegEx() string {
	return buildDataSetRegex(tp.Keys(), tp.ValueRegEx(), true, []string{})
}

// MonitorRegEx generates the TimePropertiesDS regex without capture groups.
func (tp *TimePropertiesDS) MonitorRegEx() string {
	return buildDataSetRegex(tp.Keys(), tp.ValueRegEx(), false, []string{})
}

// Keys provides the keys method for the TimePropertiesDS values
func (tp *TimePropertiesDS) Keys() []string {
	return []string{"currentUtcOffset", "leap61", "leap59", "currentUtcOffsetValid", "ptpTimescale",
		"timeTraceable", "frequencyTraceable", "timeSource"}
}

// Update provides the Update method for the TimePropertiesDS values
func (tp *TimePropertiesDS) Update(key string, value string) {
	switch key {
	case "currentUtcOffset":
		tp.CurrentUtcOffset = stoi32(value)
	case "currentUtcOffsetValid":
		tp.CurrentUtcOffsetValid = stob(value)
	case "leap59":
		tp.Leap59 = stob(value)
	case "leap61":
		tp.Leap61 = stob(value)
	case "timeTraceable":
		tp.TimeTraceable = stob(value)
	case "frequencyTraceable":
		tp.FrequencyTraceable = stob(value)
	case "ptpTimescale":
		tp.PtpTimescale = stob(value)
	case "timeSource":
		tp.TimeSource = protocol.TimeSource(stou8h(value))
	}
}

func (tp *TimePropertiesDS) String() string {
	if tp == nil {
		glog.Error("returned empty TimePropertiesDS")
		return ""
	}
	result := fmt.Sprintf(" currentUtcOffset        %d\n", tp.CurrentUtcOffset)
	result += fmt.Sprintf(" currentUtcOffsetValid   %d\n", btoi(tp.CurrentUtcOffsetValid))
	result += fmt.Sprintf(" leap59                  %d\n", btoi(tp.Leap59))
	result += fmt.Sprintf(" leap61                  %d\n", btoi(tp.Leap61))
	result += fmt.Sprintf(" timeTraceable           %d\n", btoi(tp.TimeTraceable))
	result += fmt.Sprintf(" frequencyTraceable      %d\n", btoi(tp.FrequencyTraceable))
	result += fmt.Sprintf(" ptpTimescale            %d\n", btoi(tp.PtpTimescale))
	result += fmt.Sprintf(" timeSource              0x%x\n", uint(tp.TimeSource))
	return result
}

// SubscribedEvents represents the subscription events configuration for PTP notifications.
type SubscribedEvents struct {
	Duration            int32
	NotifyPortState     bool
	NotifyTimeSync      bool
	NotifyParentDataSet bool
	NotifyCmlds         bool
}

// ValueRegEx provides the regex method for the SubscribedEvents values matching
func (se *SubscribedEvents) ValueRegEx() map[string]string {
	return map[string]string{
		"duration":               `-?\d+`,
		"NOTIFY_PORT_STATE":      `on|off`,
		"NOTIFY_TIME_SYNC":       `on|off`,
		"NOTIFY_PARENT_DATA_SET": `on|off`,
		"NOTIFY_CMLDS":           `on|off`,
	}
}

// RegEx generates the SubscribedEvents command regex
func (se *SubscribedEvents) RegEx() string {
	return buildDataSetRegex(se.Keys(), se.ValueRegEx(), true, []string{"NOTIFY_CMLDS"})
}

// MonitorRegEx generates the SubscribedEvents regex without capture groups.
func (se *SubscribedEvents) MonitorRegEx() string {
	return buildDataSetRegex(se.Keys(), se.ValueRegEx(), false, []string{"NOTIFY_CMLDS"})
}

// Keys provides the keys method for the SubscribedEvents values
func (se *SubscribedEvents) Keys() []string {
	return []string{
		"duration",
		"NOTIFY_PORT_STATE",
		"NOTIFY_TIME_SYNC",
		"NOTIFY_PARENT_DATA_SET",
		"NOTIFY_CMLDS",
	}
}

// Update provides the Update method for the SubscribedEvents values
func (se *SubscribedEvents) Update(key string, value string) {
	switch key {
	case "duration":
		se.Duration = stoi32(value)
	case "NOTIFY_PORT_STATE":
		se.NotifyPortState = ootob(value)
	case "NOTIFY_TIME_SYNC":
		se.NotifyTimeSync = ootob(value)
	case "NOTIFY_PARENT_DATA_SET":
		se.NotifyParentDataSet = ootob(value)
	case "NOTIFY_CMLDS":
		se.NotifyCmlds = ootob(value)
	}
}

func (se *SubscribedEvents) String() string {
	if se == nil {
		glog.Error("returned empty SubscribedEvents")
		return ""
	}
	result := fmt.Sprintf(" duration               %d\n", se.Duration)
	result += fmt.Sprintf(" NOTIFY_PORT_STATE      %s\n", btooo(se.NotifyPortState))
	result += fmt.Sprintf(" NOTIFY_TIME_SYNC       %s\n", btooo(se.NotifyTimeSync))
	result += fmt.Sprintf(" NOTIFY_PARENT_DATA_SET %s\n", btooo(se.NotifyParentDataSet))
	result += fmt.Sprintf(" NOTIFY_CMLDS           %s\n", btooo(se.NotifyCmlds))
	return result
}

// ProcessMessage parses PMC output matches into a DataSet structure.
func ProcessMessage[P any, T interface {
	*P
	DataSet
}](matches []string) (T, error) {
	var result T = new(P)
	keys := result.Keys()
	if len(matches)-1 < len(keys) {
		return result, fmt.Errorf("short match expected=%d found=%d", len(keys), len(matches)-1)
	}

	for i, m := range matches[1:] {
		if i < len(keys) {
			result.Update(keys[i], m)
		}
	}

	return result, nil
}
