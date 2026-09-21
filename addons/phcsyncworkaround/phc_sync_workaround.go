package phcsyncworkaround

import (
	"bufio"
	"context"
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
	measurementTimeout = 15 * time.Second
	commandTimeout     = 30 * time.Second
	measurementSamples = 16
)

var (
	clockTimePattern  = regexp.MustCompile(`clock time is\s+([0-9]+(?:\.[0-9]+)?)`)
	masterOffsetRegex = regexp.MustCompile(`master offset\s+([+-]?\d+)\s+s\d+\s+freq\s+[+-]?\d+\s+path delay\s+([+-]?\d+)`)
)

func onPTPConfigChange(_ *interface{}, profile *ptpv1.PtpProfile) error {
	iface, err := timeReceiverInterface(profile)
	if err != nil {
		return err
	}
	if iface == "" {
		return nil
	}
	return updatePHC(profileName(profile), profile, iface)
}

func profileName(profile *ptpv1.PtpProfile) string {
	if profile != nil && profile.Name != nil {
		return *profile.Name
	}
	return "unknown"
}

// timeReceiverInterface returns the profile's PTP time receiver interface, i.e.
// the single interface section configured with masterOnly=0. It returns an empty
// string when the profile is not a time receiver, and an error when more than
// one time receiver port is configured.
func timeReceiverInterface(profile *ptpv1.PtpProfile) (string, error) {
	ports := hardwareconfig.UpstreamPortsFromPtpProfile(profile)
	switch len(ports) {
	case 0:
		return "", nil
	case 1:
		return ports[0], nil
	default:
		return "", fmt.Errorf("profile %s has multiple time receiver ports: %v", profileName(profile), ports)
	}
}

func updatePHC(profileName string, profile *ptpv1.PtpProfile, iface string) error {
	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	defer cancel()

	glog.Infof("PHC sync workaround started: profile=%s interface=%s", profileName, iface)
	dev := network.GetPhcId(iface)
	if dev == "" {
		return fmt.Errorf("could not determine PHC device for interface %q", iface)
	}
	glog.Infof("PHC sync workaround resolved: profile=%s interface=%s phc=%s", profileName, iface, dev)

	offset, err := measureOffset(ctx, profile, iface)
	if err != nil {
		return fmt.Errorf("measure PHC offset for %s: %w", iface, err)
	}
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
	glog.Infof("PHC sync workaround completed: profile=%s interface=%s phc=%s", profileName, iface, dev)
	return nil
}

func measureOffset(ctx context.Context, profile *ptpv1.PtpProfile, iface string) (int64, error) {
	measureCtx, cancel := context.WithTimeout(ctx, measurementTimeout)
	defer cancel()
	config, err := os.CreateTemp("", "phc-sync-*.conf")
	if err != nil {
		return 0, err
	}
	path := config.Name()
	defer os.Remove(path)
	content, err := renderMeasurementConfig(profile, iface)
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
	var samples []int64
	_, err = commandOutputUntil(measureCtx, func(line string) bool {
		match := masterOffsetRegex.FindStringSubmatch(line)
		if match == nil || match[2] == "0" {
			return false
		}
		value, parseErr := strconv.ParseInt(match[1], 10, 64)
		if parseErr == nil {
			samples = append(samples, value)
		}
		return len(samples) >= measurementSamples
	}, "ptp4l", "-f", path, "-i", iface, "-m", "--free_running=1", "--freq_est_interval=-4", "--summary_interval=-4")
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
// domainNumber and the telecom dataset/transport settings) and the selected TR
// interface section, then overrides the options that define the measurement
// session (free-running, slave-only, fast summaries, unique UDS address).
func renderMeasurementConfig(profile *ptpv1.PtpProfile, iface string) (string, error) {
	if profile == nil || profile.Ptp4lConf == nil {
		return "", fmt.Errorf("profile has no ptp4l configuration")
	}

	var globalOptions []string
	var interfaceOptions []string
	domain := "0"
	section := ""
	foundInterface := false
	for _, rawLine := range strings.Split(*profile.Ptp4lConf, "\n") {
		line := strings.TrimSpace(rawLine)
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = strings.TrimSuffix(strings.TrimPrefix(line, "["), "]")
			if section == iface {
				foundInterface = true
			}
			continue
		}
		if section != "global" && section != iface {
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
			interfaceOptions = append(interfaceOptions, line)
		}
	}
	if !foundInterface {
		return "", fmt.Errorf("profile has no configuration section for TR interface %q", iface)
	}

	global := []string{
		"[global]",
		"slaveOnly 1",
		"free_running 1",
		"domainNumber " + domain,
		"summary_interval -4",
		fmt.Sprintf("uds_address /tmp/phc-sync-%d.socket", os.Getpid()),
	}
	global = append(global, globalOptions...)
	global = append(global, "["+iface+"]")
	global = append(global, interfaceOptions...)
	return strings.Join(global, "\n") + "\n", nil
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
