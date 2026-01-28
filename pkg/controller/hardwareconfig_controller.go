// Package controller provides Kubernetes controller functionality for PTP configuration.
package controller

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/golang/glog"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/daemon"
	ptpv1 "github.com/k8snetworkplumbingwg/ptp-operator/api/v1"
	ptpv2alpha1 "github.com/k8snetworkplumbingwg/ptp-operator/api/v2alpha1"
)

const (
	backoffTime = 15 * time.Second
)

// HardwareConfigUpdateHandler defines the interface implemented by the daemon (or a test double)
// to receive effective hardware configuration updates computed by this controller.
//
// Production wiring:
// - In cmd/main.go, the reconciler is constructed with HardwareConfigHandler: daemonInstance
// - The daemon implements UpdateHardwareConfig and forwards to its internal HardwareConfigManager
// - Therefore, calls here reach dn.hardwareConfigManager.UpdateHardwareConfig(...)
type HardwareConfigUpdateHandler interface {
	UpdateHardwareConfig(hwConfigs []ptpv2alpha1.HardwareConfig) error
	// GetCurrentHardwareConfigs returns the currently applied hardware configurations
	// This is used at startup to initialize lastAppliedConfigs and avoid unnecessary restarts
	GetCurrentHardwareConfigs() []ptpv2alpha1.HardwareConfig
}

// HardwareConfigReconciler watches HardwareConfig CRs and forwards the node's effective
// configuration to the daemon via HardwareConfigHandler (see cmd/main.go for wiring).
type HardwareConfigReconciler struct {
	client.Client
	Scheme *runtime.Scheme

	// NodeName is the name of the node this daemon is running on
	NodeName string

	// HardwareConfigHandler is set to the daemon instance in main (cmd/main.go),
	// so calls below (UpdateHardwareConfig) ultimately invoke the daemon method
	// (dn *Daemon).UpdateHardwareConfig, which forwards to dn.hardwareConfigManager.
	HardwareConfigHandler HardwareConfigUpdateHandler

	// ConfigUpdate is used to trigger PTP process restarts when hardware config changes
	// affect currently active PTP profiles. This field is required and must always be set.
	ConfigUpdate HardwareConfigRestartTrigger

	// Recorder is used to record events for HardwareConfig resources
	Recorder record.EventRecorder

	// lastAppliedConfigs tracks the last successfully applied hardware configurations
	// Used to detect actual changes and avoid unnecessary restarts.
	// Configs are stored before resolution (raw user spec) to detect user changes.
	// Key is the config name (HardwareConfigs are cluster-scoped, so name is unique).
	// Protected by reconcileMutex - all reads/writes happen inside reconcileAllConfigs.
	lastAppliedConfigs map[string]ptpv2alpha1.HardwareConfig

	// reconcileMutex prevents concurrent reconciliations to avoid race conditions
	// when updating lastAppliedConfigs and triggering restarts.
	// All access to lastAppliedConfigs must happen inside reconcileAllConfigs (which holds this mutex).
	reconcileMutex sync.Mutex

	// File watching for hybrid mode (file-based PtpConfig)
	profileDir      string            // Directory containing PtpConfig files (e.g., /etc/linuxptp)
	fileWatcher     *fsnotify.Watcher // Watcher for profile file changes
	fileWatchStopCh chan struct{}     // Channel to stop file watching goroutine
	fileWatchMutex  sync.Mutex        // Protects file watcher operations

	// Debouncing for profile change triggers
	profileChangeMutex sync.Mutex  // Protects debounce timer
	profileChangeTimer *time.Timer // Debounce timer for profile changes
}

// HardwareConfigRestartTrigger interface for triggering PTP restarts
type HardwareConfigRestartTrigger interface {
	TriggerRestartForHardwareChange() error
	GetCurrentPTPProfiles() []string
}

// Reconcile handles HardwareConfig changes and updates the daemon hardware configuration
// It also handles PtpConfig changes (via watch) to recalculate applicable hardware configs
// when active PTP profiles change.
// All change detection is handled inside reconcileAllConfigs (protected by reconcileMutex).
func (r *HardwareConfigReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	// Log the trigger for debugging
	if req.Namespace == "" && req.Name == "" {
		glog.V(2).Infof("Profile change triggered reconciliation for node %s", r.NodeName)
	} else {
		glog.V(2).Infof("Reconciling HardwareConfig name=%s namespace=%s", req.Name, req.Namespace)
	}

	// Delegate all work to reconcileAllConfigs which:
	// - Lists all HardwareConfigs
	// - Computes diff against lastAppliedConfigs
	// - Applies changes only if something changed
	// - Is protected by reconcileMutex to prevent concurrent access to lastAppliedConfigs
	return r.reconcileAllConfigs(ctx)
}

