package legacy

import "testing"

func TestNormalizePoolModeRetryStatusCodesSortsAndDeduplicates(t *testing.T) {
	got, err := NormalizePoolModeRetryStatusCodes([]int{429, 401, 429, 403})
	if err != nil {
		t.Fatal(err)
	}
	want := []int{401, 403, 429}
	if len(got) != len(want) {
		t.Fatalf("normalized length = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("normalized = %v, want %v", got, want)
		}
	}
	if _, err := NormalizePoolModeRetryStatusCodes([]int{99}); err == nil {
		t.Fatal("invalid status code should fail")
	}
}
