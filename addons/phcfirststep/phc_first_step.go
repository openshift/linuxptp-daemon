package phcfirststep

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/golang/glog"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/network"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/plugin"
	ptpv1 "github.com/k8snetworkplumbingwg/ptp-operator/api/v1"
)

const (
	pluginName                    = "phc-first-step"
	globalSectionName             = "global"
	nmeaSectionName               = "nmea"
	unicastMasterTableSectionName = "unicast_master_table"
)

const measurementSamples = 1

const nanosecondsPerSecond int64 = 1_000_000_000

const residualOffsetThreshold int64 = nanosecondsPerSecond

type pluginOptions struct {
	Timeout string `json:"timeout,omitempty"`
}

var (
	clockTimePattern = regexp.MustCompile(`clock time is\s+([+-]?[0-9]+(?:\.[0-9]+)?)`)
	masterOffsetRE   = regexp.MustCompile(`master offset\s+([+-]?\d+)\s+s\d+\s+freq\s+[+-]?\d+\s+path delay\s+[+-]?0*[1-9]\d*`)
	phcIDForIface    = network.GetPhcId
)

func onPTPConfigChange(_ *interface{}, profile *ptpv1.PtpProfile) error {
	if profile == nil {
		return nil
	}
	if _, selected := profile.Plugins[pluginName]; !selected {
		return nil
	}

	name := profileName(profile)
	ifaces := timeReceiverInterfaces(profile)
	glog.Infof("phc-first-step started: profile=%s interfaces=%v", name, ifaces)
	if len(ifaces) == 0 {
		return logFailure(profile, fmt.Errorf("profile %s has no TR interfaces with masterOnly 0", name))
	}
	timeout, err := measurementTimeout(profile)
	if err != nil {
		return logFailure(profile, err)
	}
	phc, err := sharedPHC(ifaces)
	if err != nil {
		return logFailure(profile, err)
	}
	if err = validateE825PHC(profile, phc); err != nil {
		return logFailure(profile, err)
	}
	glog.Infof("phc-first-step resolved: profile=%s interfaces=%v PHC=%s", name, ifaces, phc)
	if err = updatePHC(name, profile, ifaces, phc, timeout); err != nil {
		return logFailure(profile, err)
	}
	glog.Infof("phc-first-step completed: profile=%s interfaces=%v PHC=%s", name, ifaces, phc)
	return nil
}

func timeReceiverInterfaces(profile *ptpv1.PtpProfile) []string {
	if profile == nil || profile.Ptp4lConf == nil {
		return nil
	}
	var interfaces []string
	seen := make(map[string]bool)
	section := ""
	for _, rawLine := range strings.Split(*profile.Ptp4lConf, "\n") {
		line := strings.TrimSpace(rawLine)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = strings.TrimSuffix(strings.TrimPrefix(line, "["), "]")
			switch section {
			case globalSectionName, nmeaSectionName, unicastMasterTableSectionName:
				section = ""
			}
			continue
		}
		if section == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[0] == "masterOnly" && fields[1] == "0" && !seen[section] {
			interfaces = append(interfaces, section)
			seen[section] = true
		}
	}
	return interfaces
}

func validateE825PHC(profile *ptpv1.PtpProfile, trPHC string) error {
	if profile == nil || profile.Plugins == nil {
		return fmt.Errorf("profile has no e825 plugin configuration for TR PHC %s", trPHC)
	}
	raw, ok := profile.Plugins["e825"]
	if !ok || raw == nil {
		return fmt.Errorf("profile e825 plugin configuration is required for TR PHC %s", trPHC)
	}
	var opts struct {
		Devices []string `json:"devices"`
	}
	if err := json.Unmarshal(raw.Raw, &opts); err != nil {
		return fmt.Errorf("decode e825 devices for profile %s: %w", profileName(profile), err)
	}
	for _, device := range opts.Devices {
		if phcIDForIface(device) == trPHC {
			return nil
		}
	}
	return fmt.Errorf("no profile e825.devices entry exposes the TR PHC %s", trPHC)
}

