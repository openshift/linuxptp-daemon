package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/golang/glog"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/kubernetes"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"

	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/config"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/controller"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/daemon"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/features"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/leap"

	ptpv1 "github.com/k8snetworkplumbingwg/ptp-operator/api/v1"
	ptpv2alpha1 "github.com/k8snetworkplumbingwg/ptp-operator/api/v2alpha1"
	ptpclient "github.com/k8snetworkplumbingwg/ptp-operator/pkg/client/clientset/versioned"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

// Git commit of current build set at build time
var GitCommit = "Undefined"

const labelRetries = 5
const labelRetryInterval = time.Second

type cliParams struct {
	updateInterval            int
	profileDir                string
	pmcPollInterval           int
	useController             bool
	enablePtpConfigController bool
}

var (
	scheme = runtime.NewScheme()
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(ptpv1.AddToScheme(scheme))
	// Register HardwareConfig types from ptp-operator v2alpha1
	utilruntime.Must(ptpv2alpha1.AddToScheme(scheme))
}

// Parse Command line flags
func (cp *cliParams) flagInit() {
	flag.IntVar(&cp.updateInterval, "update-interval", config.DefaultUpdateInterval,
		"Interval to update PTP status")
	flag.StringVar(&cp.profileDir, "linuxptp-profile-path", config.DefaultProfilePath,
		"profile to start linuxptp processes")
	flag.IntVar(&cp.pmcPollInterval, "pmc-poll-interval", config.DefaultPmcPollInterval,
		"Interval for periodical PMC poll")
	flag.BoolVar(&cp.useController, "use-controller", true,
		"Use Kubernetes controller manager (required for HardwareConfig support)")
	flag.BoolVar(&cp.enablePtpConfigController, "enable-ptpconfig-controller", false,
		"Enable PtpConfig controller to watch PtpConfig CRs (default: false, uses file-based config)")
	flag.Parse()
	cp.debugPrint()
}

func (cp *cliParams) debugPrint() {
	glog.Infof("resync period set to: %d [s]", cp.updateInterval)
	glog.Infof("linuxptp profile path set to: %s", cp.profileDir)
	glog.Infof("pmc poll interval set to: %d [s]", cp.pmcPollInterval)
	glog.Infof("use controller: %v", cp.useController)
	glog.Infof("enable PtpConfig controller: %v", cp.enablePtpConfigController)
}

