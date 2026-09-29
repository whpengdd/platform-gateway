package jira

import "time"

const jqlTimeLayout = "2006-01-02 15:04"

// repeatedLocalMinute tests the final JQL minute for multiple offset occurrences.
// IANA UTC offsets fit within 48 hours. Enumerating every zone interval in this
// window also covers half-hour rollbacks and historical date-line shifts. Test
// the entire minute against each repeated wall-time interval: historical shifts
// can start partway through a minute, so checking only second zero is not enough.
func repeatedLocalMinute(t time.Time) bool {
	loc := t.Location()
	wall := time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), t.Minute(), 0, 0, time.UTC)
	wallEnd := wall.Add(time.Minute)
	end := wall.Add(48 * time.Hour)
	for probe := wall.Add(-48 * time.Hour).In(loc); !probe.After(end); {
		_, before := probe.Zone()
		_, next := probe.ZoneBounds()
		if next.IsZero() || !next.After(probe) || next.After(end) {
			break
		}
		_, after := next.In(loc).Zone()
		if before > after {
			repeatedStart := next.UTC().Add(time.Duration(after) * time.Second)
			repeatedEnd := next.UTC().Add(time.Duration(before) * time.Second)
			if wall.Before(repeatedEnd) && wallEnd.After(repeatedStart) {
				return true
			}
		}
		probe = next.In(loc)
	}
	return false
}
