package clockmgr

// OSClockSampleForTest exposes only the private OS-clock sample metadata needed
// by external-package tests. State assertions use the public GetStats API.
func OSClockSampleForTest(m *ClockManager) (iface string, offset int64) {
	m.clockManagementMu.Lock()
	defer m.clockManagementMu.Unlock()
	return m.osClock.IFace, m.osClock.Offset
}
