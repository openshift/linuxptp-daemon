package daemon

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/golang/glog"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/event"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/hardwareconfig"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/parser"
	parserconstants "github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/parser/constants"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/process"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/utils"
	ptpv1 "github.com/k8snetworkplumbingwg/ptp-operator/api/v1"
)

type sourceQualificationPolicy struct {
	enabled bool
	config  utils.QualificationConfig
}

type sourceQualificationRuntime struct {
	mu           sync.Mutex // teardown waits for an in-flight hardware action
	closed       bool
	generation   uint64
	ctx          context.Context
	apply        func(bool) error
	applied      bool
	appliedKnown bool
}

func (p *ptpProcess) configureSourceQualification(policy sourceQualificationPolicy) error {
	if len(p.tBCAttributes.trIfaceNames) == 0 {
		return fmt.Errorf("source qualification requires upstream TR ports")
	}
	hasHardware := p.dn != nil && p.dn.hardwareConfigManager != nil && p.dn.hardwareConfigManager.HasHardwareConfigForProfile(p.nodeProfile)
	if policy.config.UseS3 && !hasHardware {
		return fmt.Errorf("S3 source qualification requires HardwareConfig")
	}
	if p.dn == nil || p.dn.processManager == nil || p.dn.processManager.clockMgr == nil {
		return fmt.Errorf("source qualification requires a clock manager")
	}
	generation, err := p.dn.processManager.clockMgr.ConfigureSourceQualification(p.configName, p.tBCAttributes.trIfaceNames, policy.config)
	if err != nil {
		return err
	}
	p.sourceQualification = &sourceQualificationRuntime{
		generation: generation,
		apply: func(qualified bool) error {
			condition := hardwareconfig.ConditionTypeLost
			if qualified {
				condition = hardwareconfig.ConditionTypeLocked
			}
			if hasHardware {
				return p.dn.hardwareConfigManager.ApplyConditionForProfile(p.nodeProfile, condition)
			}
			hook := "tbc-ho-entry"
			if qualified {
				hook = "tbc-ho-exit"
			}
			// The legacy manager dispatcher discards hook errors. Source pin
			// application must retain them so failed actions are retried rather
			// than recorded as successfully applied. Still invoke every plugin.
			var failures []error
			for name, handler := range p.pm.Plugins {
				if handler.AfterRunPTPCommand != nil {
					if hookErr := handler.AfterRunPTPCommand(p.pm.Data[name], p.nodeProfile, hook); hookErr != nil {
						failures = append(failures, fmt.Errorf("plugin %s %s: %w", name, hook, hookErr))
					}
				}
			}
			return errors.Join(failures...)
		},
	}
	glog.Infof("Source qualification profile=%s config=%s S3=%t thresholds=%d/%d samples=%d/%d thresholdFallbacks=%t/%t",
		getProfileName(p.nodeProfile), p.configName, policy.config.UseS3,
		policy.config.QualifiedThreshold, policy.config.DisqualifiedThreshold,
		policy.config.QualifiedSamples, policy.config.DisqualifiedSamples,
		p.nodeProfile.PtpClockThreshold.PtpSourceQualifiedThreshold == nil,
		p.nodeProfile.PtpClockThreshold.PtpSourceDisqualifiedThreshold == nil)
	return nil
}

// forwardSourceData forwards fresh parser facts before metrics mutate
// them. The clock evaluates readiness; the scanner retains hardware application.
func (p *ptpProcess) forwardSourceData(metrics *parser.Metrics, portEvent *parser.PTPEvent) {
	q := p.sourceQualification
	if q == nil || p.name != ptp4lProcessName {
		return
	}
	if portEvent != nil && portEvent.Role != parserconstants.PortRoleUnknown {
		port := portEvent.Iface
		if port == "" && portEvent.PortID > 0 && portEvent.PortID <= len(p.ifaces) {
			port = p.ifaces[portEvent.PortID-1].Name
		}
		p.observeSource(port, &event.PTPSourceObservation{PortRole: utils.ToPtr(portEvent.Role)})
	}
	// Summary/port-status lines are not fresh servo measurements.
	if metrics == nil || metrics.ServoState == "" {
		return
	}
	p.observeSource(metrics.Iface, &event.PTPSourceObservation{Offset: metrics.Offset, ServoState: metrics.ServoState})
}