// validateHardwareConfigSeparation ensures no conflicts between hardware configs
func (r *HardwareConfigReconciler) validateHardwareConfigSeparation(hwConfigs []ptpv2alpha1.HardwareConfig) error {
	// Track DPLL subsystems and PTP profile references to detect conflicts
	dpllSubsystems := make(map[string]string) // subsystem -> hardwareconfig name
	ptpProfiles := make(map[string]string)    // ptp profile -> hardwareconfig name

	for _, hwConfig := range hwConfigs {
		hwConfigName := hwConfig.Name

		// Check PTP profile conflicts
		relatedProfile := hwConfig.Spec.RelatedPtpProfileName
		if relatedProfile != "" {
			if existingHwConfig, exists := ptpProfiles[relatedProfile]; exists {
				return fmt.Errorf("PTP profile '%s' is referenced by multiple hardware configs: %s and %s",
					relatedProfile, existingHwConfig, hwConfigName)
			}
			ptpProfiles[relatedProfile] = hwConfigName
		}

		// Check DPLL subsystem conflicts
		if hwConfig.Spec.Profile.ClockChain != nil {
			for _, subsystem := range hwConfig.Spec.Profile.ClockChain.Structure {
				// Get network interface for unique identification
				netIface := subsystem.DPLL.NetworkInterface
				if netIface == "" && len(subsystem.Ethernet) > 0 && len(subsystem.Ethernet[0].Ports) > 0 {
					netIface = subsystem.Ethernet[0].Ports[0]
				}
				subsystemKey := fmt.Sprintf("%s:%s", subsystem.Name, netIface)
				if existingHwConfig, exists := dpllSubsystems[subsystemKey]; exists {
					return fmt.Errorf("configuration error: DPLL subsystem '%s' (NetworkInterface: %s) is addressed by multiple hardware configs: %s and %s",
						subsystem.Name, netIface, existingHwConfig, hwConfigName)
				}
				dpllSubsystems[subsystemKey] = hwConfigName
			}
		}
	}

	glog.Infof("Hardware config validation passed: %d unique PTP profiles, %d unique DPLL subsystems",
		len(ptpProfiles), len(dpllSubsystems))
	return nil
}

