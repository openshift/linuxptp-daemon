package daemon

import (
	"cmp"
	"context"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/alias"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/clock"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/clockmgr"
	dpllnl "github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/dpll-netlink"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/hardwareconfig"
	ptpnetwork "github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/network"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/pmc"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/ublox"
	ptpv2alpha1 "github.com/k8snetworkplumbingwg/ptp-operator/api/v2alpha1"
	ptpclient "github.com/k8snetworkplumbingwg/ptp-operator/pkg/client/clientset/versioned"

	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/config"

	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/dpll"

	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/event"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/ipc"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/leap"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/plugin"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/process"

	"github.com/golang/glog"
	ptpv1 "github.com/k8snetworkplumbingwg/ptp-operator/api/v1"
	"k8s.io/client-go/kubernetes"
)

// PtpNamespace is the namespace where PTP resources are managed.
// It defaults to "openshift-ptp" and can be overridden via the
// NAME_SPACE env var for OLMv1 AllNamespaces install mode.
var PtpNamespace = "openshift-ptp"

func init() {
	if ns := os.Getenv("NAME_SPACE"); ns != "" {
		PtpNamespace = ns
	}
}

const (
	PTP4L_CONF_FILE_PATH            = "/etc/ptp4l.conf"
	PTP4L_CONF_DIR                  = "/ptp4l-conf"
	ipcSocket                       = "/var/run/ptp/ipc.sock"
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
	ChronydSocketPath               = "/tmp/chrony/chronyd.sock"
	// Offset filter size is hardcoded to 64 for now. It covers 4 seconds with reporting rate 16x/second.
	// TODO: consider making it configurable
	offsetFilterSize = 64
	// defaultPtp4lOffsetEventWindowSize is the sliding window size for averaging ptp4l offsets
	// before sending them to the T-BC state machine. The window should cover ~1 second of
	// offset data. Set via PtpSettings["ptp4lOffsetEventWindowSize"]. Tune according to
	// the ptp4l message rate: 16 for 8275.1 (16 msg/s), 128 for 8275.2 (128 msg/s).
	defaultPtp4lOffsetEventWindowSize = 16
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

// Daemon is the main structure for linuxptp instance.
// It contains all the necessary data to run linuxptp instance.
type Daemon struct {
	// node name where daemon is running
	nodeName  string
	namespace string

	// kubeClient allows interaction with Kubernetes, including the node we are running on.
	kubeClient *kubernetes.Clientset

	ptpUpdate *LinuxPTPConfUpdate

	processManager *ProcessManager
	readyTracker   *ReadyTracker
	ctx            context.Context
	cancel         context.CancelFunc

	hwconfigs   *[]ptpv1.HwConfig
	hwconfigsMu sync.Mutex // protects hwconfigs from concurrent GPSD goroutine writes

	// Hardware config manager handles hardware configurations from HardwareConfig CRs
	hardwareConfigManager *hardwareconfig.HardwareConfigManager

	refreshNodePtpDevice *bool

	// channel ensure LinuxPTP.Run() exit when main function exits.
	// stopCh is created by main function and passed by Daemon via NewLinuxPTP()
	stopCh <-chan struct{}

	pmcPollInterval int

	// Allow vendors to include plugins
	pluginManager  plugin.PluginManager
	saFileWatcher  *fsnotify.Watcher
	ptpClient      *ptpclient.Clientset
	unknownPlugins []string

	interfaceResolver *ptpnetwork.InterfaceResolver
	syncStatusMu      sync.Mutex
}

// UpdateHardwareConfig implements controller.HardwareConfigUpdateHandler.
// It is invoked by the controller reconciler via HardwareConfigHandler
// (wired in cmd/main.go) to push the effective hardware configuration
// into the running daemon. The daemon forwards the update to its
// HardwareConfigManager which resolves and caches DPLL/sysfs commands.
func (dn *Daemon) UpdateHardwareConfig(hwConfigs []ptpv2alpha1.HardwareConfig) error {
	return dn.hardwareConfigManager.UpdateHardwareConfig(hwConfigs)
}

// GetCurrentHardwareConfigs implements controller.HardwareConfigUpdateHandler.
// Returns the currently applied hardware configurations from the HardwareConfigManager.
// This is used at startup to initialize lastAppliedConfigs and avoid unnecessary restarts.
func (dn *Daemon) GetCurrentHardwareConfigs() []ptpv2alpha1.HardwareConfig {
	return dn.hardwareConfigManager.CloneHardwareConfigs()
}

// getHoldoverParameters retrieves holdover parameters from HardwareConfig for a specific clock ID
// Returns nil if no hardware config is available or no parameters are configured for the clock
func (dn *Daemon) getHoldoverParameters(profileName string, clockID uint64) *ptpv2alpha1.HoldoverParameters {
	return dn.hardwareConfigManager.GetHoldoverParameters(profileName, clockID)
}

// getDPLLFlags retrieves DPLL monitoring flags from HardwareConfig for a specific clock ID.
// Returns nil if no hardware config is available or no flags are configured for the clock.
func (dn *Daemon) getDPLLFlags(profileName string, clockID uint64) *dpll.Flag {
	return dn.hardwareConfigManager.GetDPLLFlags(profileName, clockID)
}

// getInterfacesFromHardwareConfig derives interfaces from hardwareconfig structure for T-BC mode.
// It extracts the first interface from structure[*]->dpll->networkInterface.
// Returns an empty slice if no hardwareconfig is available or no interfaces are found.
func (dn *Daemon) getInterfacesFromHardwareConfig(nodeProfile *ptpv1.PtpProfile) config.IFaces {
	if nodeProfile == nil || nodeProfile.Name == nil {
		return config.IFaces{}
	}

	// Get hardware configs for this profile
	hwProfiles := dn.hardwareConfigManager.GetHardwareConfigsForProfile(nodeProfile)
	if len(hwProfiles) == 0 {
		glog.V(2).Infof("No hardware configs found for profile %s", *nodeProfile.Name)
		return config.IFaces{}
	}

	var interfaces config.IFaces

	// Iterate through hardware profiles and extract interfaces from all subsystems.
	// All subsystems (not just the first) are included so that unmanaged DPLL subsystems
	// (e.g., Intel E830 CF1/CF2) enter the DPLL initialisation loop.
	for _, hwProfile := range hwProfiles {
		if hwProfile.ClockChain == nil || len(hwProfile.ClockChain.Structure) == 0 {
			continue
		}

		profileName := "<unnamed>"
		if hwProfile.Name != nil {
			profileName = *hwProfile.Name
		}
		glog.Infof("getInterfacesFromHardwareConfig: iterating %d subsystems in profile %s",
			len(hwProfile.ClockChain.Structure), profileName)

		for _, subsystem := range hwProfile.ClockChain.Structure {
			networkInterface, err := hardwareconfig.GetSubsystemNetworkInterface(hwProfile.ClockChain, subsystem.Name)
			if err != nil {
				glog.Infof("getInterfacesFromHardwareConfig: subsystem %s: no network interface: %v", subsystem.Name, err)
				continue
			}
			if networkInterface == "" {
				glog.Infof("getInterfacesFromHardwareConfig: subsystem %s: empty network interface, skipping", subsystem.Name)
				continue
			}

			// Get PHC ID for the interface
			phcID := ptpnetwork.GetPhcId(networkInterface)

			// Register in the alias store so convergeConfig can match this
			// interface against ptp4l interfaces that share the same PHC (e.g.
			// eno1 vs eth3 on an 8-port NIC where naming prefixes differ).
			if phcID != "" {
				alias.AddInterface(phcID, networkInterface)
				glog.Infof("getInterfacesFromHardwareConfig: registered iface %s phc %s in alias store", networkInterface, phcID)
			} else {
				glog.Warningf("getInterfacesFromHardwareConfig: could not get PHC ID for iface %s", networkInterface)
			}

			// Subsystems that have PhaseInputs configured are driven by ptp4l (they
			// receive a PTP time-receiver pin and run a ptp4l process).  Subsystems
			// without PhaseInputs are hardware-slaved (e.g., Intel E830 CF cards that
			// receive timing through hardware SDP/esync signals from the leader card)
			// and must not use PTP4l as their source: getDpllState() would otherwise
			// always return phaseStatus which stays DPLL_INVALID because no pps device
			// notification ever arrives for these cards.  PPS is the correct source for
			// hardware-slaved unmanaged DPLLs.
			source := event.PTP4l
			if len(subsystem.DPLL.PhaseInputs) == 0 {
				source = event.PPS
			}
			interfaces = append(interfaces, config.Iface{
				Name:     networkInterface,
				Source:   source,
				PhcId:    phcID,
				IsMaster: false,
			})
		}
	}

	return interfaces
}

// New LinuxPTP is called by daemon to generate new linuxptp instance
func New(
	nodeName string,
	namespace string,
	kubeClient *kubernetes.Clientset,
	ptpClient *ptpclient.Clientset,
	ptpUpdate *LinuxPTPConfUpdate,
	stopCh <-chan struct{},
	plugins []string,
	hwconfigs *[]ptpv1.HwConfig,
	refreshNodePtpDevice *bool,
	closeManager chan bool,
	pmcPollInterval int,
	tracker *ReadyTracker,
) *Daemon {
	RegisterMetrics(nodeName)
	InitializeOffsetMaps()
	pluginManager, unknownPlugins := registerPlugins(plugins)
	eventChannel := make(chan event.Event, 100)
	handlerEvents := make(chan event.Event, 100)
	ctx, cancel := context.WithCancel(context.Background())
	cache := ipc.NewCache(100)
	pm := &ProcessManager{
		process:   nil,
		eventsIn:  eventChannel,
		eventsOut: handlerEvents,
		clockMgr:  clockmgr.Init(nodeName, handlerEvents, Offset, ClockState, ClockClassMetrics, cache),
	}
	tracker.processManager = pm
	go ipc.NewLink(ipcSocket, cache).Run(context.TODO())

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
			// destroy and release the watcher
			saFileWatch.Close()
			saFileWatch = nil
		} else {
			glog.Infof("Watching %s for sa_file changes", PtpSecretMountDir)
		}
	}

	dn := &Daemon{
		nodeName:             nodeName,
		namespace:            namespace,
		kubeClient:           kubeClient,
		ptpClient:            ptpClient,
		ptpUpdate:            ptpUpdate,
		pluginManager:        pluginManager,
		unknownPlugins:       unknownPlugins,
		hwconfigs:            hwconfigs,
		interfaceResolver:    ptpnetwork.NewInterfaceResolver(),
		refreshNodePtpDevice: refreshNodePtpDevice,
		pmcPollInterval:      pmcPollInterval,
		processManager:       pm,
		readyTracker:         tracker,
		ctx:                  ctx,
		cancel:               cancel,
		stopCh:               stopCh,
		saFileWatcher:        saFileWatch,
	}
	dn.hardwareConfigManager = hardwareconfig.NewHardwareConfigManager(kubeClient, namespace, dn.interfaceResolver)
	pm.daemon = dn
	return dn
}

