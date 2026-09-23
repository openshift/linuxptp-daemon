package daemon

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/golang/glog"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	ptpv1 "github.com/k8snetworkplumbingwg/ptp-operator/api/v1"
	ptpclient "github.com/k8snetworkplumbingwg/ptp-operator/pkg/client/clientset/versioned"

	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/event"
	ptpnetwork "github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/network"
)

const (
	conditionReady       = "Ready"
	conditionSynced      = "Synced"
	reasonNoProfile      = "NoProfile"
	reasonProfileApplied = "ProfileApplied"
	reasonProcessesDown  = "ProcessesDown"
)

func populateNodePTPDevices(nodePTPDev *ptpv1.NodePtpDevice, hwconfigs *[]ptpv1.HwConfig) (*ptpv1.NodePtpDevice, error) {
	nodePTPDev.Status.Hwconfig = []ptpv1.HwConfig{}
	for _, hw := range *hwconfigs {
		nodePTPDev.Status.Hwconfig = append(nodePTPDev.Status.Hwconfig, hw)
	}
	return nodePTPDev, nil
}

func GetDevStatusUpdate(nodePTPDev *ptpv1.NodePtpDevice) (*ptpv1.NodePtpDevice, error) {
	hostDevs, err := ptpnetwork.DiscoverPTPDevices()
	if err != nil {
		return nodePTPDev, fmt.Errorf("discover PTP devices failed: %v", err)
	}
	glog.Infof("PTP capable NICs: %v", hostDevs)

	// Build new device list with hardware info
	newDevices := make([]ptpv1.PtpDevice, 0)
	for _, hostDev := range hostDevs {
		hwInfo, hwErr := ptpnetwork.GetHardwareInfo(hostDev)
		if hwErr != nil {
			glog.Warningf("Failed to get hardware info for device %s: %v", hostDev, hwErr)
			continue
		}
		newDevices = append(newDevices, ptpv1.PtpDevice{
			Name:         hostDev,
			Profile:      "",
			HardwareInfo: hwInfo,
		})
	}

	// Log hardware info deduplicated by NIC: full details only for the port that exposes a PTP device
	nicDefaultPort := make(map[string]string)
	for _, dev := range newDevices {
		if dev.HardwareInfo == nil {
			continue
		}
		nicBase := dev.HardwareInfo.PCIAddress
		if idx := strings.LastIndex(nicBase, "."); idx != -1 {
			nicBase = nicBase[:idx]
		}
		if _, exists := nicDefaultPort[nicBase]; !exists {
			nicDefaultPort[nicBase] = dev.Name
		}
		if exposesPTPDevice(dev.Name) {
			nicDefaultPort[nicBase] = dev.Name
		}
	}
	// Collect VPD once per NIC using the port that exposes the PTP device
	vpdCache := make(map[string]*ptpnetwork.VPDInfo)
	for nicBase, portName := range nicDefaultPort {
		for _, dev := range newDevices {
			if dev.Name == portName && dev.HardwareInfo != nil {
				vpd, vpdErr := ptpnetwork.GetVPDInfo(dev.HardwareInfo.PCIAddress)
				if vpdErr != nil {
					glog.V(2).Infof("VPD for NIC %s (via %s): %v", nicBase, portName, vpdErr)
				} else {
					vpdCache[nicBase] = vpd
				}
				break
			}
		}
	}
	for i := range newDevices {
		if newDevices[i].HardwareInfo == nil {
			continue
		}
		nicBase := newDevices[i].HardwareInfo.PCIAddress
		if idx := strings.LastIndex(nicBase, "."); idx != -1 {
			nicBase = nicBase[:idx]
		}
		if vpd, ok := vpdCache[nicBase]; ok {
			newDevices[i].HardwareInfo.VPDIdentifierString = vpd.IdentifierString
			newDevices[i].HardwareInfo.VPDPartNumber = vpd.PartNumber
			newDevices[i].HardwareInfo.VPDSerialNumber = vpd.SerialNumber
			newDevices[i].HardwareInfo.VPDManufacturerID = vpd.ManufacturerID
			newDevices[i].HardwareInfo.VPDProductName = vpd.ProductName
			newDevices[i].HardwareInfo.VPDVendorSpecific1 = vpd.VendorSpecific1
			newDevices[i].HardwareInfo.VPDVendorSpecific2 = vpd.VendorSpecific2
		}
	}

	for _, dev := range newDevices {
		if dev.HardwareInfo == nil {
			continue
		}
		nicBase := dev.HardwareInfo.PCIAddress
		if idx := strings.LastIndex(nicBase, "."); idx != -1 {
			nicBase = nicBase[:idx]
		}
		if dev.Name == nicDefaultPort[nicBase] {
			ptpnetwork.LogStructuredHardwareInfo(dev.Name, dev.HardwareInfo)
		} else {
			glog.V(14).Infof("PTP Device: %s (PCI: %s, same NIC as %s)", dev.Name, dev.HardwareInfo.PCIAddress, nicDefaultPort[nicBase])
		}
	}

	// Log device changes (additions and removals)
	ptpnetwork.LogDeviceChanges(nodePTPDev.Status.Devices, newDevices)

	nodePTPDev.Status.Devices = newDevices

	// Populate system and baseboard DMI/SMBIOS info only once (static per-node data).
	if nodePTPDev.Status.SystemInfo == nil || nodePTPDev.Status.BaseBoardInfo == nil {
		nodePTPDev.Status.SystemInfo = ptpnetwork.GetSystemInfo()
		nodePTPDev.Status.BaseBoardInfo = ptpnetwork.GetBaseBoardInfo()
		ptpnetwork.LogDMIInfo(nodePTPDev.Status.SystemInfo, nodePTPDev.Status.BaseBoardInfo)
	}

	return nodePTPDev, nil
}

