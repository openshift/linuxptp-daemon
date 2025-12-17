package daemon

import (
	"bufio"
	"cmp"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/hardwareconfig"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/parser"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/synce"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/utils"
	ptpv2alpha1 "github.com/k8snetworkplumbingwg/ptp-operator/api/v2alpha1"

	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/config"

	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/dpll"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/leap"

	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/event"
	ptpnetwork "github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/network"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/plugin"

	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/logfilter"

	"github.com/golang/glog"
	"k8s.io/client-go/kubernetes"

	ptpv1 "github.com/k8snetworkplumbingwg/ptp-operator/api/v1"
)

const (
	PtpNamespace                    = "openshift-ptp"
	PTP4L_CONF_FILE_PATH            = "/etc/ptp4l.conf"
	PTP4L_CONF_DIR                  = "/ptp4l-conf"
	connectionRetryInterval         = 1 * time.Second
	eventSocket                     = "/cloud-native/events.sock"
	ClockClassChangeIndicator       = "selected best master clock"
	GPSDDefaultGNSSSerialPort       = "/dev/gnss0"
	NMEASourceDisabledIndicator     = "nmea source timed out"
	NMEASourceDisabledIndicator2    = "source ts not valid"
	InvalidMasterTimestampIndicator = "ignoring invalid master time stamp"
	PTP_HA_IDENTIFIER               = "haProfiles"
	HAInDomainIndicator             = "as domain source clock"
	HAOutOfDomainIndicator          = "as out-of-domain source"
	MessageTagSuffixSeperator       = ":"
	TBC                             = "T-BC"
	TGM                             = "T-GM"
	PtpSecretMountDir               = "/etc/ptp-secret-mount/"
)

var (
	haInDomainRegEx       = regexp.MustCompile(`selecting ([\w\-]+) as domain source clock`)
	haOutDomainRegEx      = regexp.MustCompile(`selecting ([\w\-]+) as out-of-domain source clock`)
	messageTagSuffixRegEx = regexp.MustCompile(`([a-zA-Z0-9]+\.[a-zA-Z0-9]+\.config):[a-zA-Z0-9]+(:[a-zA-Z0-9]+)?`)
	clockIDRegEx          = regexp.MustCompile(`\/dev\/ptp\d+`)
)

// SecurityFilesChanged is set by fsnotify when security files change.
// It is consumed by GetAndResetSecurityFilesChanged() which is called before UpdateConfig().
var SecurityFilesChanged atomic.Bool

// GetAndResetSecurityFilesChanged atomically reads and resets the SecurityFilesChanged flag.
// Returns true if security files have changed since the last call.
func GetAndResetSecurityFilesChanged() bool {
	return SecurityFilesChanged.CompareAndSwap(true, false)
}

var configPrefix = config.DefaultConfigPath

var ptpProcesses = []string{
	chronydProcessName, // there can be only one chronyd process in the system
	ts2phcProcessName,  // there can be only one ts2phc process in the system
	syncEProcessName,   // there can be only one synce Process per profile
	ptp4lProcessName,   // there could be more than one ptp4l in the system
	phc2sysProcessName, // there can be only one phc2sys process in the system
}

var ptpTmpFiles = []string{
	ts2phcProcessName,
	syncEProcessName,
	ptp4lProcessName,
	phc2sysProcessName,
	chronydProcessName,
	pmcSocketName,
}

var vTbcHasHardwareConfig = false

func dialSocket() (net.Conn, error) {
	c, err := net.Dial("unix", eventSocket)
	if err != nil {
		glog.Errorf("error trying to connect to event socket")
		time.Sleep(connectionRetryInterval)
	}
	return c, err
}

// ProcessManager manages a set of ptpProcess
// which could be ptp4l, phc2sys or timemaster.
// Processes in ProcessManager will be started
// or stopped simultaneously.
type ProcessManager struct {
	process         []*ptpProcess
	eventChannel    chan event.EventChannel
	ptpEventHandler *event.EventHandler
}

// NewProcessManager is used by unit tests
func NewProcessManager() *ProcessManager {
	processPTP := &ptpProcess{}
	processPTP.ptpClockThreshold = &ptpv1.PtpClockThreshold{
		HoldOverTimeout:    5,
		MaxOffsetThreshold: 100,
		MinOffsetThreshold: -100,
	}
	return &ProcessManager{
		process: []*ptpProcess{processPTP},
	}
}

// NewDaemonForTests is used by unit tests
func NewDaemonForTests(tracker *ReadyTracker, processManager *ProcessManager) *Daemon {
	tracker.processManager = processManager
	return &Daemon{
		readyTracker:   tracker,
		processManager: processManager,
	}
}

// SetTestProfileProcess ...
func (p *ProcessManager) SetTestProfileProcess(name string, ifaces config.IFaces, socketPath,
	processConfigPath string, nodeProfile ptpv1.PtpProfile) {
	p.process = append(p.process, &ptpProcess{
		name:              name,
		ifaces:            ifaces,
		processSocketPath: socketPath,
		processConfigPath: processConfigPath,
		execMutex:         sync.Mutex{},
		nodeProfile:       nodeProfile,
	})
}

// SetTestData is used by unit tests
func (p *ProcessManager) SetTestData(name, msgTag string, ifaces config.IFaces) {
	if len(p.process) < 1 || p.process[0] == nil {
		glog.Error("process is not initialized in SetTestData()")
		return
	}
	eventChannel := make(chan event.EventChannel)
	closeManager := make(chan bool)
	p.process[0].name = name
	p.process[0].messageTag = msgTag
	p.process[0].ifaces = ifaces
	p.process[0].logParser = getParser(name)
	p.process[0].handler = event.Init("test", false, eventSocket, eventChannel, closeManager, Offset, ClockState, ClockClassMetrics)
}

// RunProcessPTPMetrics is used by unit tests
func (p *ProcessManager) RunProcessPTPMetrics(log string) {
	if len(p.process) < 1 || p.process[0] == nil {
		glog.Error("process is not initialized in RunProcessPTPMetrics()")
		return
	}
	p.process[0].processPTPMetrics(log)
}

// RunSynceParser is used by unit tests
func (p *ProcessManager) RunSynceParser(log string) {
	if len(p.process) < 1 || p.process[0] == nil {
		glog.Error("process is not initialized in RunProcessPTPMetrics()")
		return
	}
	logEntry := synce.ParseLog(log)
	p.process[0].ProcessSynceEvents(logEntry)
}

// UpdateSynceConfig is used by unit tests
func (p *ProcessManager) UpdateSynceConfig(config *synce.Relations) {
	if len(p.process) < 1 || p.process[0] == nil {
		glog.Error("process is not initialized in RunProcessPTPMetrics()")
		return
	}
	p.process[0].syncERelations = config

}

// EmitProcessStatusLogs ...
func (p *ProcessManager) EmitProcessStatusLogs() {
	for _, proc := range p.process {
		status := PtpProcessUp
		if proc.Stopped() {
			status = PtpProcessDown
		}
		if proc.c == nil {
			for {
				var err error
				proc.c, err = dialSocket()
				if err == nil {
					break
				}
			}
		}
		logProcessStatus(proc.name, proc.configName, status, proc.c)
	}
}

// EmitClockClassLogs ...
func (p *ProcessManager) EmitClockClassLogs(c net.Conn) {
	for _, proc := range p.process {
		if proc.name == ptp4lProcessName {
			for _, dp := range proc.depProcess {
				if dp.Name() == PMCProcessName {
					pmc := dp.(*PMCProcess)
					if pmc.parentDS != nil {
						// if parentDS is nil that means the clock class will
						// be announced as soon as we get one
						// therefore no need force it.
						pmc.EmitClockClassLogs(c)
					}
				}
			}
		}
	}
}

type tBCProcessAttributes struct {
	controlledPortsConfigFile string
	// Time receiver interface name for T-BC clock monitoring
	trIfaceName string
}

type ptpProcess struct {
	name                  string
	ifaces                config.IFaces
	processSocketPath     string
	processConfigPath     string
	configName            string
	messageTag            string
	eventCh               chan event.EventChannel
	exitCh                chan bool
	execMutex             sync.Mutex
	stopped               bool
	logFilters            []*logfilter.LogFilter // List of filters to apply to logs
	cmd                   *exec.Cmd
	depProcess            []process // these are list of dependent process which needs to be started/stopped if the parent process is starts/stops
	nodeProfile           ptpv1.PtpProfile
	logParser             parser.MetricsExtractor
	lastTransitionResult  event.PTPState
	clockType             event.ClockType
	ptpClockThreshold     *ptpv1.PtpClockThreshold
	haProfile             map[string][]string // stores list of interface name for each profile
	syncERelations        *synce.Relations
	c                     net.Conn
	hasCollectedMetrics   bool
	tBCAttributes         tBCProcessAttributes
	GrandmasterClockClass uint8
	handler               *event.EventHandler
	dn                    *Daemon
	cmdSetEnabledMutex    sync.Mutex
	tbcStateDetector      *hardwareconfig.PTPStateDetector // Cached PTP state detector instance
}

func (p *ptpProcess) Stopped() bool {
	p.execMutex.Lock()
	me := p.stopped
	p.execMutex.Unlock()
	return me
}

func (p *ptpProcess) getAndSetStopped(val bool) bool {
	p.execMutex.Lock()
	ret := p.stopped
	p.stopped = val
	p.execMutex.Unlock()
	return ret
}

func (p *ptpProcess) setStopped(val bool) {
	p.execMutex.Lock()
	p.stopped = val
	p.execMutex.Unlock()
}

// Daemon is the main structure for linuxptp instance.
// It contains all the necessary data to run linuxptp instance.
type Daemon struct {
	// node name where daemon is running
	nodeName  string
	namespace string
	// write logs to socket, this will also send metrics to the socket
	stdoutToSocket bool

	// kubeClient allows interaction with Kubernetes, including the node we are running on.
	kubeClient *kubernetes.Clientset

	ptpUpdate *LinuxPTPConfUpdate

	processManager *ProcessManager
	readyTracker   *ReadyTracker

	hwconfigs *[]ptpv1.HwConfig

	// Hardware config manager handles hardware configurations from HardwareConfig CRs
	hardwareConfigManager *hardwareconfig.HardwareConfigManager

	refreshNodePtpDevice *bool

	// channel ensure LinuxPTP.Run() exit when main function exits.
	// stopCh is created by main function and passed by Daemon via NewLinuxPTP()
	stopCh <-chan struct{}

	pmcPollInterval int

	// Allow vendors to include plugins
	pluginManager plugin.PluginManager
	saFileWatcher *fsnotify.Watcher
}

