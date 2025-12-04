package intel

import (
	"errors"
	"fmt"

	"github.com/golang/glog"
)

type (
	pinSet map[string]string
	frqSet []string
)

type pinConfigurer interface {
	applyPinSet(device string, pins pinSet) error
	applyPinFrq(device string, values frqSet) error
}

var pinConfig pinConfigurer = realPinConfig{}

type realPinConfig struct{}

func (r realPinConfig) applyPinSet(device string, pins pinSet) error {
	deviceDir := fmt.Sprintf("/sys/class/net/%s/device/ptp/", device)
	phcs, err := filesystem.ReadDir(deviceDir)
	if err != nil {
		return err
	}
	errList := []error{}
	for _, phc := range phcs {
		for pin, value := range pins {
			pinPath := fmt.Sprintf("/sys/class/net/%s/device/ptp/%s/pins/%s", device, phc.Name(), pin)
			glog.Infof("Setting \"%s\" > %s", value, pinPath)
			err = filesystem.WriteFile(pinPath, []byte(value), 0o666)
			if err != nil {
				glog.Errorf("e825 pin write failure: %s", err)
				errList = append(errList, err)
			}
		}
	}
	return errors.Join(errList...)
}

func (r realPinConfig) applyPinFrq(device string, values frqSet) error {
	deviceDir := fmt.Sprintf("/sys/class/net/%s/device/ptp/", device)
	phcs, err := filesystem.ReadDir(deviceDir)
	if err != nil {
		return err
	}
	errList := []error{}
	for _, phc := range phcs {
		periodPath := fmt.Sprintf("/sys/class/net/%s/device/ptp/%s/period", device, phc.Name())
		for _, value := range values {
			glog.Infof("Setting \"%s\" > %s", value, periodPath)
			err = filesystem.WriteFile(periodPath, []byte(value), 0o666)
			if err != nil {
				glog.Errorf("e825 period write failure: %s", err)
				errList = append(errList, err)
			}
		}
	}
	return errors.Join(errList...)
}