func sharedPHC(interfaces []string) (string, error) {
	var shared string
	for _, iface := range interfaces {
		phc := phcIDForIface(iface)
		if phc == "" {
			return "", fmt.Errorf("could not determine PHC device for TR interface %q", iface)
		}
		if shared == "" {
			shared = phc
			continue
		}
		if phc != shared {
			return "", fmt.Errorf("TR interfaces resolve to different PHCs: %s and %s", shared, phc)
		}
	}
	if shared == "" {
		return "", fmt.Errorf("no TR interfaces provided for PHC discovery")
	}
	return shared, nil
}

func measurementTimeout(profile *ptpv1.PtpProfile) (time.Duration, error) {
	if profile == nil || profile.Plugins == nil {
		return 0, nil
	}
	raw, ok := profile.Plugins[pluginName]
	if !ok || raw == nil || len(raw.Raw) == 0 {
		return 0, nil
	}
	var opts pluginOptions
	if err := json.Unmarshal(raw.Raw, &opts); err != nil {
		return 0, fmt.Errorf("decode %s options: %w", pluginName, err)
	}
	if opts.Timeout == "" {
		return 0, nil
	}
	timeout, err := time.ParseDuration(opts.Timeout)
	if err != nil {
		return 0, fmt.Errorf("invalid %s timeout %q: %w", pluginName, opts.Timeout, err)
	}
	if timeout < 0 {
		return 0, fmt.Errorf("invalid %s timeout %q: must not be negative", pluginName, opts.Timeout)
	}
	return timeout, nil
}

func parseSample(line string) (int64, bool) {
	match := masterOffsetRE.FindStringSubmatch(line)
	if match == nil {
		return 0, false
	}
	offset, err := strconv.ParseInt(match[1], 10, 64)
	if err != nil {
		return 0, false
	}
	return offset, true
}

func updatePHC(name string, profile *ptpv1.PtpProfile, interfaces []string, phc string, timeout time.Duration) error {
	for adjustment := 0; adjustment < 2; adjustment++ {
		offset, err := measureOffset(context.Background(), profile, interfaces, timeout)
		if err != nil {
			return fmt.Errorf("measure PHC offset for interfaces %v: %w", interfaces, err)
		}
		glog.Infof("phc-first-step measurement complete: profile=%s interfaces=%v PHC=%s samples=%d offset=%d ns", name, interfaces, phc, measurementSamples, offset)
		if adjustment > 0 && offset <= residualOffsetThreshold && offset >= -residualOffsetThreshold {
			glog.Infof("phc-first-step residual offset within threshold: profile=%s PHC=%s latestOffset=%d ns threshold=%d ns", name, phc, offset, residualOffsetThreshold)
			return nil
		}
		phcTime, err := readPHCTime(context.Background(), phc)
		if err != nil {
			return fmt.Errorf("read PHC %s: %w", phc, err)
		}
		corrected, err := correctedTime(phcTime, offset)
		if err != nil {
			return fmt.Errorf("calculate corrected PHC time for %s: %w", phc, err)
		}
		glog.Infof("phc-first-step correction calculated: profile=%s PHC=%s currentPHC=%d ns targetPHC=%d ns", name, phc, phcTime, corrected)
		if err = setPHCTime(context.Background(), phc, corrected); err != nil {
			return fmt.Errorf("set PHC %s: %w", phc, err)
		}
		glog.Infof("phc-first-step adjustment applied: profile=%s interfaces=%v PHC=%s adjustment=%d offset=%d ns", name, interfaces, phc, adjustment+1, offset)
	}
	return nil
}

