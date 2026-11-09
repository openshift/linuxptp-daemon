package utils

import (
	"math"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestQualificationOffsetWindows(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		offsets []float64
		want    []bool
	}{
		{"inclusive absolute qualification", []float64{-100, 100}, []bool{false, true}},
		{"asymmetric disqualification", []float64{0, 0, -201, 201, 201}, []bool{false, true, true, true, false}},
		{"qualification run interrupted", []float64{100, 150, 100, 100}, []bool{false, false, false, true}},
		{"high equality interrupts disqualification", []float64{0, 0, 201, 201, 200, 201, 201, 201}, []bool{false, true, true, true, true, true, true, false}},
		{"band retains unqualified", []float64{150, 150, 150}, []bool{false, false, false}},
		{"band retains qualified", []float64{0, 0, 150, 150}, []bool{false, true, true, true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s, err := NewQualificationState(QualificationConfig{QualifiedThreshold: 100, DisqualifiedThreshold: 200, QualifiedSamples: 2, DisqualifiedSamples: 3})
			require.NoError(t, err)
			previous := false
			for i, offset := range tc.offsets {
				got := s.Evaluate(QualificationSample{Source: testPortSource("tr0"), Offset: offset, SourceAvailable: true})
				require.Equal(t, testPortSource("tr0"), got.Source)
				require.Equal(t, tc.want[i], got.Qualified, "sample %d", i)
				require.Equal(t, previous != tc.want[i], got.Changed, "sample %d", i)
				require.Equal(t, got.Qualified, s.Qualified())
				previous = got.Qualified
			}
		})
	}
}

func TestQualificationSourceAndConfigurationReset(t *testing.T) {
	t.Parallel()
	for _, tc := range []string{"loss", "source change", "configuration", "explicit reset", "nan", "infinity", "missing source"} {
		t.Run(tc, func(t *testing.T) {
			t.Parallel()
			cfg := QualificationConfig{QualifiedThreshold: 100, DisqualifiedThreshold: 200, QualifiedSamples: 2, DisqualifiedSamples: 3}
			s, err := NewQualificationState(cfg)
			require.NoError(t, err)
			sample := QualificationSample{Source: testPortSource("tr0"), SourceAvailable: true}
			s.Evaluate(sample)
			require.True(t, s.Evaluate(sample).Qualified)
			checkResult := func(result QualificationResult) {
				require.True(t, result.Changed)
				require.Equal(t, sample.Source, result.Source)
			}
			switch tc {
			case "loss":
				checkResult(s.Evaluate(QualificationSample{}))
			case "source change":
				sample.Source = testPortSource("tr1")
				checkResult(s.Evaluate(sample))
				// The new port has one fresh sample, never the old port's run.
				require.True(t, s.Evaluate(sample).Qualified)
				return
			case "configuration":
				cfg.QualifiedThreshold = 50
				require.NoError(t, s.Configure(cfg))
			case "explicit reset":
				checkResult(s.Reset())
			case "nan":
				bad := sample
				bad.Offset = math.NaN()
				checkResult(s.Evaluate(bad))
			case "infinity":
				bad := sample
				bad.Offset = math.Inf(1)
				checkResult(s.Evaluate(bad))
			case "missing source":
				bad := sample
				bad.Source = nil
				checkResult(s.Evaluate(bad))
			}
			require.False(t, s.Qualified())
			require.False(t, s.Evaluate(sample).Qualified)
			require.True(t, s.Evaluate(sample).Qualified)
		})
	}
}

func TestQualificationS3(t *testing.T) {
	t.Parallel()
	s, err := NewQualificationState(QualificationConfig{UseS3: true, QualifiedThreshold: 100, DisqualifiedThreshold: 200, QualifiedSamples: 5, DisqualifiedSamples: 5})
	require.NoError(t, err)
	for _, tc := range []struct {
		servo string
		want  bool
	}{
		{"s2", false}, {"s3", true}, {"s3", true}, {"s2", false}, {"s3", true},
		{"s1", false}, {"s0", false}, {"", false}, {"unknown", false}, {"s3", true},
	} {
		got := s.Evaluate(QualificationSample{Source: testPortSource("tr0"), SourceAvailable: true, ServoState: tc.servo, Offset: 1e9})
		require.Equal(t, testPortSource("tr0"), got.Source)
		require.Equal(t, tc.want, got.Qualified, tc.servo)
	}
	require.False(t, s.Evaluate(QualificationSample{Source: testPortSource("tr0"), ServoState: "s3"}).Qualified)
}