func (p *ptpProcess) observeSource(port string, observation *event.PTPSourceObservation) {
	q := p.sourceQualification
	q.mu.Lock()
	observation.Generation = q.generation
	ctx := q.ctx
	closed := q.closed
	q.mu.Unlock()
	if closed {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	observation.Reply = make(chan event.SourceQualification, 1)
	ev := event.Event{Source: event.PTP4l, CfgName: p.configName, IFace: port, ClockType: p.clockType, Time: time.Now().UnixMilli(), Data: observation}
	if observation.PortRole != nil || observation.SourceLost {
		// These transitions may not repeat. Wait for delivery, but allow daemon
		// shutdown to release a sender whose consumer has stopped.
		select {
		case p.eventCh <- ev:
		case <-ctx.Done():
			return
		}
	} else {
		select {
		case p.eventCh <- ev:
		case <-ctx.Done():
			return
		default:
			glog.Errorf("source qualification event channel full for %s[%s]; dropping observation", p.name, p.configName)
			return
		}
	}
	select {
	case result := <-observation.Reply:
		if result.SourceType == event.PTP4l {
			p.applySourceQualification(result)
		}
	case <-ctx.Done():
	}
}

func (p *ptpProcess) applySourceQualification(result event.SourceQualification) {
	q := p.sourceQualification
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed || result.Generation != q.generation {
		return
	}
	p.tBCAttributes.activePort = result.IFace // label existing state/averaged-offset events
	previousState := p.tBCAttributes.lastReportedState
	if result.Changed {
		glog.Infof("PTPSourceQualified profile=%s config=%s source=%s port=%s qualified=%t reason=%q samples=%d",
			getProfileName(p.nodeProfile), p.configName, result.SourceType, result.IFace, result.Qualified, result.Reason, result.Samples)
	}
	if result.Qualified {
		p.tBCAttributes.lastReportedState = event.PTP_LOCKED
	} else {
		p.tBCAttributes.lastReportedState = event.PTP_FREERUN
	}
	// Source loss is a fact even if a pin update fails. Report it independently
	// of successful hardware application, without fabricating aggregate lock.
	if previousState != p.tBCAttributes.lastReportedState {
		p.sendPtp4lStateEvent()
	}
	if q.appliedKnown && q.applied == result.Qualified {
		return
	}
	if err := q.apply(result.Qualified); err != nil {
		q.appliedKnown = false // failed actions may have partially programmed pins
		glog.Errorf("PTPSourceQualified hardware apply profile=%s qualified=%t: %v", getProfileName(p.nodeProfile), result.Qualified, err)
		return
	}
	q.applied, q.appliedKnown = result.Qualified, true
	p.tBCAttributes.lastAppliedState = event.PTP_HOLDOVER
	if result.Qualified {
		p.tBCAttributes.lastAppliedState = event.PTP_LOCKED
	}
}

func (p *ptpProcess) invalidateSourceQualification() {
	if p.sourceQualification == nil {
		return
	}
	p.observeSource("", &event.PTPSourceObservation{SourceLost: true})
}

func (q *sourceQualificationRuntime) close() {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.closed = true
}

func sourceQualifiedStartCondition(env ptpProcessEnv) process.Condition {
	var conditions []process.Condition
	if env.topology != nil && env.topology.isHAProfile(getProfileName(env.nodeProfile)) {
		for _, cfg := range haPtp4lConfigs(env.topology) {
			conditions = append(conditions, process.OnSourceQualified{ConfigName: cfg})
		}
	}
	if len(conditions) == 0 {
		return process.OnSourceQualified{ConfigName: getPtp4lConfig(env.runID)}
	}
	return process.Any{Conditions: conditions}
}

// resolveSourceQualificationPolicy preserves the explicit opt-in boundary. Resolve
// the migration fallback once, never from the aggregate InSync state at runtime.
func resolveSourceQualificationPolicy(profile *ptpv1.PtpProfile) (sourceQualificationPolicy, error) {
	if profileClockType(profile) != TBC || profile.PtpClockThreshold == nil {
		return sourceQualificationPolicy{}, nil
	}
	th := profile.PtpClockThreshold
	if th.PtpSourceQualifiedThreshold == nil && th.PtpSourceDisqualifiedThreshold == nil &&
		th.PtpSourceQualifiedSamples == nil && th.PtpSourceDisqualifiedSamples == nil && !th.PtpSourceUseS3 {
		return sourceQualificationPolicy{}, nil
	}
	fallback := th.MaxOffsetThreshold
	if th.PtpSourceQualifiedThreshold == nil || th.PtpSourceDisqualifiedThreshold == nil {
		if fallback <= 0 {
			fallback = 100
			if isNtpFailoverEnabled(profile) {
				fallback = 1000
			}
		}
		if value, ok := profile.PtpSettings["inSyncConditionThreshold"]; ok {
			var err error
			fallback, err = strconv.ParseInt(value, 0, 64)
			if err != nil || fallback <= 0 {
				return sourceQualificationPolicy{}, fmt.Errorf("invalid source qualification fallback inSyncConditionThreshold %q", value)
			}
		}
	}
	cfg := utils.QualificationConfig{
		QualifiedThreshold: fallback, DisqualifiedThreshold: fallback,
		QualifiedSamples: 5, DisqualifiedSamples: 5, UseS3: th.PtpSourceUseS3,
	}
	if th.PtpSourceQualifiedThreshold != nil {
		cfg.QualifiedThreshold = *th.PtpSourceQualifiedThreshold
	}
	if th.PtpSourceDisqualifiedThreshold != nil {
		cfg.DisqualifiedThreshold = *th.PtpSourceDisqualifiedThreshold
	}
	for _, count := range []struct {
		value *int64
		dest  *int
	}{
		{th.PtpSourceQualifiedSamples, &cfg.QualifiedSamples},
		{th.PtpSourceDisqualifiedSamples, &cfg.DisqualifiedSamples},
	} {
		if count.value != nil {
			if *count.value < 1 || uint64(*count.value) > uint64(^uint(0)>>1) {
				return sourceQualificationPolicy{}, fmt.Errorf("invalid source qualification sample count %d", *count.value)
			}
			*count.dest = int(*count.value)
		}
	}
	if err := cfg.Validate(); err != nil {
		return sourceQualificationPolicy{}, fmt.Errorf("invalid source qualification policy: %w", err)
	}
	return sourceQualificationPolicy{enabled: true, config: cfg}, nil
}