// UpdateHardwareConfig implements controller.HardwareConfigUpdateHandler.
// It is invoked by the controller reconciler via HardwareConfigHandler
// (wired in cmd/main.go) to push the effective hardware configuration
// into the running daemon. The daemon forwards the update to its
// HardwareConfigManager which resolves and caches DPLL/sysfs commands.
func (dn *Daemon) UpdateHardwareConfig(hwConfigs []ptpv2alpha1.HardwareConfig) error {
	if dn.hardwareConfigManager == nil {
		return fmt.Errorf("hardware config manager not initialized")
	}
	return dn.hardwareConfigManager.UpdateHardwareConfig(hwConfigs)
}

// getHoldoverParameters retrieves holdover parameters from HardwareConfig for a specific clock ID
// Returns nil if no hardware config is available or no parameters are configured for the clock
func (dn *Daemon) getHoldoverParameters(profileName string, clockID uint64) *ptpv2alpha1.HoldoverParameters {
	if dn.hardwareConfigManager == nil {
		return nil
	}
	return dn.hardwareConfigManager.GetHoldoverParameters(profileName, clockID)
}

// New LinuxPTP is called by daemon to generate new linuxptp instance
func New(
	nodeName string,
	namespace string,
	stdoutToSocket bool,
	kubeClient *kubernetes.Clientset,
	ptpUpdate *LinuxPTPConfUpdate,
	stopCh <-chan struct{},
	plugins []string,
	hwconfigs *[]ptpv1.HwConfig,
	refreshNodePtpDevice *bool,
	closeManager chan bool,
	pmcPollInterval int,
	tracker *ReadyTracker,
) *Daemon {
	if !stdoutToSocket {
		RegisterMetrics(nodeName)
	}
	InitializeOffsetMaps()
	pluginManager := registerPlugins(plugins)
	eventChannel := make(chan event.EventChannel, 100)
	pm := &ProcessManager{
		process:         nil,
		eventChannel:    eventChannel,
		ptpEventHandler: event.Init(nodeName, stdoutToSocket, eventSocket, eventChannel, closeManager, Offset, ClockState, ClockClassMetrics),
	}
	tracker.processManager = pm

	// Initialize fsnotify watcher for sa_file change detection
	saFileWatch, err := fsnotify.NewWatcher()
	if err != nil {
		glog.Errorf("Failed to create fsnotify watcher for sa_file monitoring: %v", err)
		glog.Warning("sa_file change detection will be disabled")
		saFileWatch = nil
	} else {
		glog.Info("fsnotify watcher initialized for sa_file change detection")
		// Watch the known security mount folder from startup
		if watchErr := saFileWatch.Add(PtpSecretMountDir); watchErr != nil {
			glog.Warningf("Failed to watch %s (may not exist yet): %v", PtpSecretMountDir, watchErr)
			//destroy and release the watcher
			saFileWatch.Close()
			saFileWatch = nil
		} else {
			glog.Infof("Watching %s for sa_file changes", PtpSecretMountDir)
		}
	}

	return &Daemon{
		nodeName:              nodeName,
		namespace:             namespace,
		stdoutToSocket:        stdoutToSocket,
		kubeClient:            kubeClient,
		ptpUpdate:             ptpUpdate,
		pluginManager:         pluginManager,
		hwconfigs:             hwconfigs,
		hardwareConfigManager: hardwareconfig.NewHardwareConfigManager(),
		refreshNodePtpDevice:  refreshNodePtpDevice,
		pmcPollInterval:       pmcPollInterval,
		processManager:        pm,
		readyTracker:          tracker,
		stopCh:                stopCh,
		saFileWatcher:         saFileWatch,
	}
}

// Run in a for loop to listen for any LinuxPTPConfUpdate changes
// This function handles two types of configuration changes:
// 1. PtpConfig changes (via ConfigMap) - triggers UpdateCh
// 2. Authentication file changes (via Secret) - triggers fsnotify events (instant detection)
// Both trigger applyNodePTPProfiles() which restarts PTP processes WITHOUT restarting the pod
func (dn *Daemon) Run() {
	glog.Info("Daemon Run() started, waiting for configuration updates...")
	go dn.processManager.ptpEventHandler.ProcessEvents()

	// Setup fsnotify channels (may be nil if watcher initialization failed)
	var saFilesWatcherEventCh chan fsnotify.Event
	var saFilesWatcherErrCh chan error
	if dn.saFileWatcher != nil {
		saFilesWatcherEventCh = dn.saFileWatcher.Events
		saFilesWatcherErrCh = dn.saFileWatcher.Errors
		defer dn.saFileWatcher.Close()
		glog.Info("Using fsnotify for instant sa_file change detection")
	} else {
		glog.Warning("fsnotify unavailable, sa_file change detection disabled")
	}

	for {
		select {
		case <-dn.ptpUpdate.UpdateCh:
			glog.Info("Received configuration update signal via UpdateCh")
			err := dn.applyNodePTPProfiles()
			if err != nil {
				glog.Errorf("linuxPTP apply node profile failed: %v", err)
			}
		case event, ok := <-saFilesWatcherEventCh:
			// File system event on sa_file directory detected, set flag for UpdateConfig to handle
			if !ok {
				glog.Error("fsnotify watcher channel closed, disabling sa_file monitoring")
				saFilesWatcherEventCh = nil
				continue
			}
			if event.Op&(fsnotify.Write|fsnotify.Create|fsnotify.Remove) == 0 {
				continue
			}
			if strings.HasPrefix(filepath.Base(event.Name), ".") {
				continue // Ignore hidden files like .data
			}
			glog.Infof("Security file changed: %s (op: %s), flagging for update", event.Name, event.Op.String())
			SecurityFilesChanged.Store(true)
		case err, ok := <-saFilesWatcherErrCh:
			if !ok {
				// Channel closed - recreate the watcher to recover from failure
				// This prevents needing to restart the entire pod when fsnotify crashes
				glog.Warning("fsnotify watcher error channel closed, recreating watcher")

				dn.saFileWatcher, err = fsnotify.NewWatcher()
				if err != nil {
					glog.Errorf("Failed to recreate fsnotify watcher for sa_file monitoring: %v", err)
					continue
				}

				// Re-add the watch path for the new watcher
				if watchErr := dn.saFileWatcher.Add(PtpSecretMountDir); watchErr != nil {
					glog.Warningf("Failed to re-add watch on %s: %v", PtpSecretMountDir, watchErr)
				} else {
					glog.Infof("Re-added watch on %s after watcher recreation", PtpSecretMountDir)
				}

				// Reinitialize channels for the new watcher
				saFilesWatcherEventCh = dn.saFileWatcher.Events
				saFilesWatcherErrCh = dn.saFileWatcher.Errors

				glog.Info("fsnotify watcher successfully recreated for sa_file change detection")
				continue
			}
			glog.Errorf("fsnotify watcher error: %v", err)
		case <-dn.stopCh:
			dn.stopAllProcesses()
			glog.Infof("linuxPTP stop signal received, existing..")
			return
		}
	}
}

func printWhenNotNil(p interface{}, description string) {
	switch v := p.(type) {
	case *string:
		if v != nil {
			glog.Info(description, ": ", *v)
		}
	case *int64:
		if v != nil {
			glog.Info(description, ": ", *v)
		}
	default:
		glog.Info(description, ": ", v)
	}
}

func printWhenNotEmpty(output string) {
	if output != "" {
		fmt.Printf("%s\n", output)
	}
}

// SetProcessManager in tests
func (dn *Daemon) SetProcessManager(p *ProcessManager) {
	dn.processManager = p
	dn.readyTracker.processManager = p
}

// Delete all socket and config files
func (dn *Daemon) cleanupTempFiles() error {
	glog.Infof("Cleaning up temporary files")
	var err error
	for _, p := range ptpTmpFiles {
		processWildcard := fmt.Sprintf("%s/%s*", configPrefix, p)
		files, _ := filepath.Glob(processWildcard)
		for _, file := range files {
			err = os.Remove(file)
			if err != nil {
				glog.Infof("Failed deleting %s", file)
			}
		}

	}
	return nil
}

