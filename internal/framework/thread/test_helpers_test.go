package thread

import "time"

func fixedNow() func() time.Time {
	current := time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC)
	return func() time.Time {
		current = current.Add(time.Millisecond)
		return current
	}
}
