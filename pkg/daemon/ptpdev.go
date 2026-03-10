package daemon

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/golang/glog"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	ptpv1 "github.com/k8snetworkplumbingwg/ptp-operator/api/v1"
	ptpclient "github.com/k8snetworkplumbingwg/ptp-operator/pkg/client/clientset/versioned"

	ptpnetwork "github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/network"
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
			glog.Infof("PTP Device: %s (PCI: %s, same NIC as %s)", dev.Name, dev.HardwareInfo.PCIAddress, nicDefaultPort[nicBase])
		}
	}

	// Log device changes (additions and removals)
	ptpnetwork.LogDeviceChanges(nodePTPDev.Status.Devices, newDevices)

	nodePTPDev.Status.Devices = newDevices
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

// return true if the network device exposes a PTP clock device
func exposesPTPDevice(deviceName string) bool {
	ptpDir := fmt.Sprintf("/sys/class/net/%s/device/ptp", deviceName)
	entries, err := os.ReadDir(ptpDir)
	return err == nil && len(entries) > 0
}