func (dn *Daemon) applyNodePTPProfiles() error {
	dn.readyTracker.setConfig(false)

	glog.Infof("in applyNodePTPProfiles - starting to apply %d node profiles", len(dn.ptpUpdate.NodeProfiles))
	dn.stopAllProcesses()
	// All process should have been stopped,
	// clear process in process manager.
	// Assigning processManager.process to nil releases
	// the underlying slice to the garbage
	// collector (assuming there are no other
	// references).
	dn.processManager.process = nil

	// All configs will be rebuild, and sockets recreated, so they can all be deleted
	_ = dn.cleanupTempFiles()

	// TODO:
	// compare nodeProfile with previous config,
	// only apply when nodeProfile changes

	//clear hwconfig before updating
	*dn.hwconfigs = []ptpv1.HwConfig{}

	glog.Infof("updating NodePTPProfiles to:")
	runID := 0
	slices.SortFunc(dn.ptpUpdate.NodeProfiles, func(a, b ptpv1.PtpProfile) int {
		aHasPhc2sysOpts := a.Phc2sysOpts != nil && *a.Phc2sysOpts != ""
		bHasPhc2sysOpts := b.Phc2sysOpts != nil && *b.Phc2sysOpts != ""
		//sorted in ascending order
		// here having phc2sysOptions is considered a high number
		if !aHasPhc2sysOpts && bHasPhc2sysOpts {
			return -1 //  a<b return -1
		} else if aHasPhc2sysOpts && !bHasPhc2sysOpts {
			return 1 //  a>b return
		}
		return cmp.Compare(*a.Name, *b.Name)
	})

	relations := reconcileRelatedProfiles(dn.ptpUpdate.NodeProfiles)
	// TODO: resolve clock IDs, clockType, leadingInterface and upstreamPort from hardware config
	// (needed to keep code compatibility elsewhere and allow it to work both with hardware config and plugins)
	for _, profile := range dn.ptpUpdate.NodeProfiles {
		glog.Infof("Processing profile: %s", *profile.Name)

		// Log profile details for debugging
		if profile.Interface != nil {
			glog.Infof("Profile %s interface: %s", *profile.Name, *profile.Interface)
		} else {
			glog.Infof("Profile %s has no interface field (nil)", *profile.Name)
		}
		if profile.Ptp4lOpts != nil {
			glog.Infof("Profile %s ptp4lOpts: %s", *profile.Name, *profile.Ptp4lOpts)
		}
		if profile.Phc2sysOpts != nil {
			glog.Infof("Profile %s phc2sysOpts: %s", *profile.Name, *profile.Phc2sysOpts)
		}
		if profile.Ts2PhcOpts != nil {
			glog.Infof("Profile %s ts2phcOpts: %s", *profile.Name, *profile.Ts2PhcOpts)
		}
		if profile.ChronydOpts != nil {
			glog.Infof("Profile %s chronydOpts: %s", *profile.Name, *profile.ChronydOpts)
		}
		if controlledID, ok := relations[*profile.Name]; ok {
			profile.PtpSettings["controlledId"] = strconv.Itoa(controlledID)
		}

		glog.Infof("Calling applyNodePtpProfile for profile %s with runID %d", *profile.Name, runID)
		err := dn.applyNodePtpProfile(runID, &profile)
		if err != nil {
			glog.Errorf("Failed to apply profile %s: %v", *profile.Name, err)
			return err
		}
		glog.Infof("Successfully applied profile: %s", *profile.Name)
		runID++
	}

	glog.Infof("All profiles applied, starting %d processes", len(dn.processManager.process))
	// Start all the process
	for _, p := range dn.processManager.process {
		if p != nil {
			p.eventCh = dn.processManager.eventChannel
			// start ptp4l process early , it doesn't have
			if p.depProcess == nil {
				go p.cmdRun(dn.stdoutToSocket, &dn.pluginManager)
			} else {
				for _, d := range p.depProcess {
					if d != nil {
						time.Sleep(3 * time.Second)
						glog.Infof("Starting %s", d.Name())
						go d.CmdRun(false)
						time.Sleep(3 * time.Second)
						dn.pluginManager.AfterRunPTPCommand(&p.nodeProfile, d.Name())
						d.MonitorProcess(config.ProcessConfig{
							ClockType:    p.clockType,
							ConfigName:   p.configName,
							EventChannel: dn.processManager.eventChannel,
							GMThreshold: config.Threshold{
								Max:             p.ptpClockThreshold.MaxOffsetThreshold,
								Min:             p.ptpClockThreshold.MinOffsetThreshold,
								HoldOverTimeout: p.ptpClockThreshold.HoldOverTimeout,
							},
							InitialPTPState: event.PTP_FREERUN,
						})
						glog.Infof("enabling dep process %s with Max %d Min %d Holdover %d", d.Name(), p.ptpClockThreshold.MaxOffsetThreshold, p.ptpClockThreshold.MinOffsetThreshold, p.ptpClockThreshold.HoldOverTimeout)
					}
				}
				go p.cmdRun(dn.stdoutToSocket, &dn.pluginManager)
			}
			dn.pluginManager.AfterRunPTPCommand(&p.nodeProfile, p.name)
		}
	}
	dn.pluginManager.PopulateHwConfig(dn.hwconfigs)
	*dn.refreshNodePtpDevice = true
	dn.readyTracker.setConfig(true)
	return nil
}

func reconcileRelatedProfiles(profiles []ptpv1.PtpProfile) map[string]int {
	dependentProfiles := map[string]string{}
	dependentRunIDs := map[string]int{}
	// Reconcile related profiles
	for _, profile := range profiles {
		if profile.PtpSettings["controllingProfile"] != "" {
			dependentProfiles[profile.PtpSettings["controllingProfile"]] = *profile.Name
		}
	}
	for k, v := range dependentProfiles {
		for controlledRunID, profile := range profiles {
			if *profile.Name == v { // controlled
				for _, profile := range profiles {
					if *profile.Name == k { // controlling
						dependentRunIDs[k] = controlledRunID
					}
				}
			}
		}
	}
	return dependentRunIDs
}

func printNodeProfile(nodeProfile *ptpv1.PtpProfile) {
	glog.Infof("------------------------------------")
	printWhenNotNil(nodeProfile.Name, "Profile Name")
	printWhenNotNil(nodeProfile.Interface, "Interface")
	printWhenNotNil(nodeProfile.Ptp4lOpts, "Ptp4lOpts")
	printWhenNotNil(nodeProfile.Ptp4lConf, "Ptp4lConf")
	printWhenNotNil(nodeProfile.Phc2sysOpts, "Phc2sysOpts")
	printWhenNotNil(nodeProfile.Phc2sysConf, "Phc2sysConf")
	printWhenNotNil(nodeProfile.Ts2PhcOpts, "Ts2PhcOpts")
	printWhenNotNil(nodeProfile.Ts2PhcConf, "Ts2PhcConf")
	printWhenNotNil(nodeProfile.Synce4lOpts, "Synce4lOpts")
	printWhenNotNil(nodeProfile.Synce4lConf, "Synce4lConf")
	printWhenNotNil(nodeProfile.PtpSchedulingPolicy, "PtpSchedulingPolicy")
	printWhenNotNil(nodeProfile.PtpSchedulingPriority, "PtpSchedulingPriority")
	printWhenNotNil(nodeProfile.PtpSettings, "PtpSettings")
	glog.Infof("------------------------------------")
}

