package utils

import (
	"fmt"
	"math"
)

// QualificationConfig is an upstream-source policy, independent of aggregate InSync.
// Callers resolve optional API values before constructing it.
type QualificationConfig struct {
	QualifiedThreshold    int64
	DisqualifiedThreshold int64
	QualifiedSamples      int
	DisqualifiedSamples   int
	UseS3                 bool
}

// Validate rejects unsafe policies instead of silently clamping invalid API inputs.
func (c QualificationConfig) Validate() error {
	if c.QualifiedThreshold <= 0 || c.DisqualifiedThreshold < c.QualifiedThreshold {
		return fmt.Errorf("source thresholds must be positive and disqualified >= qualified")
	}
	if c.QualifiedSamples < 1 || c.DisqualifiedSamples < 1 {
		return fmt.Errorf("source sample counts must be positive")
	}
	return nil
}

// QualificationSource identifies the timing input whose readiness is being
// evaluated. For PTP, this is the selected local upstream receive port.
// Other source types may identify a GNSS receiver or an external PPS input.
// It is not the process reporting measurements, the local clock being
// synchronized, or the upstream grandmaster's ultimate time reference.
//
// Implementations must be immutable, non-nil identities. Equal must compare the
// source kind and identity, returning false for nil or a different source kind.
type QualificationSource interface {
	Equal(QualificationSource) bool
	String() string
}

// QualificationSample is one fresh measurement from the active valid source.
// Unrelated log lines and reconciliation ticks must not be submitted as samples.
type QualificationSample struct {
	Source          QualificationSource
	Offset          float64
	ServoState      string
	SourceAvailable bool
}

// QualificationResult describes readiness and supplies transition logging context.
type QualificationResult struct {
	Source    QualificationSource // evaluated input, or the previous input on reset
	Qualified bool
	Changed   bool
	Reason    string
	Samples   int
}

// QualificationFilter permits alternate numeric filters without exposing their counters.
type QualificationFilter interface {
	// Update consumes one offset; only HysteresisLow means qualified.
	Update(float64) (HysteresisState, bool)
	Reset()
	// Samples reports implementation-specific evidence (run length/window size).
	Samples() int
}

// QualificationState owns the readiness of one profile's active upstream source.
// It is used by the source's serialized input path, not concurrently by consumers.
type QualificationState struct {
	config    QualificationConfig
	filter    QualificationFilter
	source    QualificationSource
	qualified bool
}

// NewQualificationState creates an initially unqualified source evaluator.
func NewQualificationState(config QualificationConfig) (*QualificationState, error) {
	s := &QualificationState{}
	if err := s.Configure(config); err != nil {
		return nil, err
	}
	return s, nil
}

// Configure replaces the policy and resets readiness when its values change.
// Invalid replacement policies fail closed. Reset is also required when the input
// configuration generation changes without a change to the resolved policy values.
func (s *QualificationState) Configure(config QualificationConfig) error {
	if err := config.Validate(); err != nil {
		s.Reset()
		s.filter = nil
		return err
	}
	if s.filter != nil && s.config == config {
		return nil
	}
	s.Reset()
	s.config = config
	s.filter = NewHysteresisFilter(float64(config.QualifiedThreshold), float64(config.DisqualifiedThreshold), config.QualifiedSamples, config.DisqualifiedSamples)
	return nil
}

// Qualified returns evaluated source readiness, not aggregate clock lock.
func (s *QualificationState) Qualified() bool { return s.qualified }

// Reset invalidates readiness and all source-specific history, retaining policy.
func (s *QualificationState) Reset() QualificationResult {
	wasQualified := s.qualified
	source := s.source
	s.qualified = false
	s.source = nil
	if s.filter != nil {
		s.filter.Reset()
	}
	return QualificationResult{Source: source, Changed: wasQualified, Reason: "source reset"}
}

// Evaluate advances readiness once for a fresh active-source sample. Source loss
// or malformed input revokes readiness immediately, without consulting InSync.
func (s *QualificationState) Evaluate(sample QualificationSample) QualificationResult {
	previous := s.qualified
	if s.filter == nil || !sample.SourceAvailable || sample.Source == nil {
		result := s.Reset()
		result.Reason = "source unavailable"
		return result
	}
	if s.source == nil || !s.source.Equal(sample.Source) {
		s.Reset()
		s.source = sample.Source
	}
	result := QualificationResult{Source: s.source}
	if s.config.UseS3 {
		s.qualified = sample.ServoState == "s3"
		result.Reason = "servo " + sample.ServoState
		result.Samples = 1
	} else {
		if math.IsNaN(sample.Offset) || math.IsInf(sample.Offset, 0) {
			result = s.Reset()
			result.Changed = previous
			result.Reason = "invalid source offset"
			return result
		}
		state, _ := s.filter.Update(sample.Offset)
		s.qualified = state == HysteresisLow
		result.Reason = "offset window"
		result.Samples = s.filter.Samples()
	}
	result.Qualified = s.qualified
	result.Changed = previous != s.qualified
	return result
}