func runDeviceStatusUpdate(ptpClient *ptpclient.Clientset, nodeName string, hwconfigs *[]ptpv1.HwConfig) {
	// Discover PTP capable devices
	// Don't return in case of discover failure
	ptpDevs, err := ptpnetwork.DiscoverPTPDevices()
	if err != nil {
		glog.Errorf("discover PTP devices failed: %v", err)
	}
	glog.Infof("PTP capable NICs: %v", ptpDevs)

	// Assume NodePtpDevice CR for this particular node
	// is already created manually or by PTP-Operator.
	ptpDev, err := ptpClient.PtpV1().NodePtpDevices(PtpNamespace).Get(context.TODO(), nodeName, metav1.GetOptions{})
	if err != nil {
		glog.Errorf("failed to get NodePtpDevice CR for node %s: %v", nodeName, err)
	}

	// Render status of NodePtpDevice CR by inspecting PTP capability of node network devices
	ptpDev, err = GetDevStatusUpdate(ptpDev)
	if err != nil {
		glog.Errorf("failed to get device status: %v", err)
	}

	//Populate hwconfig
	ptpDev, err = populateNodePTPDevices(ptpDev, hwconfigs)
	if err != nil {
		glog.Errorf("failed to populate node ptp devices: %v", err)
	}

	// Update NodePtpDevice CR
	_, err = ptpClient.PtpV1().NodePtpDevices(PtpNamespace).UpdateStatus(context.TODO(), ptpDev, metav1.UpdateOptions{})
	if err != nil {
		glog.Errorf("failed to update Node PTP device CR: %v", err)
	}
}

func RunDeviceStatusUpdate(ptpClient *ptpclient.Clientset, nodeName string, hwconfigs *[]ptpv1.HwConfig) {
	glog.Info("run device status update function")
	runDeviceStatusUpdate(ptpClient, nodeName, hwconfigs)
}

// runSyncStatusUpdate writes non-metrics PTP status into NodePtpDevice.status.sync
// and the Ready condition. Triggered after apply and on clock-state transitions.
// Writes are skipped while applyNodePTPProfiles is in progress unless force is set.
func (dn *Daemon) runSyncStatusUpdate() {
	dn.doSyncStatusUpdate(false)
}

func (dn *Daemon) isApplyingProfiles() bool {
	return dn.processManager != nil &&
		dn.processManager.clockMgr != nil &&
		dn.processManager.clockMgr.IsApplying()
}