func measureOffset(ctx context.Context, profile *ptpv1.PtpProfile, interfaces []string, timeout time.Duration) (int64, error) {
	measureCtx := ctx
	if timeout > 0 {
		var cancel context.CancelFunc
		measureCtx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	configFile, err := os.CreateTemp("", "phc-first-step-*.conf")
	if err != nil {
		return 0, fmt.Errorf("create temporary ptp4l config: %w", err)
	}
	path := configFile.Name()
	defer os.Remove(path)
	content, err := renderMeasurementConfig(profile, interfaces)
	if err != nil {
		_ = configFile.Close()
		return 0, err
	}
	if _, err = fmt.Fprint(configFile, content); err != nil {
		_ = configFile.Close()
		return 0, fmt.Errorf("write temporary ptp4l config: %w", err)
	}
	if err = configFile.Close(); err != nil {
		return 0, fmt.Errorf("close temporary ptp4l config: %w", err)
	}
	glog.Infof("phc-first-step measurement config: path=%s\n%s", path, content)

	args := []string{"-f", path}
	for _, iface := range interfaces {
		args = append(args, "-i", iface)
	}
	args = append(args, "-m", "--free_running=1", "--freq_est_interval=-4", "--summary_interval=-4")
	samples := make([]int64, 0, measurementSamples)
	_, err = commandOutputUntil(measureCtx, func(line string) bool {
		if len(samples) < measurementSamples {
			if offset, valid := parseSample(line); valid {
				samples = append(samples, offset)
			}
		}
		return len(samples) >= measurementSamples
	}, "ptp4l", args...)
	if len(samples) < measurementSamples {
		if timeout > 0 && errors.Is(measureCtx.Err(), context.DeadlineExceeded) {
			return 0, fmt.Errorf("timed out after %s waiting for %d valid ptp4l offset samples (received %d)", timeout, measurementSamples, len(samples))
		}
		if err != nil {
			return 0, fmt.Errorf("ptp4l measurement failed after %d of %d valid samples: %w", len(samples), measurementSamples, err)
		}
		return 0, fmt.Errorf("ptp4l exited after %d of %d valid offset samples", len(samples), measurementSamples)
	}

	return samples[len(samples)-1], nil
}

func renderMeasurementConfig(profile *ptpv1.PtpProfile, interfaces []string) (string, error) {
	if profile == nil || profile.Ptp4lConf == nil {
		return "", fmt.Errorf("profile has no ptp4l configuration")
	}
	wanted := make(map[string]bool, len(interfaces))
	for _, iface := range interfaces {
		wanted[iface] = true
	}
	found := make(map[string]bool, len(interfaces))
	interfaceOptions := make(map[string][]string, len(interfaces))
	var globalOptions []string
	domain := "0"
	section := ""
	for _, rawLine := range strings.Split(*profile.Ptp4lConf, "\n") {
		line := strings.TrimSpace(rawLine)
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = strings.TrimSuffix(strings.TrimPrefix(line, "["), "]")
			if section == globalSectionName {
				continue
			}
			if section == nmeaSectionName || section == unicastMasterTableSectionName || !wanted[section] {
				section = ""
				continue
			}
			found[section] = true
			continue
		}
		if line == "" || strings.HasPrefix(line, "#") || section == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) > 0 {
			switch fields[0] {
			case "free_running", "slaveOnly", "uds_address", "uds_ro_address":
				continue
			case "masterOnly":
				if section == globalSectionName {
					continue
				}
			case "domainNumber":
				if section == globalSectionName && len(fields) == 2 {
					domain = fields[1]
				}
				continue
			}
		}
		if section == globalSectionName {
			globalOptions = append(globalOptions, line)
		} else {
			interfaceOptions[section] = append(interfaceOptions[section], line)
		}
	}
	for _, iface := range interfaces {
		if !found[iface] {
			return "", fmt.Errorf("profile has no configuration section for TR interface %q", iface)
		}
	}
	lines := []string{
		"[" + globalSectionName + "]",
		"slaveOnly 1",
		"free_running 1",
		"domainNumber " + domain,
		"summary_interval -4",
		fmt.Sprintf("uds_address /tmp/phc-first-step-%d.socket", os.Getpid()),
	}
	lines = append(lines, globalOptions...)
	for _, iface := range interfaces {
		lines = append(lines, "["+iface+"]")
		lines = append(lines, interfaceOptions[iface]...)
	}
	return strings.Join(lines, "\n") + "\n", nil
}

func readPHCTime(ctx context.Context, phc string) (int64, error) {
	out, err := commandOutput(ctx, "phc_ctl", phc, "--", "get")
	if err != nil {
		return 0, err
	}
	match := clockTimePattern.FindStringSubmatch(out)
	if match == nil {
		return 0, fmt.Errorf("could not parse phc_ctl output")
	}
	return phcSecondsToNS(match[1])
}