func main() {

	fmt.Printf("Git commit: %s\n", GitCommit)
	cp := &cliParams{}
	cp.flagInit()

	cfg, err := config.GetKubeConfig()
	if err != nil {
		glog.Errorf("get kubeconfig failed: %v", err)
		return
	}
	glog.Infof("successfully get kubeconfig")

	kubeClient, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		glog.Errorf("cannot create new config for kubeClient: %v", err)
		return
	}

	ptpClient, err := ptpclient.NewForConfig(cfg)
	if err != nil {
		glog.Errorf("cannot create new config for ptpClient: %v", err)
		return
	}

	// The name of NodePtpDevice CR for this node is equal to the node name
	nodeName := os.Getenv("NODE_NAME")
	podName := os.Getenv("POD_NAME")
	if nodeName == "" {
		glog.Error("cannot find NODE_NAME environment variable")
		return
	}

	// The name of NodePtpDevice CR for this node is equal to the node name
	var stdoutToSocket = false
	if val, ok := os.LookupEnv("LOGS_TO_SOCKET"); ok && val != "" {
		if ret, err := strconv.ParseBool(val); err == nil {
			stdoutToSocket = ret
		}
	}

	plugins := make([]string, 0)

	if val, ok := os.LookupEnv("PLUGINS"); ok && val != "" {
		plugins = strings.Split(val, ",")
	}

	stopCh := make(chan struct{})
	defer close(stopCh)

	ptpConfUpdate, err := daemon.NewLinuxPTPConfUpdate()
	if err != nil {
		glog.Errorf("failed to create a ptp config update: %v", err)
		return
	}

	var labelErr error
	for i := 1; i <= labelRetries; i++ {
		// label the current linux-ptp-daemon pod with a nodeName label
		labelErr = labelPod(kubeClient, nodeName, podName)
		if labelErr != nil {
			glog.Errorf("failed to label linuxptp-daemon with node name, err: %v. Will retry (%d/%d)", labelErr, i, labelRetries)
			time.Sleep(labelRetryInterval)
		} else {
			// Labeling was successful so continue
			break
		}
	}
	if labelErr != nil {
		glog.Fatalf("Failed to label linuxptp-daemon with node name, err: %v", labelErr)
	}

	hwconfigs := []ptpv1.HwConfig{}
	refreshNodePtpDevice := true
	closeProcessManager := make(chan bool)
	lm, err := leap.New(kubeClient, daemon.PtpNamespace)
	if err != nil {
		glog.Error("failed to initialize Leap manager, ", err)
		return
	}
	go lm.Run()

	defer close(lm.Close)

	tracker := &daemon.ReadyTracker{}

	version := features.GetLinuxPTPPackageVersion()
	ocpVersion := features.GetOCPVersion()
	// TODO: version needs to be sent to cloud event proxy when we intergrate feature flags there
	features.SetFlags(version, ocpVersion)
	features.Flags.Print()

	daemonInstance := daemon.New(
		nodeName,
		daemon.PtpNamespace,
		stdoutToSocket,
		kubeClient,
		ptpConfUpdate,
		stopCh,
		plugins,
		&hwconfigs,
		&refreshNodePtpDevice,
		closeProcessManager,
		cp.pmcPollInterval,
		tracker,
	)
	go daemonInstance.Run()

	tickerPull := time.NewTicker(time.Second * time.Duration(cp.updateInterval))
	defer tickerPull.Stop()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGHUP, syscall.SIGINT, syscall.SIGTERM, syscall.SIGQUIT)

	// by default metrics is hosted here,if LOGS_TO_SOCKET variable is set then metrics are disabled
	if !stdoutToSocket { // if not sending metrics (log) out to a socket then host metrics here
		daemon.StartMetricsServer("0.0.0.0:9091")
	}

	daemon.StartReadyServer("0.0.0.0:8081", tracker, stdoutToSocket)

	// Wait for one ticker interval before loading the profile
	// This allows linuxptp-daemon connection to the cloud-event-proxy container to
	// be up and running before PTP state logs are printed.
	time.Sleep(time.Second * time.Duration(cp.updateInterval/2))

	// Determine and run the appropriate operation mode
	if cp.useController {
		if cp.enablePtpConfigController {
			runFullControllerMode(cfg, cp, nodeName, ptpConfUpdate, daemonInstance, ptpClient, nodeName, &hwconfigs, &refreshNodePtpDevice, tickerPull, sigCh, closeProcessManager)
		} else {
			runHybridMode(cfg, cp, nodeName, ptpConfUpdate, daemonInstance, ptpClient, nodeName, &hwconfigs, &refreshNodePtpDevice, tickerPull, sigCh, closeProcessManager)
		}
	} else {
		runLegacyMode(cp, nodeName, ptpConfUpdate, ptpClient, nodeName, &hwconfigs, &refreshNodePtpDevice, tickerPull, sigCh, closeProcessManager)
	}
}

// controllerManagerSetup holds the controller manager and its context
type controllerManagerSetup struct {
	mgr       ctrl.Manager
	mgrCtx    context.Context
	mgrCancel context.CancelFunc
}

// setupControllerManager creates and starts the controller manager with required controllers
func setupControllerManager(cfg *rest.Config, nodeName string, ptpConfUpdate *daemon.LinuxPTPConfUpdate, daemonInstance *daemon.Daemon, enablePtpConfigController bool) (*controllerManagerSetup, error) {
	glog.Info("Setting up controller manager")

	// Setup controller-runtime logger to use klog/glog
	ctrl.SetLogger(klog.NewKlogr())

	// Create manager
	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme:                 scheme,
		HealthProbeBindAddress: ":8082",
		LeaderElection:         false, // Disable leader election for daemon
		// PtpConfig is cluster-scoped, so don't restrict cache by namespace
	})
	if err != nil {
		return nil, fmt.Errorf("unable to start controller manager: %w", err)
	}

	// Add health checks
	if err = mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		return nil, fmt.Errorf("unable to set up health check: %w", err)
	}
	if err = mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		return nil, fmt.Errorf("unable to set up ready check: %w", err)
	}

	// Setup PtpConfig controller (optional)
	if enablePtpConfigController {
		glog.Info("Setting up PtpConfig controller (watching PtpConfig CRs)")
		ptpConfigReconciler := &controller.PtpConfigReconciler{
			Client:       mgr.GetClient(),
			Scheme:       mgr.GetScheme(),
			NodeName:     nodeName,
			ConfigUpdate: ptpConfUpdate,
		}

		if err = ptpConfigReconciler.SetupWithManager(mgr); err != nil {
			return nil, fmt.Errorf("unable to create controller for PtpConfig: %w", err)
		}
		glog.Info("PtpConfig controller registered successfully")
	} else {
		glog.Info("PtpConfig controller disabled - will use file-based configuration")
	}

	// Setup HardwareConfig controller (always enabled when controller manager is active)
	glog.Info("Setting up HardwareConfig controller")
	hwConfigReconciler := &controller.HardwareConfigReconciler{
		Client:   mgr.GetClient(),
		Scheme:   mgr.GetScheme(),
		NodeName: nodeName,
		// HardwareConfigHandler is the daemon instance. This allows the
		// HardwareConfig controller to call daemon.UpdateHardwareConfig(...)
		// when reconciled configs change. The daemon then forwards the
		// update to its HardwareConfigManager for resolution/caching.
		HardwareConfigHandler: daemonInstance,
		ConfigUpdate:          ptpConfUpdate, // Enable hardware config to trigger PTP restarts
	}

	if err = hwConfigReconciler.SetupWithManager(mgr); err != nil {
		return nil, fmt.Errorf("unable to create controller for HardwareConfig: %w", err)
	}
	glog.Info("HardwareConfig controller registered successfully")

	// Start the manager in a goroutine
	mgrCtx, mgrCancel := context.WithCancel(context.Background())
	go func() {
		glog.Info("Starting controller manager")
		if startErr := mgr.Start(mgrCtx); startErr != nil {
			glog.Errorf("problem running controller manager: %v", startErr)
		}
	}()

	glog.Info("Controller manager started successfully")

	return &controllerManagerSetup{
		mgr:       mgr,
		mgrCtx:    mgrCtx,
		mgrCancel: mgrCancel,
	}, nil
}