// reconcileAllConfigs calculates the effective hardware configuration for this node by examining all HardwareConfigs
// It compares the new configs with last applied configs before resolution to detect user changes.
// Only resolves and applies configs if they actually changed.
// Protected by reconcileMutex to prevent concurrent reconciliations and TOCTOU race conditions.
//
//nolint:unparam // result 0 (ctrl.Result) is always empty, but required to match Reconcile signature
func (r *HardwareConfigReconciler) reconcileAllConfigs(ctx context.Context) (ctrl.Result, error) {
	// Prevent concurrent reconciliations to avoid race conditions on lastAppliedConfigs
	// and duplicate hardware updates/restarts
	r.reconcileMutex.Lock()
	defer r.reconcileMutex.Unlock()

	// List all HardwareConfigs in the cluster
	hwConfigList := &ptpv2alpha1.HardwareConfigList{}
	if err := r.List(ctx, hwConfigList); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to list HardwareConfigs: %w", err)
	}

	// Validate hardware config separation before proceeding
	if err := r.validateHardwareConfigSeparation(hwConfigList.Items); err != nil {
		glog.Errorf("Hardware config validation failed: %v", err)
		// Don't return error to avoid controller crash, but log the issue
		// TODO: Set a status condition instead
		//  add a status condition to the HardwareConfig CRD to enable that
		// type HardwareConfigStatus struct {
		// 	MatchedNodes []MatchedNode      `json:"matchedNodes,omitempty" yaml:"matchedNodes,omitempty"`
		// 	Conditions   []metav1.Condition `json:"conditions,omitempty"
	}

	// Calculate the applicable hardware configurations for this node (BEFORE resolution)
	applicableConfigs := r.calculateNodeHardwareConfigs(ctx, hwConfigList.Items)

	// Compute diff once and reuse for change detection, description, and restart check
	// Note: lastAppliedConfigs is protected by reconcileMutex (held by caller)
	diff := diffHardwareConfigs(r.lastAppliedConfigs, applicableConfigs)

	if !diff.HasChanges() {
		glog.V(2).Infof("Hardware configurations unchanged, skipping update and restart")
		return ctrl.Result{}, nil
	}

	// Log detailed information about what changed
	changeDescription := describeConfigChanges(diff)
	glog.Infof("Hardware configurations changed (old: %d, new: %d). Changes: %s", len(r.lastAppliedConfigs), len(applicableConfigs), changeDescription)

	// Store old configs for restart check (before updating lastAppliedConfigs)
	oldConfigsForRestartCheck := deepCopyHardwareConfigMap(r.lastAppliedConfigs)

	// Update last applied configs before applying (protected by reconcileMutex)
	r.lastAppliedConfigs = deepCopyHardwareConfigMap(sliceToHardwareConfigMap(applicableConfigs))

	// Configs changed - resolve and apply them
	// Note: Resolution happens inside UpdateHardwareConfig in the hardwareconfig manager
	if len(applicableConfigs) > 0 {
		glog.Infof("Updating daemon hardware configuration with %d hardware configs for node %s", len(applicableConfigs), r.NodeName)

		// Send hardware configuration update to daemon (HardwareConfigHandler)
		if r.HardwareConfigHandler != nil {
			err := r.HardwareConfigHandler.UpdateHardwareConfig(applicableConfigs)
			if err != nil {
				// Record event and update status for failed configs
				r.recordUpdateFailure(ctx, applicableConfigs, err)
				glog.Errorf("Failed to update daemon hardware configuration: %v (will retry after backoff)", err)
				// Revert lastAppliedConfigs on failure so we retry (protected by reconcileMutex)
				r.lastAppliedConfigs = deepCopyHardwareConfigMap(oldConfigsForRestartCheck)
				// Requeue after a short delay to avoid immediate retry storms
				return ctrl.Result{RequeueAfter: backoffTime}, nil
			}
		}

		glog.Infof("Successfully updated daemon hardware configuration configs=%d", len(applicableConfigs))

		// Check if changed configs affect active profiles and trigger restart if needed
		// Reuse the diff computed earlier (no need to recompute)
		if r.checkIfChangedConfigsAffectActiveProfiles(diff) {
			// Log the reason for restart
			if len(oldConfigsForRestartCheck) == 0 {
				glog.Infof("Initial hardware config application detected (no previous configs). Applying %d config(s) that affect active PTP profiles, triggering restart", len(applicableConfigs))
			} else {
				glog.Infof("Hardware config change detected: %s. Changed configs affect active PTP profiles on node %s, triggering restart", changeDescription, r.NodeName)
			}
			if err := r.ConfigUpdate.TriggerRestartForHardwareChange(); err != nil {
				glog.Errorf("Failed to trigger PTP restart for hardware configuration change: %v", err)
			} else {
				glog.Infof("Successfully triggered PTP restart due to hardware configuration change")
			}
		} else {
			glog.Infof("Changed hardware configs do not require PTP restart (no active profiles affected)")
		}
	} else {
		glog.Infof("No applicable hardware configurations found for node %s", r.NodeName)

		// Clear hardware configurations if needed
		if r.HardwareConfigHandler != nil {
			err := r.HardwareConfigHandler.UpdateHardwareConfig([]ptpv2alpha1.HardwareConfig{})
			if err != nil {
				// Record event for failure
				r.recordUpdateFailure(ctx, []ptpv2alpha1.HardwareConfig{}, err)
				glog.Errorf("Failed to clear daemon hardware configuration: %v (will retry after backoff)", err)
				// Requeue after a short delay to avoid immediate retry storms
				return ctrl.Result{RequeueAfter: backoffTime}, nil
			}
		}

		// Update last applied configs to empty map on success (protected by reconcileMutex)
		r.lastAppliedConfigs = make(map[string]ptpv2alpha1.HardwareConfig)
	}

	return ctrl.Result{}, nil
}

