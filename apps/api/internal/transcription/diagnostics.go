package transcription

import "errors"

// OutputDiagnostic contains only safe validation facts, never provider content,
// filenames, object keys, credentials or raw errors. WordIndex is -1 when the
// failure is not associated with a particular word.
type OutputDiagnostic struct {
	Reason             string
	WordIndex          int
	ExpectedDurationMs int64
	ObservedDurationMs int64
	// Seconds preserve provider precision without rounding a boundary failure.
	// These are set only for WORD_AUDIO_BOUNDS, after finite-time validation.
	WordEndSeconds       float64
	AudioDurationSeconds float64
	OverrunSeconds       float64
	AllowedOverrunMs     int
}

type outputError struct{ diagnostic OutputDiagnostic }

func (e *outputError) Error() string { return "transcription: invalid output: " + e.diagnostic.Reason }
func (e *outputError) Unwrap() error { return ErrResponse }

// DiagnoseOutput extracts only internally constructed, sanitized diagnostics.
// Unknown errors are not formatted or exposed, even if they contain secrets.
func DiagnoseOutput(err error) OutputDiagnostic {
	var invalid *outputError
	if errors.As(err, &invalid) && invalid != nil {
		return invalid.diagnostic
	}
	return OutputDiagnostic{Reason: "OUTPUT_UNCLASSIFIED", WordIndex: -1}
}

func invalidOutput(reason string, wordIndex int) error {
	return &outputError{diagnostic: OutputDiagnostic{Reason: reason, WordIndex: wordIndex}}
}

func durationMismatch(expected, observed int64) error {
	return &outputError{diagnostic: OutputDiagnostic{
		Reason: "AUDIO_DURATION_MISMATCH", WordIndex: -1,
		ExpectedDurationMs: expected, ObservedDurationMs: observed,
	}}
}

func wordAudioBounds(index int, end, duration float64) error {
	return &outputError{diagnostic: OutputDiagnostic{
		Reason: "WORD_AUDIO_BOUNDS", WordIndex: index,
		WordEndSeconds: end, AudioDurationSeconds: duration,
		OverrunSeconds: end - duration, AllowedOverrunMs: MaxWordEndOverrunMs,
	}}
}