/*
update: March 7th 2024
To support PTP HA phc2sys profile is appended to the end
since phc2sysOpts needs to collect profile information from applied
ptpconfig profiles for ptp4l
*/
func (dn *Daemon) applyNodePtpProfile(runID int, nodeProfile *ptpv1.PtpProfile) error {
	testDir, test := nodeProfile.PtpSettings["unitTest"]
	if test {
		configPrefix = testDir
	}

	// Check if hardware configs are available for this profile
	// If hardware configs arrive later, reconciliation will re-apply the profile
	if dn.hardwareConfigManager.ReadyHardwareConfigForProfile(*nodeProfile.Name) {
		glog.Infof("Using hardware configs for PTP profile %s instead of plugins", *nodeProfile.Name)
		if err := dn.hardwareConfigManager.ApplyHardwareConfigsForProfile(nodeProfile); err != nil {
			glog.Errorf("Failed to apply hardware configs for profile %s: %v", *nodeProfile.Name, err)
			dn.pluginManager.OnPTPConfigChange(nodeProfile)
		}
	} else {
		glog.Infof("No hardware configs found for PTP profile %s, using plugins", *nodeProfile.Name)
		dn.pluginManager.OnPTPConfigChange(nodeProfile)
	}

	var err error
	var cmdLine string
	var configPath string
	var socketPath string
	var configFile string
	var configInput *string
	var configOpts *string
	var messageTag string
	var cmd *exec.Cmd
	var haProfile map[string][]string

	ptpHAEnabled := len(listHaProfiles(nodeProfile)) > 0

	var clockType event.ClockType
	profileClockType, found := (*nodeProfile).PtpSettings["clockType"]
	var leadingNic, upstreamPort string // Used below to set event source
	if found {
		switch profileClockType {
		case TGM:
			clockType = event.GM
		case TBC:
			clockType = event.BC
			leadingNic = (*nodeProfile).PtpSettings["leadingInterface"]
			upstreamPort = (*nodeProfile).PtpSettings["upstreamPort"]
		default:
			clockType = event.ClockUnset
		}
	} else {
		clockType = event.ClockUnset
	}

	// If unset default to clock type inferred from ptp4l
	if clockType == event.ClockUnset {
		ptp4lOutput := &Ptp4lConf{}
		// Parsing ptp4l needs to be done here to get the fallback clock type.
		// Needs to be done outside the loop as we need to guarantee clockType
		// set before the ts2phcProcessName case where it is used.
		err = ptp4lOutput.PopulatePtp4lConf(nodeProfile.Ptp4lConf, nodeProfile.Ptp4lOpts)
		if err != nil {
			printNodeProfile(nodeProfile)
			return err
		}
		clockType = ptp4lOutput.clock_type
	}

	for _, pProcess := range ptpProcesses {
		controlledConfigFile := ""
		switch pProcess {
		case ptp4lProcessName:
			configInput = nodeProfile.Ptp4lConf
			configOpts = nodeProfile.Ptp4lOpts
			if configOpts == nil {
				_configOpts := " "
				configOpts = &_configOpts
			}
			socketPath = fmt.Sprintf("%s/ptp4l.%d.socket", configPrefix, runID)
			configFile = fmt.Sprintf("ptp4l.%d.config", runID)
			configPath = fmt.Sprintf("%s/%s", configPrefix, configFile)
			messageTag = fmt.Sprintf("[ptp4l.%d.config:{level}]", runID)
			if controlledID, ok := nodeProfile.PtpSettings["controlledId"]; ok {
				controlledConfigFile = fmt.Sprintf("ptp4l.%s.config", controlledID)
			}

		case phc2sysProcessName:
			configInput = nodeProfile.Phc2sysConf
			configOpts = nodeProfile.Phc2sysOpts
			if !ptpHAEnabled {
				socketPath = fmt.Sprintf("%s/ptp4l.%d.socket", configPrefix, runID)
				messageTag = fmt.Sprintf("[ptp4l.%d.config:{level}]", runID)
			} else { // when ptp ha enabled it has its own valid config
				messageTag = fmt.Sprintf("[phc2sys.%d.config:{level}]", runID)
			}
			configFile = fmt.Sprintf("phc2sys.%d.config", runID)
			configPath = fmt.Sprintf("%s/%s", configPrefix, configFile)
		case ts2phcProcessName:
			configInput = nodeProfile.Ts2PhcConf
			configOpts = nodeProfile.Ts2PhcOpts
			socketPath = fmt.Sprintf("%s/ptp4l.%d.socket", configPrefix, runID)
			configFile = fmt.Sprintf("ts2phc.%d.config", runID)
			configPath = fmt.Sprintf("%s/%s", configPrefix, configFile)
			messageTag = fmt.Sprintf("[ts2phc.%d.config:{level}]", runID)
			leap.LeapMgr.SetPtp4lConfigPath(fmt.Sprintf("ptp4l.%d.config", runID))
			// DPLL is considered to be running along with ts2phc
			maxInSpecOffset, maxHoldoverOffSet, maxHoldoverTimeout, inSpecTimer, frequencyTraceable := dpll.CalculateTimer(nodeProfile)
			if clockType == event.GM {
				// update ts2phcOpts with the new config
				if configOpts != nil && *configOpts != "" {
					if !strings.Contains(*configOpts, "--ts2phc.holdover") {
						if frequencyTraceable {
							*configOpts += " --ts2phc.holdover " + strconv.FormatInt(maxHoldoverTimeout, 10)
						} else {
							*configOpts += " --ts2phc.holdover " + strconv.FormatInt(min(inSpecTimer, maxHoldoverTimeout), 10)
						}
					} // there is a 5s delay in the NMEA driver, accepting pulses 5s after the last valid NMEA message, so that might need to be subtracted from that value
					// need more testing to confirm
					if !strings.Contains(*configOpts, "--servo_offset_threshold") {
						if frequencyTraceable {
							*configOpts += " --servo_offset_threshold " + strconv.FormatInt(maxHoldoverOffSet, 10)
						} else {
							*configOpts += " --servo_offset_threshold " + strconv.FormatInt(min(maxInSpecOffset, maxHoldoverOffSet), 10)
						}
					}
					if !strings.Contains(*configOpts, "--servo_num_offset_values") { //if consecutive smaller offsets (less than the threshold) are not observed, the system stays in S2
						*configOpts += " --servo_num_offset_values 10"
					}
				}
			}
		case syncEProcessName:
			configOpts = nodeProfile.Synce4lOpts
			configInput = nodeProfile.Synce4lConf
			socketPath = ""
			configFile = fmt.Sprintf("synce4l.%d.config", runID)
			configPath = fmt.Sprintf("%s/%s", configPrefix, configFile)
			messageTag = fmt.Sprintf("[synce4l.%d.config]", runID)
		case chronydProcessName:
			configOpts = nodeProfile.ChronydOpts
			configInput = nodeProfile.ChronydConf
			socketPath = ""
			configFile = fmt.Sprintf("chronyd.%d.config", runID)
			configPath = fmt.Sprintf("%s/%s", configPrefix, configFile)
			messageTag = fmt.Sprintf("[chronyd.%d.config]", runID)
		}

		output := &Ptp4lConf{}
		err = output.PopulatePtp4lConf(configInput, nil) // cli args not need as we already have clock type from ptp4l
		if err != nil {
			printNodeProfile(nodeProfile)
			return err
		}

		if configOpts == nil || *configOpts == "" {
			glog.Infof("configOpts empty for profile %s, skipping process: %s", *nodeProfile.Name, pProcess)
			continue
		}
		glog.Infof("Processing %s for profile %s with opts: %s", pProcess, *nodeProfile.Name, *configOpts)

		if nodeProfile.Interface != nil && *nodeProfile.Interface != "" {
			output.AddInterfaceSection(*nodeProfile.Interface)
		} else {
			iface := string("")
			nodeProfile.Interface = &iface
		}

		if pProcess != chronydProcessName {
			output.ExtendGlobalSection(*nodeProfile.Name, messageTag, socketPath, pProcess)
		} else {
			output.profile_name = *nodeProfile.Name
		}

		//output, messageTag, socketPath, GPSPIPE_SERIALPORT, update_leapfile, os.Getenv("NODE_NAME")

		// This adds the flags needed for monitor
		addFlagsForMonitor(pProcess, configOpts, output, dn.stdoutToSocket)
		var configOutput string
		var relations *synce.Relations
		var ifaces config.IFaces
		if pProcess == syncEProcessName {
			configOutput, relations = output.RenderSyncE4lConf(nodeProfile.PtpSettings)
		} else {
			configOutput, ifaces = output.RenderPtp4lConf()
			for i := range ifaces {
				if upstreamPort != "" && leadingNic == ifaces[i].Name {
					ifaces[i].Source = event.PTP4l
				}
				ifaces[i].PhcId = ptpnetwork.GetPhcId(ifaces[i].Name)
			}
		}

		if configInput != nil {
			*configInput = configOutput
		}

		cmdLine = fmt.Sprintf("/usr/sbin/%s -f %s %s", pProcess, configPath, *configOpts)
		cmdLine = addScheduling(nodeProfile, cmdLine)
		if pProcess == phc2sysProcessName {
			haProfile, cmdLine = dn.ApplyHaProfiles(nodeProfile, cmdLine)
		}
		args := strings.Split(cmdLine, " ")
		cmd = exec.Command(args[0], args[1:]...)
		dprocess := ptpProcess{
			name:                 pProcess,
			ifaces:               ifaces,
			processConfigPath:    configPath,
			processSocketPath:    socketPath,
			configName:           configFile,
			messageTag:           messageTag,
			exitCh:               make(chan bool),
			stopped:              true,
			logFilters:           logfilter.GetLogFilters(pProcess, messageTag, (*nodeProfile).PtpSettings),
			cmd:                  cmd,
			depProcess:           []process{},
			nodeProfile:          *nodeProfile,
			clockType:            clockType,
			ptpClockThreshold:    getPTPThreshold(nodeProfile),
			haProfile:            haProfile,
			syncERelations:       relations,
			logParser:            getParser(pProcess),
			tBCAttributes:        tBCProcessAttributes{controlledPortsConfigFile: controlledConfigFile},
			lastTransitionResult: event.PTP_NOTSET,
			handler:              dn.processManager.ptpEventHandler,
			dn:                   dn,
		}

		if pProcess == ptp4lProcessName {
			if port, ok := (*nodeProfile).PtpSettings["upstreamPort"]; ok && clockType == event.BC {
				dprocess.tBCAttributes.trIfaceName = port
				// Prepare cached resources for T-BC processing
				dprocess.prepareTBCResources()
			}
		}

		// TODO HARDWARE PLUGIN for e810
		if pProcess == ptp4lProcessName {
			// Skip PMC creation for controlled profiles
			if controllingProfile, isControlled := (*nodeProfile).PtpSettings["controllingProfile"]; isControlled && controllingProfile != "" {
				// See DownstreamIWF
				glog.Infof("Skipping PMC monitoring for controlled profile %s", *nodeProfile.Name)
			} else {
				pmcClockType, clockTypeFound := (*nodeProfile).PtpSettings["clockType"]
				if !clockTypeFound {
					pmcClockType = string(clockType)
				}
				pmcProcess := NewPMCProcess(runID, dn.processManager.ptpEventHandler, pmcClockType)
				pmcProcess.CmdInit()
				// TODO addScheduling
				dprocess.depProcess = append(dprocess.depProcess, pmcProcess)
			}
		} else if pProcess == ts2phcProcessName { //& if the x plugin is enabled
			if clockType == event.GM {
				if output.gnss_serial_port == "" {
					output.gnss_serial_port = GPSPIPE_SERIALPORT
				}
				// TODO: move this to plugin or call it from hwplugin or leave it here and remove Hardcoded
				gmInterface := dprocess.ifaces.GetLeadingInterface().Name

				gpsDaemon := &GPSD{
					name:        GPSD_PROCESSNAME,
					execMutex:   sync.Mutex{},
					cmd:         nil,
					serialPort:  output.gnss_serial_port,
					exitCh:      make(chan struct{}),
					gmInterface: gmInterface,
					stopped:     false,
					messageTag:  messageTag,
					ublxTool:    nil,
				}
				gpsDaemon.CmdInit()
				gpsDaemon.cmdLine = addScheduling(nodeProfile, gpsDaemon.cmdLine)
				args = strings.Split(gpsDaemon.cmdLine, " ")
				gpsDaemon.cmd = exec.Command(args[0], args[1:]...)
				dprocess.depProcess = append(dprocess.depProcess, gpsDaemon)

				// init gpspipe
				gpsPipeDaemon := &gpspipe{
					name:       GPSPIPE_PROCESSNAME,
					execMutex:  sync.Mutex{},
					cmd:        nil,
					serialPort: GPSPIPE_SERIALPORT,
					exitCh:     make(chan struct{}),
					stopped:    false,
					messageTag: messageTag,
				}
				gpsPipeDaemon.CmdInit()
				gpsPipeDaemon.cmdLine = addScheduling(nodeProfile, gpsPipeDaemon.cmdLine)
				args = strings.Split(gpsPipeDaemon.cmdLine, " ")
				gpsPipeDaemon.cmd = exec.Command(args[0], args[1:]...)
				dprocess.depProcess = append(dprocess.depProcess, gpsPipeDaemon)
			}
			// init dpll
			// TODO: Try to inject DPLL depProcess via plugin ?
			var localMaxHoldoverOffSet uint64 = dpll.LocalMaxHoldoverOffSet
			var localHoldoverTimeout uint64 = dpll.LocalHoldoverTimeout
			var maxInSpecOffset uint64 = dpll.MaxInSpecOffset
			var inSyncConditionTh uint64 = dpll.MaxInSpecOffset
			var inSyncConditionTimes uint64 = 1
			sInSyncConditionTh, found1 := (*nodeProfile).PtpSettings["inSyncConditionThreshold"]
			if found1 {
				inSyncConditionTh, err = strconv.ParseUint(sInSyncConditionTh, 0, 64)
				if err != nil {
					return fmt.Errorf("failed to parse inSyncConditionThreshold: %s", err)
				}
			}
			sInSyncConditionTim, found2 := (*nodeProfile).PtpSettings["inSyncConditionTimes"]
			if found2 {
				inSyncConditionTimes, err = strconv.ParseUint(sInSyncConditionTim, 0, 64)
				if err != nil {
					return fmt.Errorf("failed to parse inSyncConditionTimes: %s", err)
				}
			}
			var clockId uint64
			phaseOffsetPinFilter := map[string]string{}
			for _, iface := range dprocess.ifaces {
				var eventSource []event.EventSource
				if iface.Source == event.GNSS || iface.Source == event.PPS ||
					(iface.Source == event.PTP4l && profileClockType == TBC) {
					if nodeProfile.PtpSettings[dpll.PtpSettingsDpllIgnoreKey(iface.Name)] == "true" {
						glog.Infof("Init dpll: Skipping dpll for %s", iface.Name)
						continue
					}
					glog.Info("Init dpll: ptp settings ", (*nodeProfile).PtpSettings)
					for k, v := range (*nodeProfile).PtpSettings {
						glog.Info("Init dpll: ptp kv ", k, " ", v)
						if strings.Contains(k, strings.Join([]string{iface.Name, "phaseOffset"}, ".")) {
							filterKey := strings.Split(k, ".")
							property := filterKey[len(filterKey)-1]
							phaseOffsetPinFilter[property] = v
							glog.Infof("dpll phase offset filter property: %s[%s]=%s", iface.Name, property, v)
							continue
						}
						i, err := strconv.ParseUint(v, 10, 64)
						if err != nil {
							continue
						}
						if k == dpll.LocalMaxHoldoverOffSetStr {
							localMaxHoldoverOffSet = i
						}
						if k == dpll.LocalHoldoverTimeoutStr {
							localHoldoverTimeout = i
						}
						if k == dpll.MaxInSpecOffsetStr {
							maxInSpecOffset = i
						}
						if k == fmt.Sprintf("%s[%s]", dpll.ClockIdStr, iface.Name) {
							clockId = i
						}
					}

					// Try to get holdover parameters from HardwareConfig (new system)
					// This takes precedence over plugin-provided values for better declarative configuration
					profileName := ""
					if nodeProfile.Name != nil {
						profileName = *nodeProfile.Name
					}
					holdoverParams := dn.getHoldoverParameters(profileName, clockId)
					if holdoverParams != nil {
						// HardwareConfig provides holdover parameters - use them
						maxInSpecOffset = holdoverParams.MaxInSpecOffset
						localMaxHoldoverOffSet = holdoverParams.LocalMaxHoldoverOffset
						localHoldoverTimeout = holdoverParams.LocalHoldoverTimeout
						glog.Infof("Using holdover parameters from HardwareConfig for clock %#x: MaxInSpec=%dns, LocalMaxOffset=%dns, Timeout=%ds",
							clockId, maxInSpecOffset, localMaxHoldoverOffSet, localHoldoverTimeout)
					} else {
						// Fall back to plugin/profile settings (backward compatibility)
						glog.Infof("Using holdover parameters from profile/plugin for clock %#x: MaxInSpec=%dns, LocalMaxOffset=%dns, Timeout=%ds",
							clockId, maxInSpecOffset, localMaxHoldoverOffSet, localHoldoverTimeout)
					}

					eventSource = []event.EventSource{iface.Source}
					// pass array of ifaces which has source + clockId -
					// here we have multiple dpll objects identified by clock id
					// depends on will be either PPS or  GNSS,
					// ONLY the one with GNSS dependency will go to HOLDOVER
					dpllDaemon := dpll.NewDpll(clockId, localMaxHoldoverOffSet, localHoldoverTimeout,
						maxInSpecOffset, iface.Name, eventSource, dpll.NONE, dn.GetPhaseOffsetPinFilter(nodeProfile),
						// Used only in T-BC in-sync condition:
						inSyncConditionTh, inSyncConditionTimes)
					glog.Infof("depending on %s", dpllDaemon.DependsOn())
					dpllDaemon.CmdInit()
					dprocess.depProcess = append(dprocess.depProcess, dpllDaemon)
				}
			}
		}
		err = os.WriteFile(configPath, []byte(configOutput), 0644)
		if err != nil {
			printNodeProfile(nodeProfile)
			return fmt.Errorf("failed to write the configuration file named %s: %v", configPath, err)
		}

		printNodeProfile(nodeProfile)
		dn.processManager.process = append(dn.processManager.process, &dprocess)
		dn.pluginManager.RegisterEnableCallback(dprocess.name, dprocess.cmdSetEnabled)
		glog.Infof("Added %s process to process manager for profile %s", pProcess, *nodeProfile.Name)

	}
	glog.Infof("Completed applyNodePtpProfile for profile %s, total processes in manager: %d", *nodeProfile.Name, len(dn.processManager.process))
	return nil
}

