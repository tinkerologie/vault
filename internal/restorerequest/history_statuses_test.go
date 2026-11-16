package restorerequest

import (
	"os"
	"strings"
	"testing"
)

// The operator history page requests every history status by name. A status
// added on one side only would silently drop requests from the history.
func TestHistoryStatusesMatchTheOperatorHistoryPage(t *testing.T) {
	page, err := os.ReadFile("../../web/src/components/RestoreRequestList.svelte")
	if err != nil {
		t.Fatal(err)
	}
	want := "'" + strings.Join(HistoryStatuses, ",") + "'"
	if !strings.Contains(string(page), "status: history ? "+want+" : 'pending'") {
		t.Fatalf("RestoreRequestList.svelte does not request the history statuses %s", want)
	}
}