func TestQualificationValidation(t *testing.T) {
	t.Parallel()
	for _, tc := range []QualificationConfig{
		{}, {QualifiedThreshold: -1, DisqualifiedThreshold: 200, QualifiedSamples: 1, DisqualifiedSamples: 1},
		{QualifiedThreshold: 100, DisqualifiedThreshold: 99, QualifiedSamples: 1, DisqualifiedSamples: 1},
		{QualifiedThreshold: 100, DisqualifiedThreshold: 200, QualifiedSamples: 0, DisqualifiedSamples: 1},
		{QualifiedThreshold: 100, DisqualifiedThreshold: 200, QualifiedSamples: 1, DisqualifiedSamples: -1},
	} {
		_, err := NewQualificationState(tc)
		require.Error(t, err)
	}
	s, err := NewQualificationState(QualificationConfig{QualifiedThreshold: 100, DisqualifiedThreshold: 100, QualifiedSamples: 1, DisqualifiedSamples: 1})
	require.NoError(t, err)
	require.NoError(t, s.Configure(QualificationConfig{UseS3: true, QualifiedThreshold: 100, DisqualifiedThreshold: 100, QualifiedSamples: 1, DisqualifiedSamples: 1}))
	require.True(t, s.Evaluate(QualificationSample{Source: testPortSource("tr0"), SourceAvailable: true, ServoState: "s3"}).Qualified)
	require.Error(t, s.Configure(QualificationConfig{}))
	require.False(t, s.Qualified(), "invalid replacement policy must fail closed")
}

func TestQualificationTransitionDetails(t *testing.T) {
	t.Parallel()
	s, err := NewQualificationState(QualificationConfig{QualifiedThreshold: 100, DisqualifiedThreshold: 200, QualifiedSamples: 2, DisqualifiedSamples: 3})
	require.NoError(t, err)
	sample := QualificationSample{Source: testPortSource("tr0"), SourceAvailable: true}
	s.Evaluate(sample)
	got := s.Evaluate(sample)
	require.True(t, got.Changed)
	require.Equal(t, 2, got.Samples)
	require.NotEmpty(t, got.Reason)
	sample.Offset = 201
	s.Evaluate(sample)
	s.Evaluate(sample)
	got = s.Evaluate(sample)
	require.True(t, got.Changed)
	require.False(t, got.Qualified)
	require.Equal(t, 3, got.Samples)
	// Two evaluation owners never share source state or counters.
	other, err := NewQualificationState(s.config)
	require.NoError(t, err)
	require.False(t, other.Qualified())
}

type testQualificationFilter struct {
	state           HysteresisState
	samples, resets int
}

func (f *testQualificationFilter) Update(float64) (HysteresisState, bool) {
	f.samples = 7
	return f.state, false
}
func (f *testQualificationFilter) Samples() int { return f.samples }
func (f *testQualificationFilter) Reset()       { f.samples = 0; f.resets++ }

func TestQualificationFilterContract(t *testing.T) {
	t.Parallel()
	s, err := NewQualificationState(QualificationConfig{QualifiedThreshold: 100, DisqualifiedThreshold: 200, QualifiedSamples: 5, DisqualifiedSamples: 5})
	require.NoError(t, err)
	f := &testQualificationFilter{state: HysteresisLow}
	s.filter = f
	sample := QualificationSample{Source: testPortSource("tr0"), SourceAvailable: true, Offset: 321}
	result := s.Evaluate(sample)
	require.True(t, result.Qualified, "the implementation, not the wrapper, decides readiness")
	require.Equal(t, 7, result.Samples)
	f.state = HysteresisHigh
	require.False(t, s.Evaluate(sample).Qualified)
	s.Reset()
	require.Equal(t, 2, f.resets, "source selection and explicit reset use the filter contract")
	require.Zero(t, f.Samples())
}

type testPortSource string

func (s testPortSource) Equal(other QualificationSource) bool {
	port, ok := other.(testPortSource)
	return ok && s == port
}
func (s testPortSource) String() string { return string(s) }

// A receiver identifier can be structured and non-comparable. Qualification
// must use source-defined equality, not interface == or the display string.
type testReceiverSource struct{ path []string }

func (s testReceiverSource) Equal(other QualificationSource) bool {
	receiver, ok := other.(testReceiverSource)
	return ok && slices.Equal(s.path, receiver.path)
}
func (s testReceiverSource) String() string { return strings.Join(s.path, "/") }

func TestQualificationSourceIdentity(t *testing.T) {
	t.Parallel()
	s, err := NewQualificationState(QualificationConfig{QualifiedThreshold: 100, DisqualifiedThreshold: 200, QualifiedSamples: 2, DisqualifiedSamples: 1})
	require.NoError(t, err)
	sample := QualificationSample{Source: testPortSource("tr0"), SourceAvailable: true, Offset: 10}
	require.False(t, s.Evaluate(sample).Qualified)
	require.True(t, s.Evaluate(sample).Qualified)

	// Different source kinds must not share history even if their labels match.
	receiver := testReceiverSource{path: []string{"tr0"}}
	require.Equal(t, sample.Source.String(), receiver.String())
	sample.Source = receiver
	result := s.Evaluate(sample)
	require.False(t, result.Qualified)
	require.True(t, result.Changed)
	require.Equal(t, receiver, result.Source)
	// A fresh value with equal identity continues the same source's history.
	sample.Source = testReceiverSource{path: []string{"tr0"}}
	require.True(t, s.Evaluate(sample).Qualified)
	sample.Source = testReceiverSource{path: []string{"receiver1"}}
	require.False(t, s.Evaluate(sample).Qualified)
	require.True(t, s.Evaluate(sample).Qualified)
	result = s.Reset()
	require.True(t, result.Changed)
	require.True(t, sample.Source.Equal(result.Source), "reset identifies the input whose readiness was revoked")
	require.False(t, s.Qualified())
	require.Nil(t, s.Reset().Source, "the previous source must not leak into later resets")
}