func (dn *Daemon) GetPhaseOffsetPinFilter(nodeProfile *ptpv1.PtpProfile) map[string]map[string]string {
	phaseOffsetPinFilter := map[string]map[string]string{}
	for k, v := range (*nodeProfile).PtpSettings {
		if strings.Contains(k, "phaseOffsetFilter") {
			filterKey := strings.Split(k, ".")
			property := filterKey[len(filterKey)-1]
			clockIdStr := filterKey[len(filterKey)-2]
			if len(phaseOffsetPinFilter[clockIdStr]) == 0 {
				phaseOffsetPinFilter[clockIdStr] = map[string]string{}
			}
			phaseOffsetPinFilter[clockIdStr][property] = v
			continue
		}
	}
	return phaseOffsetPinFilter
}

// Add fifo scheduling if specified in nodeProfile
func addScheduling(nodeProfile *ptpv1.PtpProfile, cmdLine string) string {
	if nodeProfile.PtpSchedulingPolicy != nil && *nodeProfile.PtpSchedulingPolicy == "SCHED_FIFO" {
		if nodeProfile.PtpSchedulingPriority == nil {
			glog.Errorf("Priority must be set for SCHED_FIFO; using default scheduling.")
			return cmdLine
		}
		priority := *nodeProfile.PtpSchedulingPriority
		if priority < 1 || priority > 65 {
			glog.Errorf("Invalid priority %d; using default scheduling.", priority)
			return cmdLine
		}
		cmdLine = fmt.Sprintf("/bin/chrt -f %d %s", priority, cmdLine)
		glog.Infof(cmdLine)
		return cmdLine
	}
	return cmdLine
}

func processStatus(c net.Conn, processName, messageTag string, status int64) {
	cfgName := strings.Replace(strings.Replace(messageTag, "]", "", 1), "[", "", 1)
	if cfgName != "" {
		cfgName = strings.Split(cfgName, MessageTagSuffixSeperator)[0]
	}
	// ptp4l[5196819.100]: [ptp4l.0.config] PTP_PROCESS_STOPPED:0/1

	if c == nil {
		UpdateProcessStatusMetrics(processName, cfgName, status)
		return
	}
	logProcessStatus(processName, cfgName, status, c)
}

func logProcessStatus(processName string, cfgName string, status int64, c net.Conn) {
	if c == nil {
		return
	}
	message := fmt.Sprintf("%s[%d]:[%s] PTP_PROCESS_STATUS:%d", processName, time.Now().Unix(), cfgName, status)
	glog.Info(message)
	_, err := c.Write([]byte(message + "\n"))
	if err != nil {
		glog.Errorf("Write error sending ptp4l/phc2sys process healths status%s:", err)
	}
}

// prepareTBCResources prepares cached resources for T-BC processing
// This method caches expensive operations that would otherwise be repeated 16x/second
func (p *ptpProcess) prepareTBCResources() {
	// Cache hardwareconfig availability (expensive lookup)
	if p.dn != nil && p.dn.hardwareConfigManager != nil {
		vTbcHasHardwareConfig = p.dn.hardwareConfigManager.HasHardwareConfigForProfile(&p.nodeProfile)
	}

	// Cache PTP state detector instance (expensive creation)
	if vTbcHasHardwareConfig {
		p.tbcStateDetector = p.dn.hardwareConfigManager.GetPTPStateDetector()
	}
}

// tBCTransitionCheck performs ultra-fast T-BC transition detection (called 16x/second)
// Uses cached values and optimized processing to minimize performance impact
func (p *ptpProcess) tBCTransitionCheck(output string, pm *plugin.PluginManager) {
	// Use cached hardwareconfig availability (no expensive lookups)
	if vTbcHasHardwareConfig && p.tbcStateDetector != nil {
		// Hardwareconfig path: Use cached PTP state detector
		p.processTBCTransitionHardwareConfig(output, pm)
	} else {
		// Legacy path: Use optimized string matching
		p.processTBCTransitionLegacy(output, pm)
	}
}

// applyConditionOrFallback applies hardware config for a condition or falls back to plugin
func (p *ptpProcess) applyConditionOrFallback(conditionType, pluginAction string, pm *plugin.PluginManager) {
	if p.dn != nil && p.dn.hardwareConfigManager != nil {
		if err := p.dn.hardwareConfigManager.ApplyConditionForProfile(&p.nodeProfile, conditionType); err != nil {
			glog.Errorf("Failed to apply hardware config for '%s' condition: %v", conditionType, err)
			// Fallback to plugin if hardware config fails
			pm.AfterRunPTPCommand(&p.nodeProfile, pluginAction)
		} else {
			glog.Infof("Successfully applied hardware config for '%s' condition", conditionType)
		}
	} else {
		// Fallback to plugin if no hardware config manager
		pm.AfterRunPTPCommand(&p.nodeProfile, pluginAction)
	}
}

// processTBCTransitionHardwareConfig handles T-BC transitions using hardwareconfig (optimized)
func (p *ptpProcess) processTBCTransitionHardwareConfig(output string, pm *plugin.PluginManager) {
	// Use the new DetectStateChange function for optimal performance
	conditionType := p.tbcStateDetector.DetectStateChange(output)

	switch conditionType {
	case hardwareconfig.ConditionTypeLocked:
		p.applyConditionOrFallback(hardwareconfig.ConditionTypeLocked, "tbc-ho-exit", pm)
		p.lastTransitionResult = event.PTP_LOCKED
		p.sendPtp4lEvent()
	case hardwareconfig.ConditionTypeLost:
		p.applyConditionOrFallback(hardwareconfig.ConditionTypeLost, "tbc-ho-entry", pm)
		p.lastTransitionResult = event.PTP_FREERUN
		p.sendPtp4lEvent()
	}
}

