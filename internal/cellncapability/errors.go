package cellncapability

import "errors"

func IsReason(err error, reason string) bool {
	var capabilityErr *Error
	return errors.As(err, &capabilityErr) && capabilityErr.Reason == reason
}
