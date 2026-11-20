package preparation

// SanitizeStatus validates a status returned by an injected or persistent
// store before it crosses the HTTP boundary. Persisted failure text is never
// exposed verbatim.
func SanitizeStatus(in Status) (Status, error) {
	if in.AssetID == "" || in.Attempts < 0 || in.Attempts > MaxAttempts || in.UpdatedAt.IsZero() {
		return Status{}, ErrDatabase
	}
	switch in.Status {
	case "not_prepared":
		if in.Attempts != 0 || in.FailureCode != nil {
			return Status{}, ErrDatabase
		}
	case "pending", "preparing", "ready", "failed":
	default:
		return Status{}, ErrDatabase
	}
	if in.Status == "preparing" && in.Attempts == 0 {
		return Status{}, ErrDatabase
	}
	if in.Status == "ready" {
		if in.Attempts == 0 || in.PreparedAt == nil || in.PreparedAt.IsZero() || in.FailureCode != nil {
			return Status{}, ErrDatabase
		}
		preparedAt := in.PreparedAt.UTC()
		in.PreparedAt = &preparedAt
	} else if in.PreparedAt != nil {
		return Status{}, ErrDatabase
	}
	if in.Status == "failed" && in.FailureCode == nil {
		return Status{}, ErrDatabase
	}
	if in.Status == "preparing" && in.FailureCode != nil {
		return Status{}, ErrDatabase
	}
	if in.FailureCode != nil {
		code := SafeFailureCode(*in.FailureCode)
		in.FailureCode = &code
	}
	in.UpdatedAt = in.UpdatedAt.UTC()
	return in, nil
}