// processTBCTransitionLegacy is the original implementation as ultimate fallback
func (p *ptpProcess) processTBCTransitionLegacy(output string, pm *plugin.PluginManager) {
	if strings.Contains(output, p.tBCAttributes.trIfaceName) {
		if strings.Contains(output, "to SLAVE on MASTER_CLOCK_SELECTED") {
			pm.AfterRunPTPCommand(&p.nodeProfile, "tbc-ho-exit")
			p.lastTransitionResult = event.PTP_LOCKED
			p.sendPtp4lEvent()
		} else if strings.Contains(output, "to MASTER on ANNOUNCE_RECEIPT_TIMEOUT_EXPIRES") ||
			strings.Contains(output, "SLAVE to") {
			pm.AfterRunPTPCommand(&p.nodeProfile, "tbc-ho-entry")
			p.lastTransitionResult = event.PTP_FREERUN
			glog.Info("T-BC MOVE TO HOLDOVER")
			p.sendPtp4lEvent()
		}
	}
}

// cmdRun runs given ptpProcess and restarts on errors
//
//nolint:gocyclo // complexity is acceptable for this function
func (p *ptpProcess) cmdRun(stdoutToSocket bool, pm *plugin.PluginManager) {
	cmd := p.cmd
	stopped := p.getAndSetStopped(false)
	if !stopped {
		glog.Infof("%s is already running", p.name)
		return
	}
	doneCh := make(chan struct{}) // Done setting up logging.  Go ahead and wait for process
	defer func() {
		if stdoutToSocket && p.c != nil {
			if err := p.c.Close(); err != nil {
				glog.Errorf("closing connection returned error %s", err)
			}
		}
		p.exitCh <- true
	}()

	profileClockType, pctFound := p.nodeProfile.PtpSettings["clockType"]
	if !pctFound {
		profileClockType = string(event.ClockUnset)
	}
	for {
		glog.Infof("Starting %s...", p.name)
		glog.Infof("%s cmd: %+v", p.name, cmd)

		cmdReader, err := cmd.StdoutPipe()
		if err != nil {
			glog.Errorf("CmdRun() error creating StdoutPipe for %s: %v", p.name, err)
			break
		}

		// don't discard process stderr output
		cmd.Stderr = cmd.Stdout

		if !stdoutToSocket {
			scanner := bufio.NewScanner(cmdReader)
			processStatus(nil, p.name, p.messageTag, PtpProcessUp)
			go func() {
				for scanner.Scan() {
					output := scanner.Text()
					if p.name == chronydProcessName {
						output = fmt.Sprintf("%s[%d]%s: %s", chronydProcessName, p.cmd.Process.Pid, p.messageTag, output)
					}
					output = pm.ProcessLog(p.name, output)
					// for ts2phc from 4.2 onwards replace /dev/ptpX by actual interface
					output = p.replaceClockID(output)
					printWhenNotEmpty(logfilter.FilterOutput(p.logFilters, output))
					p.processPTPMetrics(output)
					if p.name == ptp4lProcessName {
						if profileClockType == TBC {
							p.tBCTransitionCheck(output, pm)
						}
					} else if p.name == phc2sysProcessName && len(p.haProfile) > 0 {
						p.announceHAFailOver(nil, output) // do not use go routine since order of execution is important here
					}
				}
				doneCh <- struct{}{}
			}()
		} else {
			go func() {
			connect:
				select {
				case <-p.exitCh:
					doneCh <- struct{}{}
				default:
					p.c, err = dialSocket()
					if err != nil {
						goto connect
					}
				}
				scanner := bufio.NewScanner(cmdReader)
				processStatus(p.c, p.name, p.messageTag, PtpProcessUp)
				for _, d := range p.depProcess {
					if d != nil {
						d.ProcessStatus(p.c, PtpProcessUp)
					}
				}

				for scanner.Scan() {
					output := scanner.Text()
					if p.name == chronydProcessName {
						output = fmt.Sprintf("%s[%d]%s: %s", chronydProcessName, p.cmd.Process.Pid, p.messageTag, output)
					}
					output = pm.ProcessLog(p.name, output)
					// for ts2phc from 4.2 onwards replace /dev/ptpX by actual interface
					output = p.replaceClockID(output)
					printWhenNotEmpty(logfilter.FilterOutput(p.logFilters, output))

					// for ts2phc, we need to extract metrics to identify GM state
					p.processPTPMetrics(output)
					if p.name == ptp4lProcessName {
						if profileClockType == TBC {
							p.tBCTransitionCheck(output, pm)
						}
					} else if p.name == phc2sysProcessName && len(p.haProfile) > 0 {
						p.announceHAFailOver(p.c, output) // do not use go routine since order of execution is important here
					}
					line := removeMessageSuffix(output) + "\n"
					_, err2 := p.c.Write([]byte(line))
					if err2 != nil {
						glog.Errorf("Write %s error %s:", output, err2)
						goto connect
					}
				}
				doneCh <- struct{}{}
			}()
		}
		// Don't restart after termination
		if !p.Stopped() {
			glog.Infof("starting %s...", p.name)
			p.cmd = cmd
			err = cmd.Start() // this is asynchronous call,
			if err != nil {
				glog.Errorf("CmdRun() error starting %s: %v", p.name, err)
			}

			<-doneCh // goroutine is done
			err = cmd.Wait()

			glog.Infof("done waiting for %s...", p.name)
			if err != nil {
				glog.Errorf("CmdRun() error waiting for %s: %v", p.name, err)
			}
			if stdoutToSocket && p.c != nil {
				processStatus(p.c, p.name, p.messageTag, PtpProcessDown)
			} else {
				processStatus(nil, p.name, p.messageTag, PtpProcessDown)
			}
			p.updateGMStatusOnProcessDown(p.name)
		}

		if profileClockType == TBC && p.name == ptp4lProcessName {
			pm.AfterRunPTPCommand(&p.nodeProfile, "reset-to-default")
		}
		time.Sleep(connectionRetryInterval) // Delay to prevent flooding restarts if startup fails
		// Don't restart after termination
		if p.Stopped() {
			glog.Infof("Not recreating %s...", p.name)
			break
		} else {
			glog.Infof("Recreating %s...", p.name)
			newCmd := exec.Command(cmd.Args[0], cmd.Args[1:]...)
			cmd = newCmd
		}
		if stdoutToSocket && p.c != nil {
			if err2 := p.c.Close(); err2 != nil {
				glog.Errorf("closing connection returned error %s", err2)
			}
		}
	}
}

// for ts2phc along with processing metrics need to identify event
func (p *ptpProcess) processPTPMetrics(output string) {
	state := event.PTP_FREERUN
	if p.logParser != nil {
		processWithParser(p, output)
	} else if p.name == syncEProcessName {
		configName := strings.Replace(strings.Replace(p.messageTag, "]", "", 1), "[", "", 1)
		if configName == "" {
			return
		}
		configName = strings.Split(configName, MessageTagSuffixSeperator)[0] // remove any suffix added to the configName
		logEntry := synce.ParseLog(output)
		p.ProcessSynceEvents(logEntry)
	} else {
		configName, source, ptpOffset, clockState, iface := extractMetrics(p.messageTag, p.name, p.ifaces, output, p.c == nil)
		p.hasCollectedMetrics = true
		if iface != "" { // for ptp4l/phc2sys this function only update metrics
			var values map[event.ValueType]interface{}
			ifaceName := masterOffsetIface.getByAlias(configName, iface).name
			if iface != clockRealTime && p.name == ts2phcProcessName {
				eventSource := p.ifaces.GetEventSource(ifaceName)
				if eventSource == event.GNSS {
					values = map[event.ValueType]interface{}{event.NMEA_STATUS: int64(1)}
				}
			}
			// ts2phc has to be handled differently since it announce holdover state when gnss is lost
			//TODO: verify how 1pps is handled when lost
			switch clockState {
			case FREERUN:
				state = event.PTP_FREERUN
			case LOCKED:
				state = event.PTP_LOCKED
			case HOLDOVER:
				state = event.PTP_HOLDOVER // consider s1 state as holdover,this passed to event to create metrics and events
			}
			p.ProcessTs2PhcEvents(ptpOffset, source, ifaceName, state, values)
		}
	}
}

// cmdStop stops ptpProcess launched by cmdRun
func (p *ptpProcess) cmdStop() {
	glog.Infof("stopping %s...", p.name)
	cmd := p.cmd
	if cmd == nil {
		glog.Infof("cmdStop is nil %s", p.name)
		return
	}
	if p.Stopped() {
		glog.Infof("%s is already stopped", p.name)
		return
	}
	glog.Infof("%s setStopped true", p.name)

	p.setStopped(true)
	if cmd.Process != nil {
		glog.Infof("Sending TERM to (%s) PID: %d", p.name, cmd.Process.Pid)
		err := cmd.Process.Signal(syscall.SIGTERM)
		if err != nil {
			// If the process is already terminated, we will get an error here
			glog.Errorf("failed to send SIGTERM to %s (%d): %v", p.name, cmd.Process.Pid, err)
			return
		}
	} else {
		glog.Infof("not Sending TERM to (%s) which is nil", p.name)
	}
	<-p.exitCh
}

func (p *ptpProcess) cmdSetEnabled(enabled bool) {
	glog.Infof("cmdSetEnabled %s set to %t", p.name, enabled)
	p.cmdSetEnabledMutex.Lock()
	defer p.cmdSetEnabledMutex.Unlock()
	switch p.name {
	case "chronyd":
		if enabled {
			_, _ = exec.Command("chronyc", "online").Output()
			processStatus(p.c, p.name, p.messageTag, PtpProcessUp)
		} else {
			_, _ = exec.Command("chronyc", "offline").Output()
			processStatus(p.c, p.name, p.messageTag, PtpProcessDown)
		}
	case "phc2sys":
		if enabled {
			if p.Stopped() && p.cmd != nil {
				cmd := p.cmd
				newCmd := exec.Command(cmd.Args[0], cmd.Args[1:]...)
				p.cmd = newCmd
				go p.cmdRun(p.dn.stdoutToSocket, &(p.dn.pluginManager))
			}
		} else {
			p.cmdStop()
		}
	}
}

