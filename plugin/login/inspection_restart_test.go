package login

import (
	"testing"
	"testing/synctest"
	"time"
)

func TestInspectionReopensWithoutBrowserOrManagementSession(t *testing.T) {
	opts := offlineOptions(t)
	synctest.Test(t, func(t *testing.T) {
		first, err := New(opts)
		if err != nil {
			t.Fatal(err)
		}
		defer first.Close()
		account := importOfflineAccount(t, first)
		settings, err := first.AccountSettings()
		if err != nil {
			t.Fatal(err)
		}
		settings.IntervalMinutes, settings.InspectionEnabled = 5, true
		if _, err = first.UpdateAccountSettings(settings); err != nil {
			t.Fatal(err)
		}
		if err = first.Close(); err != nil {
			t.Fatal(err)
		}
		time.Sleep(20 * time.Minute)
		restored, err := New(opts)
		if err != nil {
			t.Fatal(err)
		}
		defer restored.Close()
		time.Sleep(time.Second)
		synctest.Wait()
		restored.accountMu.Lock()
		seen := restored.accounts[account.ID].View
		restored.accountMu.Unlock()
		if seen.LastCheckedAt == "" || seen.HealthStatus != "not_linked" || seen.LastJobID != "" {
			t.Fatal("background inspection missing or started a login")
		}
		time.Sleep(4 * time.Minute)
		synctest.Wait()
		restored.accountMu.Lock()
		later := restored.accounts[account.ID].View
		restored.accountMu.Unlock()
		if later.Revision != seen.Revision {
			t.Fatal("inspection repeated ahead of its scheduled interval")
		}
		t.Log("one overdue account inspection; zero browser reads, management HTTP requests and login jobs")
	})
}
