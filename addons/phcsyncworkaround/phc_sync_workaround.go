package phcsyncworkaround

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/golang/glog"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/hardwareconfig"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/network"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/plugin"
	ptpv1 "github.com/k8snetworkplumbingwg/ptp-operator/api/v1"
)

const pluginName = "phc-sync-workaround"

const (
	// measurementSamples is the number of valid offset samples (non-zero path
	// delay) to average before correcting the PHC.
	measurementSamples = 16
)

// pluginOptions is the per-profile configuration for the plugin, for example:
//
//	plugins:
//	  phc-sync-workaround:
//	    timeout: 15s
//
// timeout bounds the free-running ptp4l measurement phase. When unset (or zero)
// the measurement runs without a deadline.
type pluginOptions struct {
	Timeout string `json:"timeout,omitempty"`
}

// measurementTimeout returns the configured measurement timeout, or 0 for no
// deadline when the option is absent.
func measurementTimeout(profile *ptpv1.PtpProfile) (time.Duration, error) {
	if profile == nil || profile.Plugins == nil {
		return 0, nil
	}
	raw, ok := profile.Plugins[pluginName]
	if !ok || raw == nil {
		return 0, nil
	}
	data, err := json.Marshal(raw)
	if err != nil {
		return 0, fmt.Errorf("marshal %s options: %w", pluginName, err)
	}
	var opts pluginOptions
	if err := json.Unmarshal(data, &opts); err != nil {
		return 0, fmt.Errorf("unmarshal %s options: %w", pluginName, err)
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

var (
	clockTimePattern = regexp.MustCompile(`clock time is\s+([0-9]+(?:\.[0-9]+)?)`)
	// masterOffsetRegex matches ptp4l free-running summary lines that carry a
	// usable measurement. The path delay is required to be non-zero: before
	// delay measurement completes ptp4l emits bogus zero-delay rows, which must
	// not be counted as samples. RE2 has no lookahead, so a non-zero delay is
	// expressed as "at least one non-zero digit" ([+-]?0*[1-9]\d*).
	masterOffsetRegex = regexp.MustCompile(`master offset\s+([+-]?\d+)\s+s\d+\s+freq\s+[+-]?\d+\s+path delay\s+[+-]?0*[1-9]\d*`)
)

// parseSample extracts the master offset from a ptp4l summary line. It returns
// ok=false for lines that are not a usable measurement, i.e. anything without a
// non-zero path delay (the offset itself may legitimately be zero).
func parseSample(line string) (offset int64, ok bool) {
	match := masterOffsetRegex.FindStringSubmatch(line)
	if match == nil {
		return 0, false
	}
	offset, err := strconv.ParseInt(match[1], 10, 64)
	if err != nil {
		return 0, false
	}
	return offset, true
}

func onPTPConfigChange(_ *interface{}, profile *ptpv1.PtpProfile) error {
	ifaces := timeReceiverInterfaces(profile)
	if len(ifaces) == 0 {
		return nil
	}
	timeout, err := measurementTimeout(profile)
	if err != nil {
		return err
	}
	return updatePHC(profileName(profile), profile, ifaces, timeout)
}

func profileName(profile *ptpv1.PtpProfile) string {
	if profile != nil && profile.Name != nil {
		return *profile.Name
	}
	return "unknown"
}

// timeReceiverInterfaces returns the profile's PTP time receiver interfaces,
// i.e. every interface section configured with masterOnly=0, in profile order.
// An empty result means the profile is not a time receiver.
func timeReceiverInterfaces(profile *ptpv1.PtpProfile) []string {
	return hardwareconfig.UpstreamPortsFromPtpProfile(profile)
}

func updatePHC(profileName string, profile *ptpv1.PtpProfile, ifaces []string, timeout time.Duration) error {
	ctx := context.Background()

	glog.Infof("PHC sync workaround started: profile=%s interfaces=%v", profileName, ifaces)
	devs, err := phcDevices(ifaces)
	if err != nil {
		return err
	}
	glog.Infof("PHC sync workaround resolved: profile=%s interfaces=%v phc=%v", profileName, ifaces, devs)

	offset, err := measureOffset(ctx, profile, ifaces, timeout)
	if err != nil {
		return fmt.Errorf("measure PHC offset for %v: %w", ifaces, err)
	}
	for _, dev := range devs {
		phcTime, err := readPHCTime(ctx, dev)
		if err != nil {
			return fmt.Errorf("read PHC %s: %w", dev, err)
		}
		corrected, err := correctedTime(phcTime, offset)
		if err != nil {
			return fmt.Errorf("calculate corrected PHC time: %w", err)
		}
		if err := setPHCTime(ctx, dev, corrected); err != nil {
			return fmt.Errorf("set PHC %s: %w", dev, err)
		}
	}
	glog.Infof("PHC sync workaround completed: profile=%s interfaces=%v phc=%v", profileName, ifaces, devs)
	return nil
}

// phcDevices maps the time receiver interfaces to their PHC devices, keeping
// profile order and collapsing interfaces that share the same PHC.
func phcDevices(ifaces []string) ([]string, error) {
	seen := make(map[string]bool)
	var devs []string
	for _, iface := range ifaces {
		dev := network.GetPhcId(iface)
		if dev == "" {
			return nil, fmt.Errorf("could not determine PHC device for interface %q", iface)
		}
		if !seen[dev] {
			seen[dev] = true
			devs = append(devs, dev)
		}
	}
	return devs, nil
}

func measureOffset(ctx context.Context, profile *ptpv1.PtpProfile, ifaces []string, timeout time.Duration) (int64, error) {
	measureCtx := ctx
	if timeout > 0 {
		var cancel context.CancelFunc
		measureCtx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	config, err := os.CreateTemp("", "phc-sync-*.conf")
	if err != nil {
		return 0, err
	}
	path := config.Name()
	defer os.Remove(path)
	content, err := renderMeasurementConfig(profile, ifaces)
	if err != nil {
		config.Close()
		return 0, err
	}
	if _, err := fmt.Fprint(config, content); err != nil {
		config.Close()
		return 0, err
	}
	if err := config.Close(); err != nil {
		return 0, err
	}
	glog.Infof("PHC sync workaround ptp4l config: path=%s\n%s", path, content)
	args := []string{"-f", path}
	for _, iface := range ifaces {
		args = append(args, "-i", iface)
	}
	args = append(args, "-m", "--free_running=1", "--freq_est_interval=-4", "--summary_interval=-4")
	var samples []int64
	_, err = commandOutputUntil(measureCtx, func(line string) bool {
		if value, ok := parseSample(line); ok {
			samples = append(samples, value)
		}
		return len(samples) >= measurementSamples
	}, "ptp4l", args...)
	if err != nil && len(samples) < measurementSamples && measureCtx.Err() == nil {
		return 0, err
	}
	if len(samples) == 0 {
		return 0, fmt.Errorf("no valid master offset reported by ptp4l")
	}
	if len(samples) > measurementSamples {
		samples = samples[:measurementSamples]
	}
	var total int64
	for _, sample := range samples {
		total += sample
	}
	return total / int64(len(samples)), nil
}

// renderMeasurementConfig builds the free-running ptp4l configuration for the
// measurement phase. It copies the profile's [global] options (including
// domainNumber and the telecom dataset/transport settings) and every time
// receiver interface section, then overrides the options that define the
// measurement session (free-running, slave-only, fast summaries, unique UDS
// address).
func renderMeasurementConfig(profile *ptpv1.PtpProfile, ifaces []string) (string, error) {
	if profile == nil || profile.Ptp4lConf == nil {
		return "", fmt.Errorf("profile has no ptp4l configuration")
	}

	wanted := make(map[string]bool, len(ifaces))
	for _, iface := range ifaces {
		wanted[iface] = true
	}
	found := make(map[string]bool, len(ifaces))
	interfaceOptions := make(map[string][]string, len(ifaces))

	var globalOptions []string
	domain := "0"
	section := ""
	for _, rawLine := range strings.Split(*profile.Ptp4lConf, "\n") {
		line := strings.TrimSpace(rawLine)
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = strings.TrimSuffix(strings.TrimPrefix(line, "["), "]")
			continue
		}
		if section != "global" && !wanted[section] {
			continue
		}
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) > 0 {
			switch fields[0] {
			case "free_running", "slaveOnly", "uds_address", "uds_ro_address":
				continue
			case "domainNumber":
				if len(fields) == 2 {
					domain = fields[1]
				}
				continue
			}
		}
		if section == "global" {
			globalOptions = append(globalOptions, line)
		} else {
			found[section] = true
			interfaceOptions[section] = append(interfaceOptions[section], line)
		}
	}
	for _, iface := range ifaces {
		if !found[iface] {
			return "", fmt.Errorf("profile has no configuration section for TR interface %q", iface)
		}
	}

	lines := []string{
		"[global]",
		"slaveOnly 1",
		"free_running 1",
		"domainNumber " + domain,
		"summary_interval -4",
		fmt.Sprintf("uds_address /tmp/phc-sync-%d.socket", os.Getpid()),
	}
	lines = append(lines, globalOptions...)
	for _, iface := range ifaces {
		lines = append(lines, "["+iface+"]")
		lines = append(lines, interfaceOptions[iface]...)
	}
	return strings.Join(lines, "\n") + "\n", nil
}

func readPHCTime(ctx context.Context, dev string) (int64, error) {
	out, err := commandOutput(ctx, "phc_ctl", dev, "get")
	if err != nil {
		return 0, err
	}
	match := clockTimePattern.FindStringSubmatch(out)
	if match == nil {
		return 0, fmt.Errorf("could not parse phc_ctl output")
	}
	return phcSecondsToNS(match[1])
}

func setPHCTime(ctx context.Context, dev string, value int64) error {
	_, err := commandOutput(ctx, "phc_ctl", dev, "set", formatNS(value))
	return err
}

func correctedTime(phcTime, offset int64) (int64, error) {
	if (offset > 0 && phcTime < -offset) || (offset < 0 && phcTime > (1<<63-1)+offset) {
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
	return seconds*1e9 + nanoseconds, nil
}

func formatNS(value int64) string {
	return fmt.Sprintf("%d.%09d", value/1e9, value%1e9)
}

func commandOutput(ctx context.Context, name string, args ...string) (string, error) {
	return commandOutputUntil(ctx, nil, name, args...)
}

func commandOutputUntil(ctx context.Context, onLine func(string) bool, name string, args ...string) (string, error) {
	glog.Infof("PHC sync workaround executing: %s %s", name, strings.Join(args, " "))
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
	if err := cmd.Start(); err != nil {
		return "", err
	}

	var output strings.Builder
	var outputMu sync.Mutex
	var wg sync.WaitGroup
	scan := func(reader *bufio.Scanner) {
		defer wg.Done()
		for reader.Scan() {
			line := reader.Text()
			glog.Infof("PHC sync workaround output: %s", line)
			outputMu.Lock()
			output.WriteString(line)
			output.WriteByte('\n')
			outputMu.Unlock()
			if onLine != nil && onLine(line) {
				cancel()
			}
		}
	}

	wg.Add(2)
	go scan(bufio.NewScanner(stdout))
	go scan(bufio.NewScanner(stderr))
	err = cmd.Wait()
	wg.Wait()
	return output.String(), err
}

// New initializes the PHC synchronization workaround plugin.
func New(name string) (*plugin.Plugin, *interface{}) {
	if name != pluginName {
		glog.Errorf("Plugin must be initialized as '%s'", pluginName)
		return nil, nil
	}
	glog.Infof("registering %s plugin", pluginName)
	_plugin := plugin.Plugin{
		Name:              pluginName,
		OnPTPConfigChange: onPTPConfigChange,
	}
	var iface interface{}
	return &_plugin, &iface
}