func getPTPThreshold(nodeProfile *ptpv1.PtpProfile) *ptpv1.PtpClockThreshold {
	if nodeProfile.PtpClockThreshold != nil {
		return &ptpv1.PtpClockThreshold{
			HoldOverTimeout:    nodeProfile.PtpClockThreshold.HoldOverTimeout,
			MaxOffsetThreshold: nodeProfile.PtpClockThreshold.MaxOffsetThreshold,
			MinOffsetThreshold: nodeProfile.PtpClockThreshold.MinOffsetThreshold,
		}
	} else {
		return &ptpv1.PtpClockThreshold{
			HoldOverTimeout:    5,
			MaxOffsetThreshold: 100,
			MinOffsetThreshold: -100,
		}
	}
}

func (p *ptpProcess) MonitorEvent(offset float64, clockState string) {
	// not implemented
}

func (p *ptpProcess) ProcessTs2PhcEvents(ptpOffset float64, source string, iface string, state event.PTPState, extraValue map[event.ValueType]interface{}) {
	var ptpState event.PTPState
	ptpState = state
	ptpOffsetInt64 := int64(ptpOffset)
	// if state is HOLDOVER do not update the state
	// transition to FREERUN if offset is outside configured thresholds
	if shouldFreeRun(state, ptpOffset, p.ptpClockThreshold) {
		ptpState = event.PTP_FREERUN
	}

	if source == ts2phcProcessName { // for ts2phc send it to event to create metrics and events
		var values = make(map[event.ValueType]interface{})

		values[event.OFFSET] = ptpOffsetInt64
		for k, v := range extraValue {
			values[k] = v
		}
		select {
		case p.eventCh <- event.EventChannel{
			ProcessName: event.TS2PHC,
			State:       ptpState,
			CfgName:     p.configName,
			IFace:       iface,
			Values:      values,
			ClockType:   p.clockType,
			Time:        time.Now().UnixMilli(),
			WriteToLog: func() bool { // only write to log if there is something extra
				if len(extraValue) > 0 {
					return true
				}
				return false
			}(),
			Reset: false,
		}:
		default:
		}

	} else {
		if iface != "" && iface != clockRealTime {
			iface = utils.GetAlias(iface)
		}
		if p.c != nil {
			return // no metrics when socket is used
		}
		switch ptpState {
		case event.PTP_LOCKED:
			updateClockStateMetrics(p.name, iface, LOCKED)
		case event.PTP_FREERUN:
			updateClockStateMetrics(p.name, iface, FREERUN)
		case event.PTP_HOLDOVER:
			updateClockStateMetrics(p.name, iface, HOLDOVER)
		}
	}
}

func (dn *Daemon) ApplyHaProfiles(nodeProfile *ptpv1.PtpProfile, cmdLine string) (map[string][]string, string) {
	lsProfiles := listHaProfiles(nodeProfile)
	haProfiles := make(map[string][]string, len(lsProfiles))
	updateHaProfileToSocketPath := make([]string, 0, len(lsProfiles))
	for _, profileName := range lsProfiles {
		for _, dmProcess := range dn.processManager.process {
			if dmProcess.nodeProfile.Name != nil && *dmProcess.nodeProfile.Name == profileName {
				updateHaProfileToSocketPath = append(updateHaProfileToSocketPath, "-z "+dmProcess.processSocketPath)
				var ifaces []string
				for _, iface := range dmProcess.ifaces {
					ifaces = append(ifaces, iface.Name)
				}
				haProfiles[profileName] = ifaces
				break // Exit inner loop if profile found
			}
		}
	}
	if len(updateHaProfileToSocketPath) > 0 {
		cmdLine = fmt.Sprintf("%s%s", cmdLine, strings.Join(updateHaProfileToSocketPath, " "))
	}
	glog.Infof(cmdLine)
	return haProfiles, cmdLine
}

func listHaProfiles(nodeProfile *ptpv1.PtpProfile) (haProfiles []string) {
	if profiles, ok := nodeProfile.PtpSettings[PTP_HA_IDENTIFIER]; ok {
		haProfiles = strings.Split(profiles, ",")
		for index, profile := range haProfiles {
			haProfiles[index] = strings.TrimSpace(profile)
		}
	}
	return
}

func (p *ptpProcess) announceHAFailOver(c net.Conn, output string) {
	defer func() {
		if r := recover(); r != nil {
			glog.Errorf("Recovered in f %#v", r)
		}
	}()
	var activeIFace string
	var match []string
	// selecting ens2f2 as out-of-domain source clock - 0
	// selecting ens2f0 as domain source clock - 1
	domainState, activeState := failOverIndicator(output, len(p.haProfile))

	if domainState == 1 {
		match = haInDomainRegEx.FindStringSubmatch(output)
	} else if domainState == 0 && activeState == 1 {
		match = haOutDomainRegEx.FindStringSubmatch(output)
	} else {
		return
	}

	if match != nil {
		activeIFace = match[1]
	} else {
		glog.Errorf("couldn't retrieve interface name from fail over logs %s\n", output)
		return
	}
	// find profile name and construct the log-out and metrics
	var currentProfile string
	var inActiveProfiles []string
	for profile, ifaces := range p.haProfile {
		for _, iface := range ifaces {
			if iface == activeIFace {
				currentProfile = profile
				break
			}
		}
		// mark all other profiles as inactive
		if currentProfile != profile && activeState == 1 {
			inActiveProfiles = append(inActiveProfiles, profile)
		}
	}
	// log both active and inactive profiles
	logString := []string{fmt.Sprintf("%s[%d]:[%s] ptp_ha_profile %s state %d\n", p.name, time.Now().Unix(), p.configName, currentProfile, activeState)}
	for _, inActive := range inActiveProfiles {
		logString = append(logString, fmt.Sprintf("%s[%d]:[%s] ptp_ha_profile %s state %d\n", p.name, time.Now().Unix(), p.configName, inActive, 0))
	}
	if c == nil {
		for _, logProfile := range logString {
			fmt.Printf("%s", logProfile)
		}
		UpdatePTPHAMetrics(currentProfile, inActiveProfiles, activeState)
	} else {
		for _, logProfile := range logString {
			_, err := c.Write([]byte(logProfile))
			if err != nil {
				glog.Errorf("failed to write class change event %s", err.Error())
			}
		}
	}
}

// 1= In domain 0 out of domain
// All the profiles are in domain for their own domain.
// If there are multiple domains/profiles, then both are active in their own domain, and one of them is also active out of domain
// returns domain state and activeState 3 and 1 = Active,2 is inActive
func failOverIndicator(output string, count int) (int64, int64) {
	if strings.Contains(output, HAInDomainIndicator) { // when single profile then it's always 1
		if count == 1 {
			return 1, 1 // 1= in ; 1= active profile =3
		} else {
			return 1, 0 // 1= in ,1= inactive ==2
		}
	} else if strings.Contains(output, HAOutOfDomainIndicator) {
		return 0, 1 //0=out; 1=active == 1
	}
	return 0, 0
}

func removeMessageSuffix(input string) (output string) {
	// container log output  "ptp4l[2464681.628]: [phc2sys.1.config:7] master offset -4 s2 freq -26835 path delay 525"
	// make sure non-supported version can handle suffix tags
	// clear {} from unparsed template
	//"ptp4l[2464681.628]: [phc2sys.1.config:{level}] master offset -4 s2 freq -26835 path delay 525"
	replacer := strings.NewReplacer("{", "", "}", "")
	output = replacer.Replace(input)
	// Replace matching parts in the input string
	output = messageTagSuffixRegEx.ReplaceAllString(output, "$1")
	return output
}

// linuxptp 4.2 uses ptp device id ; this function will replace the ptp device id by the interface name
func (p *ptpProcess) replaceClockID(input string) (output string) {
	if p.name != ts2phcProcessName {
		return input
	}
	// replace only for value with offset
	if indx := strings.Index(input, offset); indx < 0 {
		return input
	}
	// Replace all occurrences of the pattern with the replacement string
	// ts2phc[1896327.319]: [ts2phc.0.config] dev/ptp4  offset    -1 s2 freq      -2
	// Find the first match
	match := clockIDRegEx.FindStringSubmatch(input)
	if match == nil {
		return input
	}
	// Extract the captured interface string (group 1)
	iface := p.ifaces.GetPhcID2IFace(match[0])
	// Fallback rationale:
	// In some cases the ts2phc log may reference a PHC device that isn't yet
	// present in this process' PHC->iface map (e.g., early logs before map build
	// or when ts2phc tracks an iface not listed in ptp4lConf). To avoid
	// mislabeling when multiple ts2phc-capable ifaces exist, we resolve the PHC
	// by scanning all PTP-capable NICs and matching their PHC IDs.
	if iface == match[0] || iface == "" {
		glog.Infof("Fallback to discover PTP devices to resolve PHC ID for %s", match[0])
		if nics, err := ptpnetwork.DiscoverPTPDevices(); err == nil {
			for _, dev := range nics {
				if ptpnetwork.GetPhcId(dev) == match[0] {
					iface = dev
					// Persist mapping so future lookups don't need fallback
					updated := false
					for idx := range p.ifaces {
						if p.ifaces[idx].Name == dev {
							p.ifaces[idx].PhcId = match[0]
							updated = true
							break
						}
					}
					if !updated {
						p.ifaces.Add(config.Iface{Name: dev, PhcId: match[0]})
					}
					break
				}
			}
		}
	}
	if iface == "" || strings.HasPrefix(iface, "/dev/ptp") {
		return input
	}

	output = clockIDRegEx.ReplaceAllString(input, iface)
	return output
}

// updateGMStatusOnProcessDown send events when  ts2phc process is down by
// send event to EventHandler
func (p *ptpProcess) updateGMStatusOnProcessDown(process string) {
	// need to update GM status for  following process kill for  ts2phc
	if process == ts2phcProcessName {
		// ts2phc process dead should update GM-STATUS
		// Reset the entire event subsystem
		// (this nullifies the remaining pieces in the event data if ts2phc was killed during ptp profile change)
		select {
		case p.eventCh <- event.EventChannel{
			ProcessName: event.TS2PHC,
			CfgName:     p.configName,
			Reset:       true,
		}:
		default:
		}
	}
}

