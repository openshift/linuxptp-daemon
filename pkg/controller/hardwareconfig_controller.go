// Package controller provides Kubernetes controller functionality for PTP configuration.
package controller

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/golang/glog"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"

	ptpv2alpha1 "github.com/k8snetworkplumbingwg/ptp-operator/api/v2alpha1"
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
	// affect currently active PTP profiles
	ConfigUpdate HardwareConfigRestartTrigger

	// lastRestartTime tracks when the last restart was scheduled to prevent multiple restarts
	lastRestartTime time.Time

	// activeReconciliations tracks ongoing reconciliations to ensure restart waits for completion
	activeReconciliations sync.WaitGroup

	// reconciliationMutex protects access to activeReconciliations and lastRestartTime
	reconciliationMutex sync.Mutex
}

// HardwareConfigRestartTrigger interface for triggering PTP restarts
type HardwareConfigRestartTrigger interface {
	TriggerRestartForHardwareChange() error
	GetCurrentPTPProfiles() []string
}

// +kubebuilder:rbac:groups=ptp.openshift.io,resources=hardwareconfigs,verbs=get;list;watch
// +kubebuilder:rbac:groups=ptp.openshift.io,resources=hardwareconfigs/status,verbs=get;update;patch

// Reconcile handles HardwareConfig changes and updates the daemon hardware configuration
func (r *HardwareConfigReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	glog.Infof("Reconciling HardwareConfig name=%s namespace=%s", req.Name, req.Namespace)

	// Track this reconciliation as active
	r.reconciliationMutex.Lock()
	r.activeReconciliations.Add(1)
	r.reconciliationMutex.Unlock()

	// Ensure we signal completion when done
	defer func() {
		r.reconciliationMutex.Lock()
		r.activeReconciliations.Done()
		r.reconciliationMutex.Unlock()
	}()

	// Get the HardwareConfig resource
	hwConfig := &ptpv2alpha1.HardwareConfig{}
	err := r.Get(ctx, req.NamespacedName, hwConfig)
	if err != nil {
		if errors.IsNotFound(err) {
			// HardwareConfig was deleted, trigger recalculation of hardware configurations
			glog.Infof("HardwareConfig deleted, recalculating hardware configurations name=%s", req.Name)
			return r.reconcileAllConfigs(ctx)
		}
		glog.Errorf("Failed to get HardwareConfig: %v", err)
		return ctrl.Result{}, err
	}

	// Recalculate and apply hardware configuration for this node
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
//
//nolint:unparam // result 0 (ctrl.Result) is always empty, but required to match Reconcile signature
func (r *HardwareConfigReconciler) reconcileAllConfigs(ctx context.Context) (ctrl.Result, error) {
	// List all HardwareConfigs in the cluster
	hwConfigList := &ptpv2alpha1.HardwareConfigList{}
	if err := r.List(ctx, hwConfigList); err != nil {
		glog.Errorf("Failed to list HardwareConfigs: %v", err)
		return ctrl.Result{}, err
	}

	// Validate hardware config separation before proceeding
	if err := r.validateHardwareConfigSeparation(hwConfigList.Items); err != nil {
		glog.Errorf("Hardware config validation failed: %v", err)
		// Don't return error to avoid controller crash, but log the issue
		// In production, you might want to set a status condition instead
	}

	// Check if any hardware configs are associated with currently active PTP profiles
	// If so, trigger a PTP restart to ensure hardware and PTP configurations are synchronized
	needsPTPRestart := r.checkIfActiveProfilesAffected(ctx, hwConfigList.Items)

	// Calculate the applicable hardware configurations for this node
	applicableConfigs, err := r.calculateNodeHardwareConfigs(ctx, hwConfigList.Items)
	if err != nil {
		glog.Errorf("Failed to calculate node hardware configurations: %v", err)
		return ctrl.Result{}, err
	}

	// Apply hardware configurations via the handler (the daemon instance in production)
	if len(applicableConfigs) > 0 {
		glog.Infof("Updating daemon hardware configuration with %d hardware configs for node %s", len(applicableConfigs), r.NodeName)

		// Send hardware configuration update to daemon (HardwareConfigHandler)
		if r.HardwareConfigHandler != nil {
			err = r.HardwareConfigHandler.UpdateHardwareConfig(applicableConfigs)
			if err != nil {
				glog.Errorf("Failed to update daemon hardware configuration: %v", err)
				return ctrl.Result{}, err
			}
		}

		glog.Infof("Successfully updated daemon hardware configuration configs=%d", len(applicableConfigs))
	} else {
		glog.Infof("No applicable hardware configurations found for node %s", r.NodeName)

		// Clear hardware configurations if needed
		if r.HardwareConfigHandler != nil {
			err = r.HardwareConfigHandler.UpdateHardwareConfig([]ptpv2alpha1.HardwareConfig{})
			if err != nil {
				glog.Errorf("Failed to clear daemon hardware configuration: %v", err)
				return ctrl.Result{}, err
			}
		}
	}

	if needsPTPRestart {
		glog.Infof("HardwareConfig change affects active PTP profiles on node %s, scheduling deferred restart", r.NodeName)
		r.scheduleDeferredRestart(ctx)
	} else {
		glog.Infof("HardwareConfig change does not require PTP restart (no active profiles affected)")
	}
	return ctrl.Result{}, nil
}

// scheduleDeferredRestart schedules a restart to happen after all active reconciliations complete
// This allows all HardwareConfig reconciliations to finish before triggering the restart
// It also prevents multiple restarts within a short time window
func (r *HardwareConfigReconciler) scheduleDeferredRestart(_ context.Context) {
	// Check if a restart was recently scheduled (within last 2 seconds)
	r.reconciliationMutex.Lock()
	now := time.Now()
	timeSinceLastRestart := now.Sub(r.lastRestartTime)
	if timeSinceLastRestart < 2*time.Second {
		r.reconciliationMutex.Unlock()
		glog.Infof("Restart already scheduled recently (%v ago), skipping duplicate restart request", timeSinceLastRestart)
		return
	}

	glog.Infof("Scheduling deferred restart for hardware configuration change")
	// Update the last restart time
	r.lastRestartTime = now
	// Get a reference to the WaitGroup before unlocking
	activeReconciliations := &r.activeReconciliations
	r.reconciliationMutex.Unlock()

	// Use a goroutine to wait for all active reconciliations to complete
	go func() {
		// Wait for all active reconciliations to complete
		activeReconciliations.Wait()

		if r.ConfigUpdate != nil {
			err := r.ConfigUpdate.TriggerRestartForHardwareChange()
			if err != nil {
				glog.Errorf("Failed to trigger deferred PTP restart for hardware configuration change: %v", err)
			} else {
				glog.Infof("Successfully triggered deferred PTP restart due to hardware configuration change")
			}
		}
	}()
}

// calculateNodeHardwareConfigs determines which hardware configurations should be applied to this node
//
//nolint:unparam // error return is kept for future node matching logic
func (r *HardwareConfigReconciler) calculateNodeHardwareConfigs(_ context.Context, hwConfigs []ptpv2alpha1.HardwareConfig) ([]ptpv2alpha1.HardwareConfig, error) {
	var applicableConfigs []ptpv2alpha1.HardwareConfig

	// For now, we apply all hardware configurations to all nodes
	// This can be enhanced later with node matching logic similar to PtpConfig
	for _, hwConfig := range hwConfigs {
		glog.Infof("Processing HardwareConfig name=%s profile=%s", hwConfig.Name, getProfileName(hwConfig.Spec.Profile))

		// TODO: Add node-specific filtering logic here
		// For now, we include all hardware configs
		applicableConfigs = append(applicableConfigs, hwConfig)
		glog.Infof("Added hardware config profileName=%s relatedPtpProfile=%s", getProfileName(hwConfig.Spec.Profile), hwConfig.Spec.RelatedPtpProfileName)
	}

	glog.Infof("Calculated hardware configurations for node node=%s totalConfigs=%d", r.NodeName, len(applicableConfigs))
	return applicableConfigs, nil
}

// checkIfActiveProfilesAffected determines if hardware config changes should trigger PTP restart
// We restart when hardware configs associated with active PTP profiles change
func (r *HardwareConfigReconciler) checkIfActiveProfilesAffected(_ context.Context, hwConfigs []ptpv2alpha1.HardwareConfig) bool {
	// Get currently active PTP profiles from the daemon
	if r.ConfigUpdate == nil {
		glog.Infof("No ConfigUpdate interface available, cannot check active PTP profiles")
		return false
	}

	activePTPProfiles := r.ConfigUpdate.GetCurrentPTPProfiles()
	glog.Infof("Checking hardware config associations activeProfiles=%v hardwareConfigs=%d", activePTPProfiles, len(hwConfigs))

	// If no hardware configs exist, don't restart
	if len(hwConfigs) == 0 {
		glog.Infof("No hardware configs present, skipping restart")
		return false
	}

	// If no active profiles, don't restart
	if len(activePTPProfiles) == 0 {
		glog.Infof("No active PTP profiles, skipping hardware config restart")
		return false
	}

	// Check if any hardware config is associated with an active PTP profile
	for _, hwConfig := range hwConfigs {
		if hwConfig.Spec.RelatedPtpProfileName != "" {
			for _, activeProfile := range activePTPProfiles {
				if hwConfig.Spec.RelatedPtpProfileName == activeProfile {
					glog.Infof("Found hardware config '%s' associated with active PTP profile '%s', will trigger restart",
						hwConfig.Spec.Profile.Name, activeProfile)
					return true
				}
			}
		}
	}

	glog.Infof("No hardware configs are associated with currently active PTP profiles")
	return false
}

// getProfileName safely extracts the profile name from a HardwareProfile
func getProfileName(profile ptpv2alpha1.HardwareProfile) string {
	if profile.Name != nil {
		return *profile.Name
	}
	return "unnamed"
}

// SetupWithManager sets up the controller with the Manager
func (r *HardwareConfigReconciler) SetupWithManager(mgr ctrl.Manager) error {
	// Watch HardwareConfig resources
	return ctrl.NewControllerManagedBy(mgr).
		For(&ptpv2alpha1.HardwareConfig{}).
		WithOptions(controller.Options{
			// Use a custom reconciler name for logging
			RecoverPanic: func() *bool { v := true; return &v }(),
		}).
		Complete(r)
}
