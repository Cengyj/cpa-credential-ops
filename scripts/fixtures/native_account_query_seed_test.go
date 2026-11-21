package login

import (
	"os"
	"testing"
	"time"

	bolt "go.etcd.io/bbolt"
)

// This template runs only in the isolated fixture volume, before CPA starts.
func TestSeedNativeAccountQuery(t *testing.T) {
	const (
		mount     = "/query-storage"
		dir       = mount + "/accounts"
		accountID = "qa-readonly-account"
		jobID     = "qa-readonly-history-job"
		command   = "qa-readonly-account-command"
		email     = "readonly-account@example.invalid"
	)
	parent, err := os.Lstat(mount)
	if err != nil || !parent.IsDir() {
		t.Fatal("isolated fixture volume is missing")
	}
	// Reserve a new path atomically. Never open an existing account database.
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatalf("requires an unused fixture directory: %v", err)
	}
	j, err := openJournal(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := j.db.Close(); err != nil {
			t.Error(err)
		}
	})
	now := time.Now().UTC()
	created := now.Add(-time.Minute)
	job := storedJob{
		View: Job{
			ID: jobID, AccountID: accountID, Revision: 3, Method: "password_totp", Intent: "import",
			Status: "cancelled", MaskedEmail: Mask(email), PlanType: "unknown", CreatedAt: created.Format(time.RFC3339Nano),
		},
		Created: created, Updated: now, AccountSequence: 1,
	}
	entry := storedAccount{
		View: Account{
			ID: accountID, Revision: 1, Label: "Synthetic query fixture", MaskedEmail: Mask(email), PlanType: "unknown",
			LastJobID: jobID, LastJobStatus: job.View.Status, HealthStatus: "unbound",
			NextCheckAt: now.Add(-30 * time.Minute).Format(time.RFC3339), AutoRelogin: false,
		},
		EmailHash: j.fingerprint("account-email", email), SecretRevision: 1,
		Credentials: Credentials{Email: email, Password: "fixture-query-only-password", TOTPSecret: "KJCU6TCBFVGU6RCS"},
	}
	body, err := canonicalRequest([]byte(`{"id":"qa-readonly-account","revision":1,"upload_to_cpa":false}`))
	if err != nil {
		t.Fatal(err)
	}
	response, err := Encode(job.View)
	if err != nil {
		t.Fatal(err)
	}
	record := idempotency{Fingerprint: j.fingerprint("POST/accounts/start", body), JobPath: "/accounts/start", JobID: jobID, Created: created, Response: response}
	settings := AccountSettings{Revision: 1, InspectionEnabled: true, IntervalMinutes: 15, AutoReloginEnabled: false}
	if err := j.db.Update(func(tx *bolt.Tx) error {
		if err := putGob(tx, "meta", "account-settings", settings); err != nil {
			return err
		}
		if err := putGob(tx, "accounts", accountID, entry); err != nil {
			return err
		}
		if err := j.saveTx(tx, job); err != nil {
			return err
		}
		return putGob(tx, "idempotency", j.fingerprint(command), record)
	}); err != nil {
		t.Fatal(err)
	}
}
