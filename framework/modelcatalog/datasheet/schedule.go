package datasheet

import (
	"fmt"
	"math"
	"strings"
	"time"
)

// Pricing schedule calendars. The generic evaluator intentionally supports
// only deterministic calendars. Provider holiday/workday calendars require
// authoritative external data and should be separate future calendar kinds.
const (
	// PricingScheduleCalendarNone ignores the date and evaluates only time.
	PricingScheduleCalendarNone = "none"
	// PricingScheduleCalendarISOWeekday evaluates ISO weekday plus time.
	PricingScheduleCalendarISOWeekday = "iso_weekday"
)

// PricingTimeSchedule defines recurring time-based price multipliers.
//
// A schedule is provider-generic: DeepSeek is the first consumer, but other
// providers can describe their own timezone/day/time windows with the same
// structure. No rule is a peak/off-peak statement by itself; each rule simply
// supplies the multiplier to apply to the final resolved tier rate.
type PricingTimeSchedule struct {
	// Timezone is an IANA location name (for example "Asia/Shanghai").
	Timezone string `json:"timezone,omitempty"`
	// Calendar selects how dates are interpreted. Initial supported values are
	// "none" and "iso_weekday". Holidays and makeup workdays are intentionally
	// not modeled yet.
	Calendar string `json:"calendar,omitempty"`
	// Rules are recurring half-open local-time windows.
	Rules []PricingTimeRule `json:"rules,omitempty"`
}

// PricingTimeRule is one recurring schedule rule.
type PricingTimeRule struct {
	// Days are ISO weekday names for iso_weekday schedules. Empty means every
	// day. Values are case-insensitive and normalized to lowercase.
	Days []string `json:"days,omitempty"`
	// StartTime and EndTime are inclusive local wall-clock boundaries in HH:MM
	// format. The window is [StartTime, EndTime). EndTime may precede StartTime,
	// which creates a cross-midnight window.
	StartTime string `json:"start_time,omitempty"`
	EndTime   string `json:"end_time,omitempty"`
	// Multiplier is applied to the final resolved pricing rate. It must be
	// greater than zero.
	Multiplier float64 `json:"multiplier"`
}

// PricingScheduleEvaluation reports the multiplier selected for one instant.
type PricingScheduleEvaluation struct {
	Multiplier float64
	Matched    bool
	RuleIndex  int
}

// EvaluatePricingTimeSchedule evaluates schedule at the authoritative billing
// instant (the provider attempt start). The returned multiplier is 1 and
// Matched is false when no rule matches. Callers must not substitute another
// timestamp for at.
func EvaluatePricingTimeSchedule(schedule *PricingTimeSchedule, at time.Time) (PricingScheduleEvaluation, error) {
	if schedule == nil {
		return PricingScheduleEvaluation{Multiplier: 1}, nil
	}

	calendar, err := normalizePricingScheduleCalendar(schedule.Calendar)
	if err != nil {
		return PricingScheduleEvaluation{}, err
	}
	location, err := time.LoadLocation(strings.TrimSpace(schedule.Timezone))
	if err != nil {
		return PricingScheduleEvaluation{}, fmt.Errorf("invalid pricing schedule timezone %q: %w", schedule.Timezone, err)
	}

	local := at.In(location)
	for index, rule := range schedule.Rules {
		matched, err := rule.matches(local, calendar)
		if err != nil {
			return PricingScheduleEvaluation{}, fmt.Errorf("pricing schedule rule %d: %w", index, err)
		}
		if matched {
			return PricingScheduleEvaluation{
				Multiplier: rule.Multiplier,
				Matched:    true,
				RuleIndex:  index,
			}, nil
		}
	}
	return PricingScheduleEvaluation{Multiplier: 1}, nil
}