// configDiff represents the differences between old and new hardware configurations.
// It categorizes changes into added, removed, and modified configs.
type configDiff struct {
	Added    []ptpv2alpha1.HardwareConfig // Configs present in new but not in old
	Removed  []ptpv2alpha1.HardwareConfig // Configs present in old but not in new
	Modified []ptpv2alpha1.HardwareConfig // Configs present in both but with different specs (new version)
}

// HasChanges returns true if there are any differences between old and new configs
func (d *configDiff) HasChanges() bool {
	return len(d.Added) > 0 || len(d.Removed) > 0 || len(d.Modified) > 0
}

// AllChanged returns all changed configs (added, removed, and modified combined)
func (d *configDiff) AllChanged() []ptpv2alpha1.HardwareConfig {
	result := make([]ptpv2alpha1.HardwareConfig, 0, len(d.Added)+len(d.Removed)+len(d.Modified))
	result = append(result, d.Added...)
	result = append(result, d.Removed...)
	result = append(result, d.Modified...)
	return result
}

// diffHardwareConfigs computes the differences between old configs (as a map) and new configs (as a slice).
// This is the central function for detecting what changed between two sets of hardware configurations.
// Using a map for old configs and slice for new configs matches our internal storage pattern.
func diffHardwareConfigs(oldMap map[string]ptpv2alpha1.HardwareConfig, newConfigs []ptpv2alpha1.HardwareConfig) configDiff {
	diff := configDiff{
		Added:    make([]ptpv2alpha1.HardwareConfig, 0),
		Removed:  make([]ptpv2alpha1.HardwareConfig, 0),
		Modified: make([]ptpv2alpha1.HardwareConfig, 0),
	}

	// Build a map from new configs for efficient lookup
	newMap := make(map[string]ptpv2alpha1.HardwareConfig, len(newConfigs))
	for _, cfg := range newConfigs {
		newMap[cfg.Name] = cfg
	}

	// Check for added or modified configs
	for _, newCfg := range newConfigs {
		oldCfg, exists := oldMap[newCfg.Name]
		if !exists {
			// Config was added
			diff.Added = append(diff.Added, newCfg)
		} else if !reflect.DeepEqual(oldCfg.Spec, newCfg.Spec) {
			// Config was modified (store the new version)
			diff.Modified = append(diff.Modified, newCfg)
		}
	}

	// Check for removed configs
	for name, oldCfg := range oldMap {
		if _, exists := newMap[name]; !exists {
			// Config was removed
			diff.Removed = append(diff.Removed, oldCfg)
		}
	}

	return diff
}

// describeConfigChanges provides a description (stringof what changed based on a pre-computed configDiff.
func describeConfigChanges(diff configDiff) string {
	if !diff.HasChanges() {
		return "no changes"
	}

	var changes []string

	for _, cfg := range diff.Added {
		changes = append(changes, fmt.Sprintf("added: %s/%s (relatedPtpProfile: %s)", cfg.Namespace, cfg.Name, cfg.Spec.RelatedPtpProfileName))
	}

	for _, cfg := range diff.Removed {
		changes = append(changes, fmt.Sprintf("removed: %s/%s (relatedPtpProfile: %s)", cfg.Namespace, cfg.Name, cfg.Spec.RelatedPtpProfileName))
	}

	for _, cfg := range diff.Modified {
		changes = append(changes, fmt.Sprintf("modified: %s/%s (spec changed)", cfg.Namespace, cfg.Name))
	}

	return strings.Join(changes, "; ")
}

// sliceToHardwareConfigMap converts a slice of HardwareConfigs to a map keyed by name.
// Does NOT deep copy - use deepCopyHardwareConfigMap if you need independent copies for storage.
func sliceToHardwareConfigMap(configs []ptpv2alpha1.HardwareConfig) map[string]ptpv2alpha1.HardwareConfig {
	result := make(map[string]ptpv2alpha1.HardwareConfig, len(configs))
	for _, cfg := range configs {
		result[cfg.Name] = cfg
	}
	return result
}

// deepCopyHardwareConfigMap creates a deep copy of a hardware config map.
// Use this when storing state to ensure independent copies without shared references.
func deepCopyHardwareConfigMap(configs map[string]ptpv2alpha1.HardwareConfig) map[string]ptpv2alpha1.HardwareConfig {
	if configs == nil {
		return nil
	}
	result := make(map[string]ptpv2alpha1.HardwareConfig, len(configs))
	for _, v := range configs {
		result[v.Name] = *v.DeepCopy()
	}
	return result
}

