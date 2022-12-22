package daemon

import "github.com/golang/glog"

func (dn *Daemon) isHighAvailability() bool {
	for i, profile := range dn.ptpUpdate.NodeProfiles {
		glog.Infof("Validating profile %d", i)
		if profile.Ptp4lOpts != nil {
			glog.Infof("Ptp4lOpts-%d: %s", i, *profile.Ptp4lOpts)
		} else {
			glog.Infof("No Ptp4lOpts in profile %d", i)
		}
		if profile.Phc2sysOpts != nil {
			glog.Infof("Phc2sysOpts-%d: %s", i, *profile.Phc2sysOpts)
		} else {
			glog.Infof("No Phc2sysOpts in profile %d", i)
		}

	}
	return true
}