// ValidatePricingTimeSchedule validates a schedule without evaluating it.
func ValidatePricingTimeSchedule(schedule *PricingTimeSchedule) error {
	if schedule == nil {
		return nil
	}

	calendar, err := normalizePricingScheduleCalendar(schedule.Calendar)
	if err != nil {
		return err
	}
	if _, err := time.LoadLocation(strings.TrimSpace(schedule.Timezone)); err != nil {
		return fmt.Errorf("invalid pricing schedule timezone %q: %w", schedule.Timezone, err)
	}
	if len(schedule.Rules) == 0 {
		return fmt.Errorf("pricing schedule rules must not be empty")
	}

	daySets := make([]map[string]struct{}, len(schedule.Rules))
	for index, rule := range schedule.Rules {
		if err := rule.validate(calendar); err != nil {
			return fmt.Errorf("pricing schedule rule %d: %w", index, err)
		}
		daySets[index] = rule.daySet()
		// calendar=none ignores Dates/Days; all rules therefore share the same
		// virtual day and must use the full weekday set during overlap checks.
		if calendar == PricingScheduleCalendarNone {
			daySets[index] = allPricingDays()
		}
	}

	for i := 0; i < len(schedule.Rules); i++ {
		for j := i + 1; j < len(schedule.Rules); j++ {
			if pricingRulesOverlap(schedule.Rules[i], schedule.Rules[j], daySets[i], daySets[j]) {
				if schedule.Rules[i].Multiplier == schedule.Rules[j].Multiplier {
					// Identical-multiplier overlaps are deterministic: the first
					// matching rule yields the same result either way. This also
					// lets a weekend full-day rule coexist with a weekday
					// cross-midnight window that spills into Saturday morning.
					continue
				}
				return fmt.Errorf("pricing schedule rules %d and %d overlap with different multipliers", i, j)
			}
		}
	}
	return nil
}

// normalizePricingScheduleCalendar canonicalizes and validates a calendar name.
func normalizePricingScheduleCalendar(calendar string) (string, error) {
	switch value := strings.ToLower(strings.TrimSpace(calendar)); value {
	case PricingScheduleCalendarNone, PricingScheduleCalendarISOWeekday:
		return value, nil
	default:
		return "", fmt.Errorf("unsupported pricing schedule calendar %q", calendar)
	}
}

// validate checks the rule fields that are meaningful for the selected calendar.
func (rule PricingTimeRule) validate(calendar string) error {
	if rule.Multiplier <= 0 || math.IsNaN(rule.Multiplier) || math.IsInf(rule.Multiplier, 0) {
		return fmt.Errorf("multiplier must be a finite value greater than zero")
	}
	if _, err := parsePricingClock(rule.StartTime); err != nil {
		return fmt.Errorf("invalid start_time %q: %w", rule.StartTime, err)
	}
	if _, err := parsePricingClock(rule.EndTime); err != nil {
		return fmt.Errorf("invalid end_time %q: %w", rule.EndTime, err)
	}
	if calendar == PricingScheduleCalendarISOWeekday {
		for day := range rule.daySet() {
			if !isPricingDay(day) {
				return fmt.Errorf("unknown weekday %q", day)
			}
		}
	}
	return nil
}

// matches reports whether the local instant falls within the recurring rule.
func (rule PricingTimeRule) matches(at time.Time, calendar string) (bool, error) {
	if err := rule.validate(calendar); err != nil {
		return false, err
	}

	startMinute, startErr := parsePricingClockMinutes(rule.StartTime)
	if startErr != nil {
		return false, startErr
	}
	endMinute, endErr := parsePricingClockMinutes(rule.EndTime)
	if endErr != nil {
		return false, endErr
	}

	currentMinute := clockMinutes(at)
	if startMinute == endMinute {
		days := rule.daySet()
		if calendar == PricingScheduleCalendarISOWeekday && len(days) != 0 && !containsPricingDay(days, at.Weekday()) {
			return false, nil
		}
		return true, nil
	}

	effectiveAt := at
	if startMinute > endMinute && currentMinute < endMinute {
		// The morning tail of a wrapped window belongs to the previous weekday.
		effectiveAt = at.AddDate(0, 0, -1)
	}

	duration := endMinute - startMinute
	if duration < 0 {
		duration += 24 * 60
	}
	offset := (currentMinute - startMinute + 24*60) % (24 * 60)
	if offset >= duration {
		return false, nil
	}

	days := rule.daySet()
	if calendar == PricingScheduleCalendarISOWeekday && len(days) != 0 && !containsPricingDay(days, effectiveAt.Weekday()) {
		return false, nil
	}
	return true, nil
}

// daySet returns the normalized weekday names configured on the rule.
func (rule PricingTimeRule) daySet() map[string]struct{} {
	days := make(map[string]struct{}, len(rule.Days))
	for _, day := range rule.Days {
		days[strings.ToLower(strings.TrimSpace(day))] = struct{}{}
	}
	return days
}

// parsePricingClock converts an HH:MM wall-clock value into a day offset.
func parsePricingClock(value string) (time.Duration, error) {
	if len(value) != 5 || value[2] != ':' {
		return 0, fmt.Errorf("must be HH:MM")
	}
	minutes, err := parsePricingClockMinutes(value)
	if err != nil {
		return 0, err
	}
	return time.Duration(minutes) * time.Minute, nil
}