// Run in a for loop to listen for any LinuxPTPConfUpdate changes
// This function handles two types of configuration changes:
// 1. PtpConfig changes (via ConfigMap) - triggers UpdateCh
// 2. Authentication file changes (via Secret) - triggers fsnotify events (instant detection)
// Both trigger applyNodePTPProfiles() which restarts PTP processes WITHOUT restarting the pod
func (dn *Daemon) Run(ctx context.Context) {
	glog.Info("Daemon Run() started, waiting for configuration updates...")
	go dn.processManager.clockMgr.ProcessEvents(ctx)

	// Watch for clock-state transitions and write NodePtpDevice.status.sync.
	// Status is otherwise updated after applyNodePTPProfiles. The goroutine
	// exits when dn.stopCh is closed.
	go func() {
		syncStatusCh := dn.processManager.clockMgr.SyncStatusUpdateCh()
		for {
			select {
			case _, ok := <-syncStatusCh:
				if !ok {
					return
				}
				dn.runSyncStatusUpdate()
			case <-dn.stopCh:
				return
			}
		}
	}()

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
			dn.cancel()
			dn.processManager.stopAllProcesses()
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
	p.daemon = dn
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

// OSClockConfigs ...
type OSClockConfigs struct {
	phc2sysProfiles         []string
	haReferencedProfiles    []string
	haProfileNames          []string
	ntpFailOverProfileNames []string
	profileRunID            map[string]int
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

// NewOSClockConfigs ...
func NewOSClockConfigs(nodeProfiles []ptpv1.PtpProfile) OSClockConfigs {
	haProfileHierarchy := make(map[string][]string)
	result := OSClockConfigs{profileRunID: map[string]int{}}

	for runID, profile := range nodeProfiles {
		profileName := getProfileName(&profile)
		result.profileRunID[profileName] = runID

		if processShouldRun(&profile, phc2sysProcessName) {
			result.phc2sysProfiles = append(result.phc2sysProfiles, profileName)

			if referencedProfiles := listHaProfiles(&profile); len(referencedProfiles) > 0 {
				result.haProfileNames = append(result.haProfileNames, profileName)
				result.haReferencedProfiles = append(result.haReferencedProfiles, referencedProfiles...)

				haProfileHierarchy[profileName] = referencedProfiles
			}

			if profile.Plugins != nil && profile.Plugins["ntpfailover"] != nil {
				result.ntpFailOverProfileNames = append(result.ntpFailOverProfileNames, profileName)
			}
		}
	}

	if len(result.haProfileNames) > 0 {
		glog.Warningf("More than one HA Profile is active: %s", strings.Join(result.haProfileNames, ", "))
	}

	seen := slices.Collect(maps.Keys(result.profileRunID))
	for haProfileName, refrencedProfiles := range haProfileHierarchy {
		missing := []string{}
		for _, rProfileName := range refrencedProfiles {
			if !slices.Contains(seen, rProfileName) {
				missing = append(missing, rProfileName)
			}
		}
		if len(missing) > 0 {
			glog.Warningf("HA Profile %q is missing the following refrenced profiles: %s", haProfileName, strings.Join(missing, ", "))
		}
	}

	if len(result.phc2sysProfiles) > 0 {
		glog.Warningf("More than one Profile with phc2sys is active: %s", strings.Join(result.phc2sysProfiles, ", "))
	}
	return result
}

func processShouldRun(profile *ptpv1.PtpProfile, processName string) bool {
	switch processName {
	case ptp4lProcessName:
		// (Ptp4lOpts != nil && Ptp4lOpts != "") && (Ptp4lConf != nil && Ptp4lConf != "")
		return true
	case phc2sysProcessName:
		return (profile.Phc2sysOpts != nil && *profile.Phc2sysOpts != "") && (profile.Phc2sysConf != nil && *profile.Phc2sysConf != "")
	case ts2phcProcessName:
		return (profile.Ts2PhcOpts != nil && *profile.Ts2PhcOpts != "") && (profile.Ts2PhcConf != nil && *profile.Ts2PhcConf != "")
	case syncEProcessName:
		return (profile.Synce4lOpts != nil && *profile.Synce4lOpts != "") && (profile.Synce4lConf != nil && *profile.Synce4lConf != "")
	case chronydProcessName:
		return (profile.ChronydOpts != nil && *profile.ChronydOpts != "") && (profile.ChronydConf != nil && *profile.ChronydConf != "")
	}
	glog.Warningf("Requesting if unknown process %q should run", processName)
	return false
}

func (dn *Daemon) applyNodePTPProfiles() error {
	dn.readyTracker.setConfig(false)

	glog.Infof("in applyNodePTPProfiles - starting to apply %d node profiles", len(dn.ptpUpdate.NodeProfiles))

	// Suppress T-BC FSM updates during teardown/restart so in-flight DPLL
	// events after ts2phc Reset cannot emit LOCKED→HOLDOVER / T-BC-STATUS s1.
	// Also skip NodePtpDevice status writes until apply finishes.
	if dn.processManager != nil && dn.processManager.clockMgr != nil {
		defer dn.processManager.clockMgr.SetApplying(false)
		dn.syncStatusMu.Lock()
		dn.processManager.clockMgr.SetApplying(true)
		dn.syncStatusMu.Unlock()
	}

	dn.processManager.stopAllProcesses()
	// All process should have been stopped,
	// clear process in process manager.
	// Assigning processManager.process to nil releases
	// the underlying slice to the garbage
	// collector (assuming there are no other
	// references).
	dn.processManager.process = nil
	dn.processManager.clockMgr.RemoveAllClocks()

	// Purge the alias store so stale interface→PHC mappings from a previous
	// config application do not persist. All interfaces will be re-registered
	// by RenderPtp4lConf (and getInterfacesFromHardwareConfig) below before
	// any event processing restarts.
	alias.ClearAliases()

	// Drop stale openshift_ptp_threshold series; each profile re-sets its own
	// in applyNodePtpProfile below, so removed profiles do not linger.
	Threshold.Reset()

	// Reset the offset-resolution maps. They are keyed by ptp4l config name,
	// which restarts at ptp4l.0.config on every apply, so without this a new
	// profile inherits the superseded profile's slaveIface/masterOffsetIface.
	// ptp4l emits "master offset" lines during UNCALIBRATED (before the SLAVE
	// transition), and those would resolve to the old follower interface,
	// poisoning the new BC/OC clock's leading interface so clock_state is emitted
	// for the wrong iface and never for the new follower.
	InitializeOffsetMaps()

	// All configs will be rebuild, and sockets recreated, so they can all be deleted
	_ = dn.cleanupTempFiles()

	// TODO:
	// compare nodeProfile with previous config,
	// only apply when nodeProfile changes

	// clear hwconfig before updating
	dn.hwconfigsMu.Lock()
	*dn.hwconfigs = []ptpv1.HwConfig{}
	dn.hwconfigsMu.Unlock()

	glog.Infof("updating NodePTPProfiles to:")
	runID := 0
	slices.SortFunc(dn.ptpUpdate.NodeProfiles, func(a, b ptpv1.PtpProfile) int {
		aHasPhc2sysOpts := a.Phc2sysOpts != nil && *a.Phc2sysOpts != ""
		bHasPhc2sysOpts := b.Phc2sysOpts != nil && *b.Phc2sysOpts != ""
		// sorted in ascending order
		// here having phc2sysOptions is considered a high number
		if !aHasPhc2sysOpts && bHasPhc2sysOpts {
			return -1 //  a<b return -1
		} else if aHasPhc2sysOpts && !bHasPhc2sysOpts {
			return 1 //  a>b return
		}
		return cmp.Compare(*a.Name, *b.Name)
	})

	relations := reconcileRelatedProfiles(dn.ptpUpdate.NodeProfiles)

	// Update PtpConfig in hardware config manager for clock chain resolution
	// This is done after sorting and reconciliation to ensure we use the same
	// processed profiles that will actually be applied. The NodeProfiles are already
	// filtered to be relevant for this node by calculateNodeProfiles in the controller.
	if len(dn.ptpUpdate.NodeProfiles) > 0 {
		ptpConfig := &ptpv1.PtpConfig{
			Spec: ptpv1.PtpConfigSpec{
				Profile: dn.ptpUpdate.NodeProfiles,
			},
		}
		dn.hardwareConfigManager.SetPtpConfig(ptpConfig)
		glog.Infof("Updated PtpConfig in hardware config manager with %d profiles (after sorting and reconciliation)", len(dn.ptpUpdate.NodeProfiles))
	}
	// Refresh interface resolver so RHEL 10 npN name mapping uses current system state
	if err := dn.interfaceResolver.Refresh(); err != nil {
		glog.Warningf("Failed to refresh interface resolver, name resolution may use stale data: %v", err)
	}

	osClockSettings := NewOSClockConfigs(dn.ptpUpdate.NodeProfiles)

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

		dn.interfaceResolver.ResolveProfileInterfaces(&profile)

		glog.Infof("Calling applyNodePtpProfile for profile %s with runID %d", *profile.Name, runID)
		err := dn.applyNodePtpProfile(runID, &profile, &osClockSettings)
		if err != nil {
			glog.Errorf("Failed to apply profile %s: %v", *profile.Name, err)
			return err
		}
		glog.Infof("Successfully applied profile: %s", *profile.Name)
		runID++
	}

	glog.Infof("All profiles applied, starting %d processes", len(dn.processManager.process))
	dn.pluginManager.SetEventChannel(dn.processManager.eventsIn)
	dn.processManager.StartProcesses(dn.ctx)
	dn.hwconfigsMu.Lock()
	dn.pluginManager.PopulateHwConfig(dn.hwconfigs)
	dn.hwconfigsMu.Unlock()
	*dn.refreshNodePtpDevice = true
	dn.readyTracker.setConfig(true)
	dn.doSyncStatusUpdate(true)
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
func (dn *Daemon) applyNodePtpProfile(runID int, nodeProfile *ptpv1.PtpProfile, osClockConfigs *OSClockConfigs) error {
	testDir, test := nodeProfile.PtpSettings["unitTest"]
	if test {
		configPrefix = testDir
	}
	var pluginErrors []error

	pluginErrors = dn.checkPlugins(nodeProfile, pluginErrors)
	pluginErrors = dn.initHardware(nodeProfile, pluginErrors)

	dn.reportPluginStatus(*nodeProfile.Name, pluginErrors)

	// Publish the profile's configured clock thresholds as the read-back
	// openshift_ptp_threshold gauge (config value, not a live measurement).
	th := resolvePTPThreshold(nodeProfile)
	updatePTPThresholdMetrics(nodeProfile, th)

	var err error

	var clockType event.ClockType
	profileClockType, profileClockTypefound := (*nodeProfile).PtpSettings["clockType"]
	var leadingNic string
	var upstreamPorts []string
	if profileClockTypefound {
		switch profileClockType {
		case TGM:
			clockType = event.GM
		case TBC:
			clockType = event.TBC
			leadingNic = (*nodeProfile).PtpSettings["leadingInterface"]
			if portsStr, ok := (*nodeProfile).PtpSettings["upstreamPort"]; ok {
				upstreamPorts = strings.Split(portsStr, ",")
			}
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

	clockCfgName := fmt.Sprintf("ptp4l.%d.config", runID)
	var c clock.Clock
	switch clockType {
	case event.GM:
		if c, err = clock.NewGM(clockCfgName, leap.GetUtcOffset, pmc.ActiveClient()); err != nil {
			return err
		}
	case event.TBC:
		var tbc *clock.TBC
		if tbc, err = clock.NewTBC(clockCfgName, leap.GetUtcOffset, pmc.ActiveClient()); err != nil {
			return err
		}
		c = tbc
		if leadingNic != "" {
			tbc.SetConfiguredLeadingInterface(leadingNic)
		}
	case event.BC:
		if c, err = clock.NewBC(clockCfgName, false, th); err != nil {
			return err
		}
	case event.OC:
		if c, err = clock.NewBC(clockCfgName, true, th); err != nil {
			return err
		}

	default:
		return fmt.Errorf("unsupported clock type %q for config %s", clockType, clockCfgName)
	}

	if err = dn.processManager.clockMgr.AddClock(c); err != nil {
		return fmt.Errorf("failed to register clock for profile %s: %v", *nodeProfile.Name, err)
	}

	env := ptpProcessEnv{
		runID:          runID,
		nodeProfile:    nodeProfile,
		clockType:      clockType,
		dn:             dn,
		leadingNic:     leadingNic,
		upstreamPorts:  upstreamPorts,
		osClockConfigs: osClockConfigs,
		hasFailover:    len(osClockConfigs.ntpFailOverProfileNames) > 0,
	}

	for _, pProcess := range ptpProcesses {
		configOpts := optsFor(pProcess, nodeProfile)
		if configOpts == nil || *configOpts == "" {
			glog.Infof("configOpts empty for profile %s, skipping process: %s", *nodeProfile.Name, pProcess)
			continue
		}
		glog.Infof("Processing %s for profile %s with opts: %s", pProcess, *nodeProfile.Name, *configOpts)

		var dprocess process.Process
		switch pProcess {
		case ptp4lProcessName:
			var ptpProc *ptpProcess
			ptpProc, err = NewPtp4lProcess(env)
			// Skip PMC creation for controlled profiles
			if controllingProfile, isControlled := (*nodeProfile).PtpSettings["controllingProfile"]; isControlled && controllingProfile != "" {
				// See DownstreamIWF
				glog.Infof("Skipping PMC monitoring for controlled profile %s", *nodeProfile.Name)
			} else if clockType == event.GM {
				glog.Infof("Skipping PMC monitoring for GM")
			} else {
				// TODO addScheduling
				pmcProcess := NewPMCProcess(runID, dn.processManager.eventsIn, clockType, nodeProfile, time.Duration(dn.pmcPollInterval)*time.Second)
				ptpProc.depProcess = append(ptpProc.depProcess, pmcProcess)
			}
			dprocess = ptpProc
		case phc2sysProcessName:
			dprocess, err = NewPhc2sysProcess(env)
		case ts2phcProcessName:
			if leap.LeapMgr != nil {
				leap.LeapMgr.SetPtp4lConfigPath(fmt.Sprintf("ptp4l.%d.config", runID))
			}
			var ts2phcProc *ptpProcess
			ts2phcProc, err = NewTs2phcProcess(env)

			if clockType == event.GM {
				// If a HardwareConfig defines a GNSS source, locate the serial port and any GNSS initialization commands.
				dn.setupGPSDAndGPSPipe(nodeProfile, ts2phcProc)
			}
			dn.setupDPLL(nodeProfile, clockType, ts2phcProc)
			dprocess = ts2phcProc
		case syncEProcessName:
			dprocess, err = NewSyncEProcess(env)
		case chronydProcessName:
			dprocess, err = NewChronydProcess(env)
		}

		if err != nil {
			return err
		}
		if dprocess == nil {
			continue
		}
		printNodeProfile(nodeProfile)
		dn.processManager.process = append(dn.processManager.process, dprocess)
		glog.Infof("Added %s process to process manager for profile %s", pProcess, *nodeProfile.Name)
	}

	dn.populateHAInterfaces(osClockConfigs)
	glog.Infof("Completed applyNodePtpProfile for profile %s, total processes in manager: %d", *nodeProfile.Name, len(dn.processManager.process))
	return nil
}

// Validate that all plugin names in the profile match registered plugins
func (dn *Daemon) checkPlugins(nodeProfile *ptpv1.PtpProfile, pluginErrors []error) []error {
	if nodeProfile.Plugins != nil {
		for pluginName := range nodeProfile.Plugins {
			if _, registered := dn.pluginManager.Plugins[pluginName]; !registered {
				pluginErrors = append(pluginErrors, fmt.Errorf(
					"unknown plugin '%s' in profile '%s' (possible typo in hardware plugin configuration)",
					pluginName, *nodeProfile.Name,
				))
			}
		}
	}
	return pluginErrors
}

// Check if hardware configs are available for this profile
// If hardware configs arrive later, reconciliation will re-apply the profile
func (dn *Daemon) initHardware(nodeProfile *ptpv1.PtpProfile, pluginErrors []error) []error {
	if dn.hardwareConfigManager.ReadyHardwareConfigForProfile(*nodeProfile.Name) {
		glog.Infof("Using hardware configs for PTP profile %s instead of plugins", *nodeProfile.Name)
		if err := dn.hardwareConfigManager.ApplyHardwareConfigsForProfile(nodeProfile); err != nil {
			glog.Errorf("Failed to apply hardware configs for profile %s: %v", *nodeProfile.Name, err)
			// Fall back to plugins
			errs := dn.pluginManager.OnPTPConfigChange(nodeProfile)
			pluginErrors = append(pluginErrors, errs...)
		}
	} else {
		glog.Infof("No hardware configs found for PTP profile %s, using plugins", *nodeProfile.Name)
		errs := dn.pluginManager.OnPTPConfigChange(nodeProfile)
		pluginErrors = append(pluginErrors, errs...)
	}
	return pluginErrors
}

func processConfigFor(dprocess *ptpProcess, clockType event.ClockType, eventCh chan event.Event) config.ProcessConfig {
	cfg := config.ProcessConfig{
		ClockType:       clockType,
		ConfigName:      dprocess.configName,
		EventChannel:    eventCh,
		InitialPTPState: event.PTP_FREERUN,
	}
	if dprocess.ptpClockThreshold != nil {
		cfg.GMThreshold = config.Threshold{
			Max:             dprocess.ptpClockThreshold.MaxOffsetThreshold,
			Min:             dprocess.ptpClockThreshold.MinOffsetThreshold,
			HoldOverTimeout: dprocess.ptpClockThreshold.HoldOverTimeout,
		}
	}
	return cfg
}

func (dn *Daemon) setupGPSDAndGPSPipe(nodeProfile *ptpv1.PtpProfile, dprocess *ptpProcess) {
	var gnssInitCmds ublox.CommandList
	if nodeProfile.Name != nil && dn.hardwareConfigManager.ReadyHardwareConfigForProfile(*nodeProfile.Name) {
		gnssPort, gnssErr := dn.hardwareConfigManager.GetGNSSSerialPort(nodeProfile)
		if gnssErr != nil {
			glog.Warningf("HardwareConfig GNSS device detection failed: %v", gnssErr)
		} else if gnssPort != "" {
			glog.Infof("HardwareConfig GNSS device detected: %s (overriding ts2phc config)", gnssPort)
			dprocess.gnssSerialPort = gnssPort
		}

		gnssInitCmds = dn.hardwareConfigManager.GetGNSSInitCommands(nodeProfile)
		glog.Infof("HardwareConfig GNSS initialization added %d additional commands", len(gnssInitCmds))
	}

	if dprocess.gnssSerialPort == "" {
		dprocess.gnssSerialPort = GPSDDefaultGNSSSerialPort
		glog.Warningf("Setting GNSS serial port to %s", dprocess.gnssSerialPort)
	}
	gmInterface := dprocess.ifaces.GetLeadingInterface().Name
	glog.Infof("Working with GNSS serial port %s, leading iface %s", dprocess.gnssSerialPort, gmInterface)

	// Record ublox init results (MON-HW, etc.) to NodePtpDevice status.
	// Replaces any previous "gnss" entries to avoid duplicates on re-init.
	// Additionally store on HardwareConfigManager when that path is active.
	gnssResultsFn := func(results []string) {
		dn.hwconfigsMu.Lock()
		defer dn.hwconfigsMu.Unlock()
		// Remove previous gnss entries
		filtered := (*dn.hwconfigs)[:0]
		for _, hw := range *dn.hwconfigs {
			if hw.DeviceID != "gnss" {
				filtered = append(filtered, hw)
			}
		}
		// Add current results
		for _, result := range results {
			filtered = append(filtered, ptpv1.HwConfig{
				DeviceID: "gnss",
				Status:   result,
			})
		}
		*dn.hwconfigs = filtered
	}

	gpsdProcess := NewGpsdProcess(dprocess.gnssSerialPort, gmInterface, dprocess.messageTag, gnssInitCmds, gnssResultsFn, nodeProfile, dn.processManager.eventsIn, processConfigFor(dprocess, dprocess.clockType, dn.processManager.eventsIn))
	dprocess.depProcess = append(dprocess.depProcess, gpsdProcess)
	// init gpspipe
	gpsPipeProcess := NewGpsPipeProcess(dprocess.messageTag, nodeProfile, dn.processManager.eventsIn)
	dprocess.depProcess = append(dprocess.depProcess, gpsPipeProcess)
	dprocess.conditions = map[process.Action]process.Condition{
		process.ActionStart: process.OnProcessUp{
			Source:     event.GPSD,
			ConfigName: dprocess.configName,
		},
	}
}

func (dn *Daemon) setupDPLL(nodeProfile *ptpv1.PtpProfile, clockType event.ClockType, dprocess *ptpProcess) error {
	// TODO: Try to inject DPLL depProcess via plugin ?
	var localMaxHoldoverOffSet uint64 = dpll.LocalMaxHoldoverOffSet
	var localHoldoverTimeout uint64 = dpll.LocalHoldoverTimeout
	var maxInSpecOffset uint64 = dpll.MaxInSpecOffset
	var inSyncConditionTh uint64 = dpll.MaxInSpecOffset
	var inSyncConditionTimes uint64 = 1
	var flags dpll.Flag
	var err error
	sInSyncConditionTh, found1 := nodeProfile.PtpSettings["inSyncConditionThreshold"]
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

	var clockID uint64
	phaseOffsetPinFilter := map[string]string{}
	// For T-BC mode with hardwareconfig, derive interfaces from hardwareconfig structure
	// instead of analyzing ts2phc configuration file
	var interfacesToUse config.IFaces
	if profileClockType(nodeProfile) == TBC && dn.hardwareConfigManager.ReadyHardwareConfigForProfile(*nodeProfile.Name) {
		interfacesToUse = dn.getInterfacesFromHardwareConfig(nodeProfile)
		if len(interfacesToUse) > 0 {
			glog.Infof("Using interfaces from hardwareconfig for T-BC profile %s: %v", *nodeProfile.Name, interfacesToUse)
		} else {
			glog.Warningf("Failed to derive interfaces from hardwareconfig for T-BC profile %s, falling back to ts2phc config", *nodeProfile.Name)
			interfacesToUse = dprocess.ifaces
		}
	} else {
		interfacesToUse = dprocess.ifaces
	}

	for _, iface := range interfacesToUse {
		var eventSource []event.EventSource
		if iface.Source == event.GNSS || iface.Source == event.PPS ||
			(iface.Source == event.PTP4l && profileClockType(nodeProfile) == TBC) {
			if nodeProfile.PtpSettings[dpll.PtpSettingsDpllIgnoreKey(iface.Name)] == "true" {
				glog.Infof("Init dpll: Skipping dpll for %s", iface.Name)
				continue
			}

			flags = 0 // Default to 0 = no flags set

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
				var i uint64
				i, err = strconv.ParseUint(v, 10, 64)
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
					clockID = i
				}
				if k == dpll.PtpSettingsDpllFlagsKey(iface.Name) {
					flags = dpll.Flag(i)
				}
			}

			// Try to get holdover parameters from HardwareConfig (new system)
			// This takes precedence over plugin-provided values for better declarative configuration
			profileName := ""
			if nodeProfile.Name != nil {
				profileName = *nodeProfile.Name
			}
			holdoverParams := dn.getHoldoverParameters(profileName, clockID)
			if holdoverParams != nil {
				// HardwareConfig provides holdover parameters - use them
				maxInSpecOffset = holdoverParams.MaxInSpecOffset
				localMaxHoldoverOffSet = holdoverParams.LocalMaxHoldoverOffset
				localHoldoverTimeout = holdoverParams.LocalHoldoverTimeout
				glog.Infof("Using holdover parameters from HardwareConfig for clock %#x: MaxInSpec=%dns, LocalMaxOffset=%dns, Timeout=%ds",
					clockID, maxInSpecOffset, localMaxHoldoverOffSet, localHoldoverTimeout)
			} else {
				// Fall back to plugin/profile settings (backward compatibility)
				glog.Infof("Using holdover parameters from profile/plugin for clock %#x: MaxInSpec=%dns, LocalMaxOffset=%dns, Timeout=%ds",
					clockID, maxInSpecOffset, localMaxHoldoverOffSet, localHoldoverTimeout)
			}

			// Try to get DPLL flags from HardwareConfig (new system)
			// This takes precedence over plugin-provided values
			hwFlags := dn.getDPLLFlags(profileName, clockID)
			if hwFlags != nil {
				flags = *hwFlags
				glog.Infof("Using DPLL flags from HardwareConfig for clock %#x: %d", clockID, flags)
			}

			// Hardware-slaved DPLLs (e.g. E830 CF cards, identified by
			// FlagOnlyPhaseStatus) never perform holdover
			if flags&dpll.FlagOnlyPhaseStatus == dpll.FlagOnlyPhaseStatus {
				localMaxHoldoverOffSet = 0
				localHoldoverTimeout = 1 //do not divide by zero in case it is ever used
				maxInSpecOffset = 0
				glog.Infof("Resetting holdover parameters for %s (FlagOnlyPhaseStatus): not applicable for hardware-slaved DPLLs", iface.Name)
			}

			eventSource = []event.EventSource{iface.Source}
			// pass array of ifaces which has source + clockId -
			// here we have multiple dpll objects identified by clock id
			// depends on will be either PPS or  GNSS,
			// ONLY the one with GNSS dependency will go to HOLDOVER
			dpllDaemon := dpll.NewDpll(clockID, localMaxHoldoverOffSet, localHoldoverTimeout,
				maxInSpecOffset, iface.Name, eventSource, dpll.NONE, dn.GetPhaseOffsetPinFilter(nodeProfile),
				// Used only in T-BC in-sync condition:
				inSyncConditionTh, inSyncConditionTimes, flags,
				processConfigFor(dprocess, clockType, dn.processManager.eventsIn), nodeProfile)
			glog.Infof("depending on %s", dpllDaemon.DependsOn())
			// Set hardwareconfig handler if hardwareconfig manager is available
			dpllDaemon.SetHardwareConfigHandler(func(devices []*dpllnl.DoDeviceGetReply) error {
				return dn.hardwareConfigManager.ProcessDPLLDeviceNotifications(devices)
			})
			dprocess.depProcess = append(dprocess.depProcess, dpllDaemon)
		}
	}
	return nil
}

// GetPhaseOffsetPinFilter returns a map of phase offset pin filters from the node profile.
func (dn *Daemon) GetPhaseOffsetPinFilter(nodeProfile *ptpv1.PtpProfile) map[string]map[string]string {
	phaseOffsetPinFilter := map[string]map[string]string{}
	for k, v := range (*nodeProfile).PtpSettings {
		if strings.Contains(k, "phaseOffsetFilter") {
			filterKey := strings.Split(k, ".")
			property := filterKey[len(filterKey)-1]
			clockIDStr := filterKey[len(filterKey)-2]
			if len(phaseOffsetPinFilter[clockIDStr]) == 0 {
				phaseOffsetPinFilter[clockIDStr] = map[string]string{}
			}
			phaseOffsetPinFilter[clockIDStr][property] = v
			continue
		}
	}
	return phaseOffsetPinFilter
}

func getProfileName(profile *ptpv1.PtpProfile) string {
	profileName := "<nil>"
	if profile != nil && profile.Name != nil {
		profileName = *profile.Name
	}
	return profileName
}

// resolvePTPThreshold resolves a profile's clock thresholds (max/min offset in
// ns, holdover timeout in secs), applying the same defaulting as
// cloud-event-proxy's UpdatePTPThreshold. It is the single source of truth for
// both the openshift_ptp_threshold gauge and the BC/OC clock state machine.
func resolvePTPThreshold(nodeProfile *ptpv1.PtpProfile) event.PtpClockThreshold {
	maxOffset, minOffset, holdoverTimeout := int64(100), int64(-100), int64(5)
	if th := nodeProfile.PtpClockThreshold; th != nil {
		if th.MaxOffsetThreshold > 0 {
			maxOffset = th.MaxOffsetThreshold
		}
		if th.MinOffsetThreshold > maxOffset {
			minOffset = maxOffset - 1 // keep min one ns below max
		} else {
			minOffset = th.MinOffsetThreshold
		}
		if th.HoldOverTimeout > 0 {
			holdoverTimeout = th.HoldOverTimeout
		}
	} else if isNtpFailoverEnabled(nodeProfile) {
		maxOffset, minOffset = 1000, -1000
	}
	return event.PtpClockThreshold{
		MaxOffsetThreshold: maxOffset,
		MinOffsetThreshold: minOffset,
		HoldOverTimeout:    holdoverTimeout,
	}
}

func haSocketOpts(osClockConfigs *OSClockConfigs) string {
	socketOptions := []string{}
	for _, profile := range osClockConfigs.haReferencedProfiles {
		if runID, ok := osClockConfigs.profileRunID[profile]; ok {
			socketOptions = append(socketOptions, "-z "+getPtp4lSocket(configPrefix, runID))
		}
	}
	return strings.Join(socketOptions, " ")
}

func (dn *Daemon) populateHAInterfaces(osClockConfigs *OSClockConfigs) {
	phc2sysProcesses := dn.processManager.findProcessesByName(phc2sysProcessName)
	ptp4lProcesses := dn.processManager.findProcessesByName(ptp4lProcessName)

	if len(phc2sysProcesses) > 1 {
		glog.Warning("there is more than one phc2sys process")
	}

	// note there should only ever be <= 1 phc2sys process but
	// looping also handles the 0 case nicely
	for _, proc := range phc2sysProcesses {
		phc2sysProcess := proc.(*ptpProcess)
		if !slices.Contains(osClockConfigs.haProfileNames, getProfileName(proc.Profile())) {
			continue
		}

		for _, ptp4lProc := range ptp4lProcesses {
			ptp4lProcess, ok := ptp4lProc.(*ptpProcess)
			if !ok {
				continue
			}
			profileName := getProfileName(ptp4lProcess.Profile())
			if slices.Contains(osClockConfigs.haReferencedProfiles, profileName) {
				var ifaces []string
				for _, iface := range ptp4lProcess.ifaces {
					ifaces = append(ifaces, iface.Name)
				}
				phc2sysProcess.haProfile[profileName] = ifaces
			}
		}
	}
}
