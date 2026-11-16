# Shared holdover estimation

`dpll.HoldoverModel.Estimate(time.Duration) (int64, error)` estimates a
nonnegative error in nanoseconds independently of clock type. The default
rounded linear estimator uses the resolved maximum offset divided by timeout
in seconds, multiplied by elapsed seconds. HardwareConfig and legacy profile
limits reach the same DPLL constructor; no hardware selector or model is added.

Both existing GNSS-leading T-GM and PTP-leading T-BC holdover workers call this
interface. A validated `WithHoldoverModel` constructor option supplies a model
per instance after configuration and constructor options are resolved. The model
is fixed for the instance lifetime and reused with each episode's elapsed time.
Models must be immutable, bounded, concurrency-safe and perform
no I/O. Nil/typed-nil
injection is rejected, and invalid estimates fail an active attempt closed.
Zero maximum offset with a positive timeout retains zero drift; no positive
default is silently inserted. The public positive linear constructor rejects
zero limits and negative elapsed/unrepresentable results return errors.

Estimation does not change the existing clock policies: GNSS without frequency
traceability still exits to free run at its in-spec error crossing. Legacy
PTP-leading holdover may continue out of spec until its hard timeout, and
existing offset reporting, flags, class mappings and source qualification are
unchanged. There is no new T-GM class140 behavior in this estimator extraction.

Unit tests exercise the rounded default, limits/errors, constructor injection
and nonlinear models through both actual legacy holdover workers. They do not
prove physical hardware drift, deployed timing or PMC wire interoperability.