func (p *ptpProcess) ProcessSynceEvents(logEntry synce.LogEntry) {
	//                                          STATE  VALUE  DEVICE   SOURCE EXTSOURCE
	//------------------------------------------------------------------------------------
	// synce4l[627685.138]: [synce4l.0.config] LOCKED   0     synce1            GNSS
	// synce4l[627685.138]: [synce4l.0.config] LOCKED   0     synce1  ens7f0
	// synce4l[627602.593]: [synce4l.0.config] EXT_QL  255    synce1  ens7f0
	// synce4l[627602.593]: [synce4l.0.config] QL  255    synce1  ens7f0
	// synce4l[627602.593]: [synce4l.0.config] CLOCK_QUALITY  PRS    synce1  ens7f0
	// synce4l[627602.540]: [synce4l.0.config] LOCKED   0     synce1

	extraValue := map[event.ValueType]interface{}{}
	state := event.PTP_UNKNOWN

	clockQuality := ""
	iface := ""

	// synce4l[627602.540]: [synce4l.0.config] LOCKED   0     synce1
	if logEntry.State != nil && logEntry.Source != nil {
		if sDeviceConfig := p.SyncEDeviceByInterface(*logEntry.Source); sDeviceConfig != nil {
			extraValue[event.DEVICE] = sDeviceConfig.Name
			extraValue[event.NETWORK_OPTION] = sDeviceConfig.NetworkOption
			iface = *logEntry.Source
			tState := synce.StringToEECState(strings.ReplaceAll(*logEntry.State, "EEC_LOCKED_HO_ACQ", "EEC_LOCKED"))
			glog.Infof("STATE %s", tState)
			state = tState.ToPTPState()
			sDeviceConfig.LastClockState = state
			extraValue[event.EEC_STATE] = *logEntry.State
		}
	} else if logEntry.State == nil && logEntry.Source != nil && (logEntry.QL != synce.QL_DEFAULT_SSM || logEntry.ExtQl != synce.QL_DEFAULT_SSM) {
		if sDeviceConfig := p.SyncEDeviceByInterface(*logEntry.Source); sDeviceConfig != nil {
			iface = *logEntry.Source
			// now decide on clock quality
			if sDeviceConfig.ExtendedTlv == synce.ExtendedTLV_DISABLED && logEntry.QL != synce.QL_DEFAULT_SSM {
				extraValue[event.DEVICE] = sDeviceConfig.Name
				extraValue[event.NETWORK_OPTION] = sDeviceConfig.NetworkOption
				extraValue[event.QL] = logEntry.QL
				sDeviceConfig.LastQLState[*logEntry.Source] = &synce.QualityLevelInfo{
					Priority:    0,
					SSM:         logEntry.QL,
					ExtendedSSM: synce.QL_DEFAULT_ENHSSM,
				}
				clockQuality, _ = sDeviceConfig.ClockQuality(synce.QualityLevelInfo{
					Priority:    0,
					SSM:         logEntry.QL,
					ExtendedSSM: 0,
				})
				state = sDeviceConfig.LastClockState
				if p.c == nil { // only update metrics if no socket is used
					UpdateSynceQLMetrics(syncEProcessName, p.configName, iface, sDeviceConfig.NetworkOption, sDeviceConfig.Name, "SSM", logEntry.QL)
					UpdateSynceQLMetrics(syncEProcessName, p.configName, iface, sDeviceConfig.NetworkOption, sDeviceConfig.Name, "Extended SSM", synce.QL_DEFAULT_ENHSSM)
					UpdateSynceClockQlMetrics(syncEProcessName, p.configName, iface, sDeviceConfig.NetworkOption, sDeviceConfig.Name, int(logEntry.QL)+int(synce.QL_DEFAULT_ENHSSM))
				}
			} else if sDeviceConfig.ExtendedTlv == synce.ExtendedTLV_ENABLED {
				var lastQLState *synce.QualityLevelInfo
				var ok bool
				iface = *logEntry.Source
				if lastQLState, ok = sDeviceConfig.LastQLState[*logEntry.Source]; !ok || lastQLState == nil {
					lastQLState = &synce.QualityLevelInfo{
						Priority:    0,
						SSM:         logEntry.QL,
						ExtendedSSM: logEntry.ExtQl,
					}
					sDeviceConfig.LastQLState[*logEntry.Source] = lastQLState
				}
				if lastQLState.SSM != synce.QL_DEFAULT_SSM && logEntry.ExtQl != synce.QL_DEFAULT_SSM { // then have both ql
					extraValue[event.NETWORK_OPTION] = sDeviceConfig.NetworkOption
					extraValue[event.DEVICE] = sDeviceConfig.Name
					extraValue[event.EXT_QL] = logEntry.ExtQl
					extraValue[event.QL] = lastQLState.SSM
					sDeviceConfig.LastQLState[*logEntry.Source].ExtendedSSM = logEntry.ExtQl
					clockQuality, _ = sDeviceConfig.ClockQuality(synce.QualityLevelInfo{
						SSM:         lastQLState.SSM,
						ExtendedSSM: lastQLState.ExtendedSSM,
						Priority:    0,
					})
					if p.c == nil {
						UpdateSynceQLMetrics(syncEProcessName, p.configName, iface, sDeviceConfig.NetworkOption, sDeviceConfig.Name, "SSM", lastQLState.SSM)
						UpdateSynceQLMetrics(syncEProcessName, p.configName, iface, sDeviceConfig.NetworkOption, sDeviceConfig.Name, "Extended SSM", logEntry.ExtQl)
						UpdateSynceClockQlMetrics(syncEProcessName, p.configName, iface, sDeviceConfig.NetworkOption, sDeviceConfig.Name, int(lastQLState.SSM)+int(logEntry.ExtQl))
					}

					state = sDeviceConfig.LastClockState
				} else if logEntry.QL != synce.QL_DEFAULT_SSM { //else we have only QL
					lastQLState.SSM = logEntry.QL // wait for extTlv
				}
			}
			if clockQuality != "" {
				extraValue[event.CLOCK_QUALITY] = clockQuality
			}
		}
	}
	if len(extraValue) > 0 {
		glog.Info(extraValue)
		select {
		case p.eventCh <- event.EventChannel{
			ProcessName: event.SYNCE,
			State:       state,
			CfgName:     p.configName,
			IFace:       iface,
			Values:      extraValue,
			Time:        time.Now().UnixMilli(),
			WriteToLog: func() bool { // only write to log if there is something extra
				if len(extraValue) > 0 {
					return true
				}
				return false
			}(),
			Reset: false,
		}:
		default:
		}
	}

}
func (p *ptpProcess) SyncEDeviceByInterface(iface string) *synce.Config {
	if p.syncERelations != nil {
		for _, sConfig := range p.syncERelations.Devices {
			for _, name := range sConfig.Ifaces {
				if name == iface {
					return sConfig
				}
			}
		}
	}
	return nil
}

// SyncEDeviceByName ....
func (p *ptpProcess) SyncEDeviceByName(name string) *synce.Config {
	if p.syncERelations != nil {
		for _, sConfig := range p.syncERelations.Devices {
			if sConfig.Name == name {
				return sConfig
			}
		}
	}
	return nil
}

func (dn *Daemon) stopAllProcesses() {
	for _, p := range dn.processManager.process {
		if p != nil {
			glog.Infof("stopping process.... %s", p.name)
			// Stop dependencies in reverse order first
			if p.depProcess != nil {
				for i := len(p.depProcess) - 1; i >= 0; i-- {
					d := p.depProcess[i]
					if d != nil {
						glog.Info("Stopping %s", d.Name())
						d.CmdStop()
						d = nil
					}
				}
			}

			// Stop parent process
			glog.Info("Stopping %s", p.name)
			p.cmdStop()
			p.depProcess = nil
			p.hasCollectedMetrics = false

			// Cleanup metrics
			deleteMetrics(p.ifaces, p.haProfile, p.name, p.configName)

			if p.name == syncEProcessName && p.syncERelations != nil {
				deleteSyncEMetrics(p.name, p.configName, p.syncERelations)
			}

			glog.Info("Stopped %s ", p.name)
			p = nil
		}
	}
}

func (p *ptpProcess) getPTPClockID() (string, error) {
	leadingNic, found := p.nodeProfile.PtpSettings["leadingInterface"]
	if !found {
		return "", fmt.Errorf("leadingInterface not found in ptpProfile")
	}
	key := fmt.Sprintf("%s[%s]", dpll.ClockIdStr, leadingNic)
	leadingClockID, found := p.nodeProfile.PtpSettings[key]
	if !found {
		return "", fmt.Errorf("leading interface ClockId not found in ptpProfile")
	}
	id, err := strconv.ParseUint(leadingClockID, 10, 64)
	if err != nil {
		return "", fmt.Errorf("failed to parse clock ID string %s: %s", leadingClockID, err)
	}
	formatKey := fmt.Sprintf("clockIdFormat[%s]", leadingNic)
	format, found := p.nodeProfile.PtpSettings[formatKey]
	if found && format == "EUI-48" {
		// MAC address format
		return fmt.Sprintf("%06x.fffe.%06x",
			id&0x0000ffffff000000>>24, id&0xffffff), nil
	}
	// Default format is EUE-64. For Intel WPC, it is EUI-64 alike, but not strictly compliant.
	// So we will fix it
	return fmt.Sprintf("%06x.fffe.%06x",
		id&0xffffff0000000000>>40, id&0xffffff), nil
}

func (p *ptpProcess) sendPtp4lEvent() {
	clockID, err := p.getPTPClockID()
	if err != nil {
		glog.Error(err)
		clockID = "" // Set to empty string if error occurs
	}
	_ = clockID // Ensure linter sees the variable as used
	select {
	case p.eventCh <- event.EventChannel{
		ProcessName: event.PTP4l,
		State:       p.lastTransitionResult,
		CfgName:     p.configName,
		IFace:       p.tBCAttributes.trIfaceName,
		ClockType:   p.clockType,
		Time:        time.Now().UnixMilli(),
		Reset:       false,
		SourceLost:  p.lastTransitionResult != event.PTP_LOCKED,
		OutOfSpec:   false,
		Values: map[event.ValueType]any{
			event.ControlledPortsConfig: p.tBCAttributes.controlledPortsConfigFile,
			event.ClockIDKey:            clockID,
		},
	}:
	default:
	}
}