// recordUpdateFailure records an event and updates status for failed hardware config updates
func (r *HardwareConfigReconciler) recordUpdateFailure(ctx context.Context, configs []ptpv2alpha1.HardwareConfig, err error) {
	if r.Recorder == nil {
		glog.Warningf("Event recorder not available, cannot record failure event")
		return
	}

	// Record events for each affected HardwareConfig
	for _, cfg := range configs {
		// Get the latest version of the config to record event
		latestCfg := &ptpv2alpha1.HardwareConfig{}
		if getErr := r.Get(ctx, client.ObjectKey{Name: cfg.Name, Namespace: cfg.Namespace}, latestCfg); getErr != nil {
			glog.Warningf("Failed to get HardwareConfig %s/%s for event recording: %v", cfg.Namespace, cfg.Name, getErr)
			continue
		}

		r.Recorder.Eventf(
			latestCfg,
			corev1.EventTypeWarning,
			"HardwareConfigUpdateFailed",
			"Failed to update daemon hardware configuration on node %s: %v",
			r.NodeName,
			err,
		)
	}

	// TODO: Update HardwareConfig status with error condition
	// This requires adding Conditions field to HardwareConfigStatus CRD
	// For now, we rely on events for error reporting
}

// calculateNodeHardwareConfigs determines which hardware configurations should be applied to this node
// Only includes hardware configs whose related PTP profile is applicable to this node
func (r *HardwareConfigReconciler) calculateNodeHardwareConfigs(_ context.Context, hwConfigs []ptpv2alpha1.HardwareConfig) []ptpv2alpha1.HardwareConfig {
	var applicableConfigs []ptpv2alpha1.HardwareConfig

	// Get currently active PTP profiles for this node
	// ConfigUpdate is always set in production (required field)
	activeProfiles := r.ConfigUpdate.GetCurrentPTPProfiles()
	var activePTPProfiles map[string]bool

	if len(activeProfiles) > 0 {
		activePTPProfiles = make(map[string]bool, len(activeProfiles))
		for _, profile := range activeProfiles {
			activePTPProfiles[profile] = true
		}
		glog.Infof("Filtering hardware configs by active PTP profiles: %v", activeProfiles)
	} else {
		// No active profiles - exclude all hardware configs
		activePTPProfiles = make(map[string]bool) // Empty map to trigger filtering
		glog.Infof("No active PTP profiles found - will exclude all hardware configs")
	}

	// Filter hardware configs: only include those whose related PTP profile is active on this node
	for _, hwConfig := range hwConfigs {
		relatedProfile := hwConfig.Spec.RelatedPtpProfileName

		// Skip configs without a related profile (they're not associated with any PTP profile)
		if relatedProfile == "" {
			glog.V(3).Infof("Skipping HardwareConfig '%s' - no relatedPtpProfileName specified", hwConfig.Name)
			continue
		}

		// Only include configs whose related profile is active
		if !activePTPProfiles[relatedProfile] {
			glog.V(3).Infof("Skipping HardwareConfig '%s' - related PTP profile '%s' is not active on this node", hwConfig.Name, relatedProfile)
			continue
		}

		// Config is applicable - add it
		applicableConfigs = append(applicableConfigs, hwConfig)
		profileName := ""
		if hwConfig.Spec.Profile.Name != nil {
			profileName = *hwConfig.Spec.Profile.Name
		}
		glog.Infof("Added hardware config name=%s profileName=%s relatedPtpProfile=%s",
			hwConfig.Name, profileName, relatedProfile)
	}

	glog.Infof("Calculated hardware configurations for node=%s totalConfigs=%d (filtered from %d)",
		r.NodeName, len(applicableConfigs), len(hwConfigs))
	return applicableConfigs
}

