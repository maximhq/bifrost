package datasheet

import (
	"testing"
	"time"
)

func pricingScheduleAt(day string, hour, minute int) time.Time {
	return time.Date(2026, time.August, mustAtoi(day), hour, minute, 0, 0, time.UTC)
}

func mustAtoi(value string) int {
	digits := map[byte]int{'0': 0, '1': 1, '2': 2, '3': 3, '4': 4, '5': 5, '6': 6, '7': 7, '8': 8, '9': 9}
	if len(value) != 2 {
		panic("expected two-digit day")
	}
	return digits[value[0]]*10 + digits[value[1]]
}

func TestEvaluatePricingTimeSchedule(t *testing.T) {
	// 2026-08-23 is a Sunday in UTC.
	schedule := &PricingTimeSchedule{
		Timezone: "UTC",
		Calendar: PricingScheduleCalendarISOWeekday,
		Rules: []PricingTimeRule{
			{Days: []string{"saturday", "sunday"}, StartTime: "00:00", EndTime: "00:00", Multiplier: 0.5},
			{Days: []string{"monday", "tuesday", "wednesday", "thursday", "friday"}, StartTime: "16:30", EndTime: "00:30", Multiplier: 0.5},
		},
	}

	tests := []struct {
		name       string
		at         time.Time
		multiplier float64
		matched    bool
	}{
		{name: "sunday full day", at: pricingScheduleAt("23", 12, 0), multiplier: 0.5, matched: true},
		{name: "saturday full day", at: pricingScheduleAt("22", 0, 0), multiplier: 0.5, matched: true},
		{name: "weekday before window", at: pricingScheduleAt("24", 16, 29), multiplier: 1},
		{name: "weekday start is inclusive", at: pricingScheduleAt("24", 16, 30), multiplier: 0.5, matched: true},
		{name: "weekday end is exclusive", at: pricingScheduleAt("25", 0, 30), multiplier: 1},
		{name: "cross midnight before end", at: pricingScheduleAt("25", 0, 29), multiplier: 0.5, matched: true},
		{name: "weekday peak", at: pricingScheduleAt("24", 12, 0), multiplier: 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := EvaluatePricingTimeSchedule(schedule, tt.at)
			if err != nil {
				t.Fatal(err)
			}
			if got.Matched != tt.matched || got.Multiplier != tt.multiplier {
				t.Fatalf("got multiplier=%f matched=%v, want multiplier=%f matched=%v", got.Multiplier, got.Matched, tt.multiplier, tt.matched)
			}
		})
	}
}

func TestEvaluatePricingTimeScheduleTimezone(t *testing.T) {
	schedule := &PricingTimeSchedule{
		Timezone: "Asia/Shanghai",
		Calendar: PricingScheduleCalendarISOWeekday,
		Rules: []PricingTimeRule{
			{Days: []string{"monday"}, StartTime: "09:00", EndTime: "10:00", Multiplier: 2},
		},
	}

	at, err := time.Parse(time.RFC3339, "2026-08-24T01:30:00Z")
	if err != nil {
		t.Fatal(err)
	}
	got, err := EvaluatePricingTimeSchedule(schedule, at)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Matched || got.Multiplier != 2 {
		t.Fatalf("expected Shanghai 09:30 to match, got %+v", got)
	}
}

func TestEvaluatePricingTimeScheduleNilUsesBaseMultiplier(t *testing.T) {
	got, err := EvaluatePricingTimeSchedule(nil, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if got.Matched || got.Multiplier != 1 {
		t.Fatalf("expected base multiplier, got %+v", got)
	}
}

func TestValidatePricingTimeSchedule(t *testing.T) {
	valid := &PricingTimeSchedule{
		Timezone: "UTC",
		Calendar: PricingScheduleCalendarISOWeekday,
		Rules: []PricingTimeRule{
			{Days: []string{"saturday", "sunday"}, StartTime: "00:00", EndTime: "00:00", Multiplier: 0.5},
			{Days: []string{"monday"}, StartTime: "09:00", EndTime: "10:00", Multiplier: 2},
		},
	}
	if err := ValidatePricingTimeSchedule(valid); err != nil {
		t.Fatalf("expected valid schedule: %v", err)
	}

	tests := []struct {
		name     string
		schedule PricingTimeSchedule
	}{
		{name: "invalid calendar", schedule: PricingTimeSchedule{Timezone: "UTC", Calendar: "work_day", Rules: []PricingTimeRule{{Multiplier: 1}}}},
		{name: "invalid timezone", schedule: PricingTimeSchedule{Timezone: "not-a-zone", Calendar: PricingScheduleCalendarNone, Rules: []PricingTimeRule{{Multiplier: 1}}}},
		{name: "empty rules", schedule: PricingTimeSchedule{Timezone: "UTC", Calendar: PricingScheduleCalendarNone}},
		{name: "invalid multiplier", schedule: PricingTimeSchedule{Timezone: "UTC", Calendar: PricingScheduleCalendarNone, Rules: []PricingTimeRule{{Multiplier: 0}}}},
		{name: "invalid start", schedule: PricingTimeSchedule{Timezone: "UTC", Calendar: PricingScheduleCalendarNone, Rules: []PricingTimeRule{{StartTime: "24:00", EndTime: "01:00", Multiplier: 1}}}},
		{name: "invalid end", schedule: PricingTimeSchedule{Timezone: "UTC", Calendar: PricingScheduleCalendarNone, Rules: []PricingTimeRule{{StartTime: "00:00", EndTime: "1:00", Multiplier: 1}}}},
		{
			name: "overlap same day",
			schedule: PricingTimeSchedule{
				Timezone: "UTC", Calendar: PricingScheduleCalendarISOWeekday,
				Rules: []PricingTimeRule{
					{StartTime: "09:00", EndTime: "10:00", Multiplier: 1},
					{StartTime: "09:30", EndTime: "10:30", Multiplier: 1},
				},
			},
		},
		{
			name: "full day overlap",
			schedule: PricingTimeSchedule{
				Timezone: "UTC", Calendar: PricingScheduleCalendarISOWeekday,
				Rules: []PricingTimeRule{
					{Days: []string{"monday"}, StartTime: "00:00", EndTime: "00:00", Multiplier: 1},
					{StartTime: "09:00", EndTime: "10:00", Multiplier: 1},
				},
			},
		},
		{
			name: "cross midnight overlap",
			schedule: PricingTimeSchedule{
				Timezone: "UTC", Calendar: PricingScheduleCalendarISOWeekday,
				Rules: []PricingTimeRule{
					{Days: []string{"monday"}, StartTime: "22:00", EndTime: "02:00", Multiplier: 1},
					{Days: []string{"tuesday"}, StartTime: "00:00", EndTime: "03:00", Multiplier: 1},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := ValidatePricingTimeSchedule(&tt.schedule); err == nil {
				t.Fatal("expected invalid schedule")
			}
		})
	}
}

func TestValidatePricingTimeScheduleAllowsAdjacentWeekdayWindows(t *testing.T) {
	schedule := &PricingTimeSchedule{
		Timezone: "UTC",
		Calendar: PricingScheduleCalendarISOWeekday,
		Rules: []PricingTimeRule{
			{Days: []string{"saturday", "sunday"}, StartTime: "00:00", EndTime: "00:00", Multiplier: 0.5},
			{Days: []string{"monday", "tuesday", "wednesday", "thursday", "friday"}, StartTime: "16:30", EndTime: "23:59", Multiplier: 0.5},
		},
	}
	if err := ValidatePricingTimeSchedule(schedule); err != nil {
		t.Fatalf("expected valid weekday schedule: %v", err)
	}
}