func (dn *Daemon) doSyncStatusUpdate(force bool) {
	if dn.ptpClient == nil {
		return
	}

	dn.syncStatusMu.Lock()
	defer dn.syncStatusMu.Unlock()
	if !force && dn.isApplyingProfiles() {
		glog.V(2).Info("runSyncStatusUpdate: skip, profile apply in progress")
		return
	}

	// --- Collect per-profile info from the active process list ---
	var profiles []ptpv1.NodeProfileStatus
	profileByIface := make(map[string]string)
	anyRunning := false
	var firstProfile string
	seen := make(map[string]struct{})

	for _, proc := range dn.processManager.process {
		if proc == nil {
			continue
		}
		if !proc.Stopped() {
			anyRunning = true
		}
		if proc.nodeProfile.Name == nil {
			continue
		}
		profileName := *proc.nodeProfile.Name
		if firstProfile == "" {
			firstProfile = profileName
		}

		if _, exists := seen[profileName]; !exists {
			profiles = append(profiles, ptpv1.NodeProfileStatus{
				Name:      profileName,
				ClockType: reportedClockType(proc),
			})
			seen[profileName] = struct{}{}
		}

		for _, iface := range proc.ifaces {
			if iface.Name != "" {
				profileByIface[iface.Name] = profileName
			}
		}
	}

	// --- Ready condition ---
	readyStatus := metav1.ConditionFalse
	readyReason := reasonNoProfile
	readyMsg := "No PTP profile is applied"
	if firstProfile != "" && anyRunning {
		readyStatus = metav1.ConditionTrue
		readyReason = reasonProfileApplied
		readyMsg = fmt.Sprintf("Profile %s is applied and processes are running", firstProfile)
	} else if firstProfile != "" {
		readyReason = reasonProcessesDown
		readyMsg = "Profile is applied but no PTP process is running"
	}

	// --- Get + mutate + update with conflict retry ---
	const maxRetries = 3
	for attempt := 0; attempt < maxRetries; attempt++ {
		ptpDev, err := dn.ptpClient.PtpV1().NodePtpDevices(PtpNamespace).Get(
			context.TODO(), dn.nodeName, metav1.GetOptions{})
		if err != nil {
			glog.Errorf("runSyncStatusUpdate: failed to get NodePtpDevice CR for node %s: %v", dn.nodeName, err)
			return
		}

		now := metav1.Now()

		ptpDev.Status.Sync = &ptpv1.SyncStatus{
			Profiles: profiles,
		}

		setCondition(&ptpDev.Status.Conditions, metav1.Condition{
			Type:               conditionReady,
			Status:             readyStatus,
			LastTransitionTime: now,
			Reason:             readyReason,
			Message:            readyMsg,
		})

		removeCondition(&ptpDev.Status.Conditions, conditionSynced)

		// Backfill devices[].profile
		for i := range ptpDev.Status.Devices {
			if profile, ok := profileByIface[ptpDev.Status.Devices[i].Name]; ok {
				ptpDev.Status.Devices[i].Profile = profile
			}
		}

		_, err = dn.ptpClient.PtpV1().NodePtpDevices(PtpNamespace).UpdateStatus(
			context.TODO(), ptpDev, metav1.UpdateOptions{})
		if err == nil {
			return
		}
		if !kerrors.IsConflict(err) {
			glog.Errorf("runSyncStatusUpdate: failed to update NodePtpDevice status for node %s: %v", dn.nodeName, err)
			return
		}
		glog.V(2).Infof("runSyncStatusUpdate: conflict on attempt %d, retrying", attempt+1)
	}
	glog.Warningf("runSyncStatusUpdate: exhausted retries for node %s", dn.nodeName)
}

// reportedClockType is the single clockType written on NodePtpDevice at apply.
// Status values are T-GM, T-BC, BC, or OC. There is no plain GM; a GM role
// from PopulatePtp4lConf is reported as T-GM.
func reportedClockType(proc *ptpProcess) string {
	if proc == nil {
		return ""
	}
	if declared, ok := proc.nodeProfile.PtpSettings["clockType"]; ok && (declared == TGM || declared == TBC) {
		return declared
	}
	if proc.clockType == event.GM {
		return TGM
	}
	return string(proc.clockType)
}

// setCondition inserts or updates a condition in the slice, preserving
// LastTransitionTime when the status has not changed.
func setCondition(conditions *[]metav1.Condition, newCond metav1.Condition) {
	for i, existing := range *conditions {
		if existing.Type == newCond.Type {
			if existing.Status == newCond.Status {
				newCond.LastTransitionTime = existing.LastTransitionTime
			}
			(*conditions)[i] = newCond
			return
		}
	}
	*conditions = append(*conditions, newCond)
}

// removeCondition removes a condition by type if it exists.
func removeCondition(conditions *[]metav1.Condition, condType string) {
	for i, c := range *conditions {
		if c.Type == condType {
			*conditions = append((*conditions)[:i], (*conditions)[i+1:]...)
			return
		}
	}
}

// return true if the network device exposes a PTP clock device
func exposesPTPDevice(deviceName string) bool {
	ptpDir := fmt.Sprintf("/sys/class/net/%s/device/ptp", deviceName)
	entries, err := os.ReadDir(ptpDir)
	return err == nil && len(entries) > 0
}