// checkIfChangedConfigsAffectActiveProfiles checks if the changed hardware configs
// are associated with active PTP profiles. Takes a pre-computed configDiff to avoid
// redundant diff computation.
func (r *HardwareConfigReconciler) checkIfChangedConfigsAffectActiveProfiles(diff configDiff) bool {
	// Get currently active PTP profiles from the daemon
	// ConfigUpdate is always set in production (required field)
	activePTPProfiles := r.ConfigUpdate.GetCurrentPTPProfiles()

	// If no active profiles, don't restart
	if len(activePTPProfiles) == 0 {
		glog.V(2).Infof("No active PTP profiles, skipping hardware config restart")
		return false
	}

	if !diff.HasChanges() {
		return false
	}

	// Check if any changed config is associated with an active PTP profile
	for _, changedCfg := range diff.AllChanged() {
		if changedCfg.Spec.RelatedPtpProfileName != "" {
			for _, activeProfile := range activePTPProfiles {
				if changedCfg.Spec.RelatedPtpProfileName == activeProfile {
					glog.Infof("Changed hardware config '%s' is associated with active PTP profile '%s', will trigger restart",
						changedCfg.Name, activeProfile)
					return true
				}
			}
		}
	}

	glog.V(2).Infof("Changed hardware configs are not associated with active PTP profiles %v", activePTPProfiles)
	return false
}

// SetupWithManager sets up the controller with the Manager
// If enablePtpConfigController is false, file watching will be enabled for hybrid mode
func (r *HardwareConfigReconciler) SetupWithManager(mgr ctrl.Manager, enablePtpConfigController bool, profileDir string) error {
	// Initialize event recorder if not set
	if r.Recorder == nil {
		r.Recorder = mgr.GetEventRecorderFor("hardwareconfig-controller")
	}

	// Initialize lastAppliedConfigs from current system state to avoid unnecessary restarts at startup.
	// This queries the daemon/HardwareConfigManager to get what's currently applied.
	// Note: No mutex needed here - SetupWithManager runs once at startup before the controller
	// starts processing reconcile requests, so there's no concurrent access to lastAppliedConfigs.
	if r.HardwareConfigHandler != nil {
		currentConfigs := r.HardwareConfigHandler.GetCurrentHardwareConfigs()
		r.lastAppliedConfigs = deepCopyHardwareConfigMap(sliceToHardwareConfigMap(currentConfigs))
		glog.Infof("%d hardware configs are applied on node %s", len(currentConfigs), r.NodeName)
	}

	builder := ctrl.NewControllerManagedBy(mgr).
		For(&ptpv2alpha1.HardwareConfig{})

	// Watch PtpConfig resources only if PtpConfig controller is enabled
	// Otherwise, we'll watch the file system for file-based PtpConfig changes
	if enablePtpConfigController {
		builder = builder.Watches(
			&ptpv1.PtpConfig{},
			handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, obj client.Object) []reconcile.Request {
				// When PtpConfig changes, reconcile all HardwareConfigs
				// We need to recalculate which hardware configs are applicable
				// since active profiles may have changed
				hwConfigList := &ptpv2alpha1.HardwareConfigList{}
				if err := mgr.GetClient().List(ctx, hwConfigList); err != nil {
					glog.Errorf("Failed to list HardwareConfigs when PtpConfig changed: %v", err)
					return []reconcile.Request{}
				}

				requests := make([]reconcile.Request, 0, len(hwConfigList.Items))
				for _, hwConfig := range hwConfigList.Items {
					requests = append(requests, reconcile.Request{
						NamespacedName: client.ObjectKey{
							Name:      hwConfig.Name,
							Namespace: hwConfig.Namespace,
						},
					})
				}

				// If no HardwareConfigs exist, still trigger reconciliation
				// by sending an empty request (which will be handled by checking namespace)
				if len(requests) == 0 {
					requests = append(requests, reconcile.Request{
						NamespacedName: client.ObjectKey{
							Name:      "", // Empty name triggers reconcileAllConfigs
							Namespace: "", // Empty namespace indicates PtpConfig change
						},
					})
				}

				glog.Infof("PtpConfig '%s' changed, triggering reconciliation of %d HardwareConfig(s)", obj.GetName(), len(requests))
				return requests
			}),
		)
	} else {
		// Enable file watching for hybrid mode (file-based PtpConfig)
		r.profileDir = profileDir
		if err := r.startFileWatcher(); err != nil {
			glog.Errorf("Failed to start file watcher for PtpConfig files: %v", err)
			// Continue without file watching - reconciliation will happen on ticker
		}
	}

	return builder.
		WithOptions(controller.Options{
			// Use a custom reconciler name for logging
			RecoverPanic: func() *bool { v := true; return &v }(),
		}).
		Complete(r)
}

