package utils

import (
	"runtime/debug"

	"github.com/golang/glog"
)

// CheckMetricSanity validates that process and iface labels are populated
func CheckMetricSanity(metricName, process, iface string) bool {
	if process == "" || iface == "" {
		glog.Warningf("Sanity check failed for metric '%s': process='%s', iface='%s'. Stack trace:\n%s", metricName, process, iface, string(debug.Stack()))
		return false
	}
	return true
}
