package httpapi

import (
	"testing"
	"time"
)

func TestScheduleNext(t *testing.T) {
	from := time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC)
	next, err := scheduleNext("cron", "*/15 * * * *", "Asia/Tokyo", from)
	if err != nil {
		t.Fatal(err)
	}
	if !next.After(from) {
		t.Fatalf("next=%v", next)
	}
	once := "2026-09-03T12:00:00+09:00"
	n, err := scheduleNext("once", once, "Asia/Tokyo", from)
	if err != nil {
		t.Fatal(err)
	}
	if n.Format(time.RFC3339) != once {
		t.Fatalf("once=%s", n.Format(time.RFC3339))
	}
}
func TestScheduleRejectsPastOnce(t *testing.T) {
	if _, err := scheduleNext("once", "2020-01-01T00:00:00Z", "UTC", time.Now()); err == nil {
		t.Fatal("expected past-time error")
	}
}