// runFullControllerMode runs in full controller mode with both PtpConfig and HardwareConfig controllers
func runFullControllerMode(cfg *rest.Config, _ *cliParams, nodeName string, ptpConfUpdate *daemon.LinuxPTPConfUpdate, daemonInstance *daemon.Daemon, ptpClient *ptpclient.Clientset, nodeNameForDevice string, hwconfigs *[]ptpv1.HwConfig, refreshNodePtpDevice *bool, tickerPull *time.Ticker, sigCh chan os.Signal, closeProcessManager chan bool) {
	glog.Info("Running in full controller mode - PtpConfig and HardwareConfig resources will be watched automatically")

	// Setup controller manager
	cmSetup, err := setupControllerManager(cfg, nodeName, ptpConfUpdate, daemonInstance, true)
	if err != nil {
		glog.Errorf("Failed to setup controller manager: %v", err)
		return
	}
	defer cmSetup.mgrCancel()

	// Trigger initial reconciliation by listing all PtpConfigs
	go func() {
		time.Sleep(2 * time.Second) // Give controller time to start
		glog.Info("Triggering initial PtpConfig reconciliation")

		// Force initial reconciliation by calling the controller directly
		ptpConfigReconciler := &controller.PtpConfigReconciler{
			Client:       cmSetup.mgr.GetClient(),
			Scheme:       cmSetup.mgr.GetScheme(),
			NodeName:     nodeName,
			ConfigUpdate: ptpConfUpdate,
		}

		// Trigger reconciliation for any existing PtpConfigs
		_, reconcileErr := ptpConfigReconciler.Reconcile(context.Background(), ctrl.Request{})
		if reconcileErr != nil {
			glog.Errorf("Initial reconciliation failed: %v", reconcileErr)
		}
	}()

	for {
		select {
		case <-tickerPull.C:
			glog.V(2).Infof("ticker pull (full controller mode)")
			// Run a loop to update the device status
			if *refreshNodePtpDevice {
				go daemon.RunDeviceStatusUpdate(ptpClient, nodeNameForDevice, hwconfigs)
				*refreshNodePtpDevice = false
			}
		case sig := <-sigCh:
			glog.Info("signal received, shutting down", sig)
			closeProcessManager <- true
			cmSetup.mgrCancel() // Stop the controller manager
			return
		}
	}
}