// startFileWatcher starts watching the profile file for changes
// When the file changes, it triggers reconciliation of all HardwareConfigs
func (r *HardwareConfigReconciler) startFileWatcher() error {
	r.fileWatchMutex.Lock()
	defer r.fileWatchMutex.Unlock()

	if r.profileDir == "" {
		return fmt.Errorf("profileDir is not set, cannot start file watcher")
	}

	// Create fsnotify watcher
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return fmt.Errorf("failed to create fsnotify watcher: %w", err)
	}

	// Watch the profile directory
	if err = watcher.Add(r.profileDir); err != nil {
		watcher.Close()
		return fmt.Errorf("failed to watch profile directory %s: %w", r.profileDir, err)
	}

	r.fileWatcher = watcher
	r.fileWatchStopCh = make(chan struct{})
	profileFilePath := filepath.Join(r.profileDir, r.NodeName)

	glog.Infof("Started file watcher for PtpConfig files in directory: %s", r.profileDir)

	// Start goroutine to handle file events FIRST
	// This ensures the watcher is active before we check for existing files
	go r.handleFileEvents()

	// Check if the profile file already exists and load it synchronously
	// This ensures profiles are loaded BEFORE mgr.Start() triggers reconciliation
	// fsnotify only watches for changes, not initial state, so we need to handle existing files
	// NOTE: We do NOT trigger reconciliation here because Kubernetes watch will trigger it
	// when the controller starts. We only need to ensure profiles are loaded.
	if _, err = os.Stat(profileFilePath); err == nil {
		// File exists - read and update profiles synchronously before controller starts reconciling
		glog.Infof("Profile file already exists at startup: %s, loading profiles synchronously", profileFilePath)
		if readErr := r.loadProfileFileSynchronously(profileFilePath); readErr != nil {
			glog.Warningf("Failed to load profile file at startup: %v (will retry on file change)", readErr)
		} else {
			glog.Info("Successfully loaded initial profiles from file (Kubernetes watch will trigger reconciliation)")
		}
	} else if !os.IsNotExist(err) {
		// Some other error (permissions, etc.)
		glog.Warningf("Failed to check if profile file exists: %v", err)
	}

	return nil
}

// handleFileEvents processes file system events and triggers reconciliation
func (r *HardwareConfigReconciler) handleFileEvents() {
	defer func() {
		r.fileWatchMutex.Lock()
		if r.fileWatcher != nil {
			r.fileWatcher.Close()
			r.fileWatcher = nil
		}
		r.fileWatchMutex.Unlock()
	}()

	profileFilePath := filepath.Join(r.profileDir, r.NodeName)

	for {
		select {
		case event, ok := <-r.fileWatcher.Events:
			if !ok {
				glog.Error("File watcher events channel closed")
				return
			}

			// Only process events for the node's profile file
			if !strings.HasSuffix(event.Name, r.NodeName) {
				continue
			}

			// Process write/create events (file updates)
			if event.Op&(fsnotify.Write|fsnotify.Create) != 0 {
				// Verify it's the exact file we care about
				if event.Name == profileFilePath {
					glog.Infof("PtpConfig profile file changed: %s (op: %s), loading profiles and triggering HardwareConfig reconciliation", event.Name, event.Op.String())
					// Load the file first to update profiles before triggering reconciliation
					if err := r.loadProfileFileSynchronously(profileFilePath); err != nil {
						glog.Errorf("Failed to load profile file after change: %v", err)
						// Still trigger reconciliation - it will use old profiles, but ticker will eventually reload
					}
					r.triggerReconciliationForFileChange(profileFilePath, event.Op.String())
				}
			}

		case err, ok := <-r.fileWatcher.Errors:
			if !ok {
				glog.Error("File watcher errors channel closed")
				return
			}
			glog.Errorf("File watcher error: %v", err)

		case <-r.fileWatchStopCh:
			glog.Info("File watcher stopped")
			return
		}
	}
}