func setPHCTime(ctx context.Context, phc string, value int64) error {
	_, err := commandOutput(ctx, "phc_ctl", phc, "--", "set", formatNS(value))
	return err
}

func correctedTime(phcTime, offset int64) (int64, error) {
	if offset > 0 && phcTime < math.MinInt64+offset || offset < 0 && phcTime > math.MaxInt64+offset {
		return 0, fmt.Errorf("time correction overflows int64")
	}
	return phcTime - offset, nil
}

func phcSecondsToNS(value string) (int64, error) {
	parts := strings.SplitN(value, ".", 2)
	seconds, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return 0, err
	}
	frac := ""
	if len(parts) == 2 {
		frac = parts[1]
	}
	frac = (frac + "000000000")[:9]
	nanoseconds, err := strconv.ParseInt(frac, 10, 64)
	if err != nil {
		return 0, err
	}
	minSeconds := math.MinInt64 / nanosecondsPerSecond
	maxSeconds := math.MaxInt64 / nanosecondsPerSecond
	if seconds < minSeconds-1 || seconds > maxSeconds {
		return 0, fmt.Errorf("PHC time %q overflows nanoseconds", value)
	}
	if seconds == minSeconds-1 {
		minNanoseconds := nanosecondsPerSecond + math.MinInt64%nanosecondsPerSecond
		if nanoseconds < minNanoseconds {
			return 0, fmt.Errorf("PHC time %q overflows nanoseconds", value)
		}
		return math.MinInt64 + nanoseconds - minNanoseconds, nil
	}
	base := seconds * nanosecondsPerSecond
	if base > math.MaxInt64-nanoseconds {
		return 0, fmt.Errorf("PHC time %q overflows nanoseconds", value)
	}
	return base + nanoseconds, nil
}

func formatNS(value int64) string {
	seconds := value / nanosecondsPerSecond
	nanoseconds := value % nanosecondsPerSecond
	if nanoseconds < 0 {
		seconds--
		nanoseconds += nanosecondsPerSecond
	}
	return fmt.Sprintf("%d.%09d", seconds, nanoseconds)
}

func commandOutput(ctx context.Context, name string, args ...string) (string, error) {
	return commandOutputUntil(ctx, nil, name, args...)
}

func commandOutputUntil(ctx context.Context, onLine func(string) bool, name string, args ...string) (string, error) {
	glog.Infof("phc-first-step executing: %s %s", name, strings.Join(args, " "))
	commandCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	cmd := exec.CommandContext(commandCtx, name, args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", fmt.Errorf("create stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return "", fmt.Errorf("create stderr pipe: %w", err)
	}
	if err = cmd.Start(); err != nil {
		return "", err
	}

	var output strings.Builder
	var outputMu sync.Mutex
	var lineMu sync.Mutex
	var wg sync.WaitGroup
	scan := func(reader *bufio.Scanner) {
		defer wg.Done()
		for reader.Scan() {
			line := reader.Text()
			glog.Infof("phc-first-step output: %s", line)
			outputMu.Lock()
			output.WriteString(line)
			output.WriteByte('\n')
			outputMu.Unlock()
			if onLine != nil {
				lineMu.Lock()
				stop := onLine(line)
				lineMu.Unlock()
				if stop {
					cancel()
				}
			}
		}
	}
	wg.Add(2)
	go scan(bufio.NewScanner(stdout))
	go scan(bufio.NewScanner(stderr))
	wg.Wait()
	err = cmd.Wait()
	return output.String(), err
}

func profileName(profile *ptpv1.PtpProfile) string {
	if profile != nil && profile.Name != nil {
		return *profile.Name
	}
	return "unknown"
}

func logFailure(profile *ptpv1.PtpProfile, err error) error {
	glog.Errorf("phc-first-step failed for profile %s: %v", profileName(profile), err)
	return err
}

// New creates the independently selectable phc-first-step plugin.
func New(name string) (*plugin.Plugin, *interface{}) {
	if name != pluginName {
		glog.Errorf("plugin must be initialized as %q", pluginName)
		return nil, nil
	}
	pluginObject := plugin.Plugin{
		Name:              pluginName,
		OnPTPConfigChange: onPTPConfigChange,
	}
	var data interface{}
	return &pluginObject, &data
}
