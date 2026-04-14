package network

import (
	"sync"

	"github.com/golang/glog"
	"github.com/jaypipes/ghw"
	"github.com/jaypipes/ghw/pkg/util"
	ptpv1 "github.com/k8snetworkplumbingwg/ptp-operator/api/v1"
)

var (
	cachedSystemInfo    *ptpv1.SystemInfo
	cachedBaseBoardInfo *ptpv1.BaseBoardInfo
	dmiOnce             sync.Once
)

// cleanDMIValue returns an empty string for ghw's "unknown" sentinel value
func cleanDMIValue(val string) string {
	if val == util.UNKNOWN {
		return ""
	}
	return val
}

// loadDMIInfo collects DMI/SMBIOS information once and caches the results
func loadDMIInfo() {
	dmiOnce.Do(func() {
		productInfo, err := ghw.Product(ghw.WithNullAlerter())
		if err != nil {
			glog.Warningf("Failed to get system product info from DMI: %v", err)
		} else {
			cachedSystemInfo = &ptpv1.SystemInfo{
				Manufacturer: cleanDMIValue(productInfo.Vendor),
				ProductName:  cleanDMIValue(productInfo.Name),
				Version:      cleanDMIValue(productInfo.Version),
				SerialNumber: cleanDMIValue(productInfo.SerialNumber),
				SKUNumber:    cleanDMIValue(productInfo.SKU),
				Family:       cleanDMIValue(productInfo.Family),
			}
		}

		boardInfo, err := ghw.Baseboard(ghw.WithNullAlerter())
		if err != nil {
			glog.Warningf("Failed to get baseboard info from DMI: %v", err)
		} else {
			cachedBaseBoardInfo = &ptpv1.BaseBoardInfo{
				Manufacturer: cleanDMIValue(boardInfo.Vendor),
				ProductName:  cleanDMIValue(boardInfo.Product),
				Version:      cleanDMIValue(boardInfo.Version),
				SerialNumber: cleanDMIValue(boardInfo.SerialNumber),
			}
		}
	})
}

// GetSystemInfo returns cached system-level DMI/SMBIOS information (Type 1)
func GetSystemInfo() *ptpv1.SystemInfo {
	loadDMIInfo()
	return cachedSystemInfo
}

// GetBaseBoardInfo returns cached base board DMI/SMBIOS information (Type 2)
func GetBaseBoardInfo() *ptpv1.BaseBoardInfo {
	loadDMIInfo()
	return cachedBaseBoardInfo
}

// LogDMIInfo logs system and baseboard information
func LogDMIInfo(sysInfo *ptpv1.SystemInfo, bbInfo *ptpv1.BaseBoardInfo) {
	if sysInfo != nil {
		glog.Infof("System Information:")
		if sysInfo.Manufacturer != "" {
			glog.Infof("  Manufacturer:   %s", sysInfo.Manufacturer)
		}
		if sysInfo.ProductName != "" {
			glog.Infof("  Product Name:   %s", sysInfo.ProductName)
		}
		if sysInfo.Version != "" {
			glog.Infof("  Version:        %s", sysInfo.Version)
		}
		if sysInfo.SerialNumber != "" {
			glog.Infof("  Serial Number:  %s", sysInfo.SerialNumber)
		}
		if sysInfo.SKUNumber != "" {
			glog.Infof("  SKU Number:     %s", sysInfo.SKUNumber)
		}
		if sysInfo.Family != "" {
			glog.Infof("  Family:         %s", sysInfo.Family)
		}
	}

	if bbInfo != nil {
		glog.Infof("Base Board Information:")
		if bbInfo.Manufacturer != "" {
			glog.Infof("  Manufacturer:   %s", bbInfo.Manufacturer)
		}
		if bbInfo.ProductName != "" {
			glog.Infof("  Product Name:   %s", bbInfo.ProductName)
		}
		if bbInfo.Version != "" {
			glog.Infof("  Version:        %s", bbInfo.Version)
		}
		if bbInfo.SerialNumber != "" {
			glog.Infof("  Serial Number:  %s", bbInfo.SerialNumber)
		}
	}
}