// runHybridMode runs in hybrid mode with HardwareConfig controller and file-based PtpConfig
func runHybridMode(cfg *rest.Config, cp *cliParams, nodeName string, ptpConfUpdate *daemon.LinuxPTPConfUpdate, daemonInstance *daemon.Daemon, ptpClient *ptpclient.Clientset, nodeNameForDevice string, hwconfigs *[]ptpv1.HwConfig, refreshNodePtpDevice *bool, tickerPull *time.Ticker, sigCh chan os.Signal, closeProcessManager chan bool) {
	glog.Info("Running in hybrid mode - HardwareConfig controller active, PtpConfig from files")

	// Setup controller manager
	cmSetup, err := setupControllerManager(cfg, nodeName, ptpConfUpdate, daemonInstance, false)
	if err != nil {
		glog.Errorf("Failed to setup controller manager: %v", err)
		return
	}
	defer cmSetup.mgrCancel()

	for {
		select {
		case <-tickerPull.C:
			glog.V(2).Infof("ticker pull (hybrid mode)")
			// Run a loop to update the device status
			if *refreshNodePtpDevice {
				go daemon.RunDeviceStatusUpdate(ptpClient, nodeNameForDevice, hwconfigs)
				*refreshNodePtpDevice = false
			}

			// Read PtpConfig from file
			if fileErr := readAndUpdatePtpConfigFromFile(cp.profileDir, nodeNameForDevice, ptpConfUpdate); fileErr != nil {
				glog.Errorf("Failed to read/update PtpConfig from file: %v", fileErr)
			}
		case sig := <-sigCh:
			glog.Info("signal received, shutting down", sig)
			closeProcessManager <- true
			cmSetup.mgrCancel() // Stop the controller manager
			return
		}
	}
}

// runLegacyMode runs in legacy file-based mode without any controllers
func runLegacyMode(cp *cliParams, _ string, ptpConfUpdate *daemon.LinuxPTPConfUpdate, ptpClient *ptpclient.Clientset, nodeNameForDevice string, hwconfigs *[]ptpv1.HwConfig, refreshNodePtpDevice *bool, tickerPull *time.Ticker, sigCh chan os.Signal, closeProcessManager chan bool) {
	glog.Info("Running in legacy file-based mode (no controllers)")

	for {
		select {
		case <-tickerPull.C:
			glog.V(2).Infof("ticker pull (legacy mode)")
			// Run a loop to update the device status
			if *refreshNodePtpDevice {
				go daemon.RunDeviceStatusUpdate(ptpClient, nodeNameForDevice, hwconfigs)
				*refreshNodePtpDevice = false
			}

			// Read PtpConfig from file
			if fileErr := readAndUpdatePtpConfigFromFile(cp.profileDir, nodeNameForDevice, ptpConfUpdate); fileErr != nil {
				glog.Errorf("Failed to read/update PtpConfig from file: %v", fileErr)
			}
		case sig := <-sigCh:
			glog.Info("signal received, shutting down", sig)
			closeProcessManager <- true
			return
		}
	}
}

// readAndUpdatePtpConfigFromFile reads PtpConfig from file and updates the daemon configuration
func readAndUpdatePtpConfigFromFile(profileDir, nodeName string, ptpConfUpdate *daemon.LinuxPTPConfUpdate) error {
	nodeProfile := filepath.Join(profileDir, nodeName)
	if _, err := os.Stat(nodeProfile); err != nil {
		return fmt.Errorf("error stating node profile %v: %w", nodeName, err)
	}
	nodeProfilesJSON, err := os.ReadFile(nodeProfile)
	if err != nil {
		return fmt.Errorf("error reading node profile: %w", err)
	}

	if err = ptpConfUpdate.UpdateConfig(nodeProfilesJSON); err != nil {
		return fmt.Errorf("error updating the node configuration using the profiles loaded: %w", err)
	}
	return nil
}

type patchStringValue struct {
	Op    string `json:"op"`
	Path  string `json:"path"`
	Value string `json:"value"`
}

func labelPod(kubeClient *kubernetes.Clientset, nodeName, podName string) (err error) {
	pod, err := kubeClient.CoreV1().Pods(daemon.PtpNamespace).Get(context.TODO(), podName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("error getting linuxptp-daemon pod, err=%s", err)
	}
	if pod == nil {
		return fmt.Errorf("could not find linux-ptp-daemon pod to label")
	}
	if nodeName != "" && strings.Contains(nodeName, ".") {
		nodeName = strings.Split(nodeName, ".")[0]
	}

	payload := []patchStringValue{{
		Op:    "replace",
		Path:  "/metadata/labels/nodeName",
		Value: nodeName,
	}}
	payloadBytes, _ := json.Marshal(payload)

	_, err = kubeClient.CoreV1().Pods(pod.GetNamespace()).Patch(context.TODO(), pod.GetName(), types.JSONPatchType, payloadBytes, metav1.PatchOptions{})
	if err != nil {
		return fmt.Errorf("could not label ns=%s pod %s, err=%s", pod.GetName(), pod.GetNamespace(), err)
	}
	glog.Infof("Pod %s labelled successfully.", pod.GetName())
	return nil
}