// parsePricingClockMinutes parses a strict ASCII HH:MM value into minutes.
func parsePricingClockMinutes(value string) (int, error) {
	if len(value) != 5 || value[2] != ':' ||
		value[0] < '0' || value[0] > '9' ||
		value[1] < '0' || value[1] > '9' ||
		value[3] < '0' || value[3] > '9' ||
		value[4] < '0' || value[4] > '9' {
		return 0, fmt.Errorf("must be HH:MM")
	}
	hour := int(value[0]-'0')*10 + int(value[1]-'0')
	minute := int(value[3]-'0')*10 + int(value[4]-'0')
	if hour > 23 || minute > 59 {
		return 0, fmt.Errorf("must be between 00:00 and 23:59")
	}
	return hour*60 + minute, nil
}

// isPricingDay reports whether day is a supported normalized weekday name.
func isPricingDay(day string) bool {
	_, ok := allPricingDays()[day]
	return ok
}

// containsPricingDay reports whether days includes weekday.
func containsPricingDay(days map[string]struct{}, weekday time.Weekday) bool {
	_, ok := days[strings.ToLower(weekday.String())]
	return ok
}

// clockMinutes returns the local wall-clock minute within the day.
func clockMinutes(at time.Time) int {
	hour, minute, _ := at.Clock()
	return hour*60 + minute
}

// pricingRuleDaysIntersect reports whether two normalized weekday sets overlap.
func pricingRuleDaysIntersect(aDays, bDays map[string]struct{}) bool {
	if len(aDays) == 0 || len(bDays) == 0 {
		return false
	}
	for day := range aDays {
		if _, ok := bDays[day]; ok {
			return true
		}
	}
	return false
}

type pricingRuleInterval struct {
	days        map[string]struct{}
	startMinute int
	endMinute   int
}

// pricingRulesOverlap reports whether two rules can match the same instant.
// Empty day sets mean every day, so they are normalized to the full weekday
// set before interval comparison. Under calendar=none every rule also shares
// the same virtual date and therefore the full weekday set.
func pricingRulesOverlap(a, b PricingTimeRule, aDays, bDays map[string]struct{}) bool {
	aIntervals := normalizedPricingRuleIntervals(a, aDays)
	bIntervals := normalizedPricingRuleIntervals(b, bDays)
	for _, aInterval := range aIntervals {
		for _, bInterval := range bIntervals {
			if pricingRuleDaysIntersect(aInterval.days, bInterval.days) &&
				aInterval.startMinute < bInterval.endMinute &&
				bInterval.startMinute < aInterval.endMinute {
				return true
			}
		}
	}
	return false
}

// normalizedPricingRuleIntervals expands a rule into weekly half-open intervals.
func normalizedPricingRuleIntervals(rule PricingTimeRule, days map[string]struct{}) []pricingRuleInterval {
	if len(days) == 0 {
		days = allPricingDays()
	}

	start, _ := parsePricingClockMinutes(rule.StartTime)
	end, _ := parsePricingClockMinutes(rule.EndTime)
	if start == end {
		return []pricingRuleInterval{{days: days, startMinute: 0, endMinute: 24 * 60}}
	}
	if start < end {
		return []pricingRuleInterval{{days: days, startMinute: start, endMinute: end}}
	}

	// A wrapped window covers the start day's evening and the next day's
	// morning. Normalizing to these two intervals makes weekday validation
	// independent of the wall clock under evaluation.
	return []pricingRuleInterval{
		{days: days, startMinute: start, endMinute: 24 * 60},
		{days: nextPricingDays(days), startMinute: 0, endMinute: end},
	}
}

// allPricingDays returns a set containing every supported weekday.
func allPricingDays() map[string]struct{} {
	return map[string]struct{}{
		"sunday": {}, "monday": {}, "tuesday": {}, "wednesday": {}, "thursday": {}, "friday": {}, "saturday": {},
	}
}

// nextPricingDays shifts each weekday in days forward by one day.
func nextPricingDays(days map[string]struct{}) map[string]struct{} {
	nextDays := make(map[string]struct{}, len(days))
	for day := range days {
		nextDays[nextPricingDay(day)] = struct{}{}
	}
	return nextDays
}

// nextPricingDay returns the weekday immediately following day.
func nextPricingDay(day string) string {
	switch day {
	case "sunday":
		return "monday"
	case "monday":
		return "tuesday"
	case "tuesday":
		return "wednesday"
	case "wednesday":
		return "thursday"
	case "thursday":
		return "friday"
	case "friday":
		return "saturday"
	case "saturday":
		return "sunday"
	default:
		return day
	}
}