// loadProfileFileSynchronously reads the profile file and updates ConfigUpdate synchronously.
// This is used at startup to ensure profiles are loaded before mgr.Start() triggers reconciliation.
func (r *HardwareConfigReconciler) loadProfileFileSynchronously(profileFilePath string) error {
	// Read the profile file
	nodeProfilesJSON, err := os.ReadFile(profileFilePath)
	if err != nil {
		return fmt.Errorf("failed to read profile file %s: %w", profileFilePath, err)
	}

	// Use type assertion to access UpdateConfig method if ConfigUpdate is *daemon.LinuxPTPConfUpdate
	// This allows us to update profiles synchronously before the controller starts reconciling
	if ptpConfUpdate, ok := r.ConfigUpdate.(*daemon.LinuxPTPConfUpdate); ok {
		// Pass false for ptpAuthUpdated - we only care about loading profiles here,
		// not about PTP authentication files. Auth file changes are handled separately
		// by the daemon's own file watcher.
		if err = ptpConfUpdate.UpdateConfig(nodeProfilesJSON, false); err != nil {
			return fmt.Errorf("failed to update config from profile file: %w", err)
		}
		glog.Infof("Successfully loaded profiles from file: %s", profileFilePath)
		return nil
	}

	// If ConfigUpdate is not *daemon.LinuxPTPConfUpdate (e.g., in tests), log a warning
	// but don't fail - the file watcher will handle updates via the ticker in main.go
	glog.Warningf("ConfigUpdate is not *daemon.LinuxPTPConfUpdate, cannot update profiles synchronously (type: %T)", r.ConfigUpdate)
	return nil
}

// profileChangeDebounceInterval is the debounce interval for profile change triggers.
// Multiple profile changes within this interval will be coalesced into a single reconciliation.
const profileChangeDebounceInterval = 500 * time.Millisecond

// TriggerReconciliationForProfileChange triggers reconciliation of all HardwareConfigs
// when PTP profiles change (e.g., via ticker reading the file). This is a public method
// that can be called from external code (like the ticker in main.go).
// Uses debouncing to prevent unbounded goroutines when called frequently.
func (r *HardwareConfigReconciler) TriggerReconciliationForProfileChange() {
	r.triggerReconciliationForProfileChangeInternal("profile change via ticker")
}

// triggerReconciliationForFileChange triggers reconciliation of all HardwareConfigs
// when the profile file changes. This is called from file watcher events when
// the file is created or modified (not at startup, as Kubernetes watch handles initial reconciliation).
// Uses debouncing to prevent unbounded goroutines when called frequently.
func (r *HardwareConfigReconciler) triggerReconciliationForFileChange(profileFilePath, reason string) {
	r.triggerReconciliationForProfileChangeInternal(fmt.Sprintf("file change: %s (%s)", profileFilePath, reason))
}

// triggerReconciliationForProfileChangeInternal is the internal implementation that triggers reconciliation.
// It uses a debouncing mechanism to coalesce multiple rapid profile changes into a single reconciliation,
// preventing unbounded goroutine creation and potential DoS.
func (r *HardwareConfigReconciler) triggerReconciliationForProfileChangeInternal(reason string) {
	r.profileChangeMutex.Lock()
	defer r.profileChangeMutex.Unlock()

	// If a timer is already running, reset it (debounce)
	if r.profileChangeTimer != nil {
		r.profileChangeTimer.Stop()
	}

	glog.V(2).Infof("Scheduling HardwareConfig reconciliation due to %s (debounce: %v)", reason, profileChangeDebounceInterval)

	// Create a new timer that will fire after the debounce interval
	r.profileChangeTimer = time.AfterFunc(profileChangeDebounceInterval, func() {
		r.executeProfileChangeReconciliation(reason)
	})
}

// executeProfileChangeReconciliation performs the actual reconciliation after debouncing.
// This is called by the debounce timer and runs synchronously (not in a new goroutine).
func (r *HardwareConfigReconciler) executeProfileChangeReconciliation(reason string) {
	ctx := context.Background()

	// Trigger a single global reconciliation with empty NamespacedName.
	// The Reconcile function handles this by calling reconcileAllConfigs,
	// which lists and processes ALL HardwareConfigs in a single pass.
	// This is more efficient than triggering separate reconciliations for each HardwareConfig.
	_, err := r.Reconcile(ctx, reconcile.Request{
		NamespacedName: client.ObjectKey{
			Name:      "",
			Namespace: "",
		},
	})
	if err != nil {
		glog.Errorf("Failed to reconcile HardwareConfigs after profile change (%s): %v", reason, err)
		return
	}

	glog.Infof("Completed HardwareConfig reconciliation due to %s", reason)
}
