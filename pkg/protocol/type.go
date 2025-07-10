package protocol

import (
	"fmt"
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
		"clockClass":              `(\d+)`,
		"clockAccuracy":           `(0x[\da-f]+)`,
		"offsetScaledLogVariance": `(0x[\da-f]+)`,
		"currentUtcOffset":        `(\d+)`,
		"currentUtcOffsetValid":   `([01])`,
		"leap59":                  `([01])`,
		"leap61":                  `([01])`,
		"timeTraceable":           `([01])`,
		"frequencyTraceable":      `([01])`,
		"ptpTimescale":            `([01])`,
		"timeSource":              `(0x[\da-f]+)`,
	}
}

func (g *GrandmasterSettings) RegEx() string {
	result := ""
	for _, k := range g.Keys() {
		result += `[[:space:]]+` + k + `[[:space:]]+` + g.ValueRegEx()[k]
	}
	return result
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
	if s == "1" {
		return true
	}
	return false
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
		"parentPortIdentity":                    `(\d*\.\d*\.\d*\-\d*)`,
		"parentStats":                           `(\d+)`,
		"observedParentOffsetScaledLogVariance": `(0x[\da-f]+)`,
		"observedParentClockPhaseChangeRate":    `(0x[\da-f]+)`,
		"grandmasterPriority1":                  `(\d+)`,
		"gm.ClockClass":                         `(\d+)`,
		"gm.ClockAccuracy":                      `(0x[\da-f]+)`,
		"gm.OffsetScaledLogVariance":            `(0x[\da-f]+)`,
		"grandmasterPriority2":                  `(\d+)`,
		"grandmasterIdentity":                   `(\d*\.\d*\.\d*)`,
	}
}

// RegEx generates the ParentDataSet command regex
func (p *ParentDataSet) RegEx() string {
	result := ""
	for _, k := range p.Keys() {
		result += `[[:space:]]+` + k + `[[:space:]]+` + p.ValueRegEx()[k]
	}
	return result
}

// Keys provides the keys method for the ParentDS values
func (p *ParentDataSet) Keys() []string {
	return []string{"parentPortIdentity", "parentStats", "observedParentOffsetScaledLogVariance",
		"observedParentClockPhaseChangeRate", "grandmasterPriority1", "gm.ClockClass", "gm.ClockAccuracy",
		"gm.OffsetScaledLogVariance", "grandmasterPriority2", "grandmasterIdentity"}
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

// ValueRegEx provides the regex method for the ExternalGrandmasterProperties values matching
func (e *ExternalGrandmasterProperties) ValueRegEx() map[string]string {
	return map[string]string{
		"gmIdentity":   `(\d*\.\d*\.\d*)`,
		"stepsRemoved": `(\d+)`,
	}
}

// RegEx generates the ExternalGrandmasterProperties command regex
func (e *ExternalGrandmasterProperties) RegEx() string {
	result := ""
	for _, k := range e.Keys() {
		result += `[[:space:]]+` + k + `[[:space:]]+` + e.ValueRegEx()[k]
	}
	return result
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
		"stepsRemoved":     `(\d+)`,
		"offsetFromMaster": `(-?\d+\.\d+)`,
		"meanPathDelay":    `(\d+\.\d+)`,
	}
}

// RegEx generates the CurrentDS command regex
func (c *CurrentDS) RegEx() string {
	result := ""
	for _, k := range c.Keys() {
		result += `[[:space:]]+` + k + `[[:space:]]+` + c.ValueRegEx()[k]
	}
	return result
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

// ValueRegEx provides the regex method for the CurrentDS values matching
func (tp *TimePropertiesDS) ValueRegEx() map[string]string {
	return map[string]string{
		"currentUtcOffset":      `(\d+)`,
		"currentUtcOffsetValid": `([01])`,
		"leap59":                `([01])`,
		"leap61":                `([01])`,
		"timeTraceable":         `([01])`,
		"frequencyTraceable":    `([01])`,
		"ptpTimescale":          `([01])`,
		"timeSource":            `(0x[\da-f]+)`,
	}
}

// RegEx generates the TimePropertiesDS command regex
func (tp *TimePropertiesDS) RegEx() string {
	result := ""
	for _, k := range tp.Keys() {
		result += `[[:space:]]+` + k + `[[:space:]]+` + tp.ValueRegEx()[k]
	}
	return result
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
