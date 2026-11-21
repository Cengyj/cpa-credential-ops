package login

import (
	"bytes"
	"encoding/gob"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/MoeForge/moego/ojson"
	bolt "go.etcd.io/bbolt"
)

type totpValidationCase struct {
	name  string
	value string
	valid bool
}

func totpValidationCases() []totpValidationCase {
	const seed = "JBSWY3DPEHPK3PXP"
	const uri = "otpauth://totp/OpenAI:fixture?secret=" + seed
	return []totpValidationCase{
		{"base32", seed, true},
		{"formatted", " jbsw-y3dp\tehpk 3pxp ==\n", true},
		{"unicode_whitespace", "\ufeff" + seed + "\u00a0\u2009", true},
		{"unicode_uppercase", "ßßßßßßßß", true},
		{"maximum", strings.Repeat("A", 128), true},
		{"tail_two", strings.Repeat("A", 18), true},
		{"tail_four", strings.Repeat("A", 20), true},
		{"tail_one", strings.Repeat("A", 21), true},
		{"tail_three", strings.Repeat("A", 23), true},
		{"uri_defaults", uri, true},
		{"uri_explicit", uri + "&algorithm=sha1&digits=6&period=30&issuer=OpenAI", true},
		{"uri_empty_defaults", uri + "&algorithm=&digits=&period=", true},
		{"uri_encoded", "otpauth://totp/fixture?secret=jbsw%20y3dp-ehpk3pxp", true},
		{"empty", "", false},
		{"code_instead_of_seed", "123456", false},
		{"short", strings.Repeat("A", 15), false},
		{"long", strings.Repeat("A", 129), false},
		{"invalid_character", strings.Repeat("A", 15) + "0", false},
		{"interior_padding", "JBSW=Y3DPEHPK3PXP", false},
		{"invalid_length_1", strings.Repeat("A", 17), false},
		{"invalid_length_3", strings.Repeat("A", 19), false},
		{"invalid_length_6", strings.Repeat("A", 22), false},
		{"nonzero_tail_2", strings.Repeat("A", 17) + "B", false},
		{"nonzero_tail_4", strings.Repeat("A", 19) + "B", false},
		{"nonzero_tail_1", strings.Repeat("A", 20) + "B", false},
		{"nonzero_tail_3", strings.Repeat("A", 22) + "B", false},
		{"non_worker_whitespace", seed + "\u0085", false},
		{"uri_hotp", strings.Replace(uri, "://totp/", "://hotp/", 1), false},
		{"uri_wrong_host_case", strings.Replace(uri, "://totp/", "://TOTP/", 1), false},
		{"uri_userinfo", strings.Replace(uri, "://totp/", "://name:secret@totp/", 1), false},
		{"uri_fragment", uri + "#fragment", false},
		{"uri_missing_secret", "otpauth://totp/fixture?issuer=OpenAI", false},
		{"uri_sha256", uri + "&algorithm=SHA256", false},
		{"uri_eight_digits", uri + "&digits=8", false},
		{"uri_sixty_seconds", uri + "&period=60", false},
		{"uri_duplicate_secret", uri + "&secret=" + seed, false},
		{"uri_duplicate_encoded_secret", uri + "&%73ecret=" + seed, false},
		{"uri_duplicate_algorithm", uri + "&algorithm=SHA1&algorithm=SHA1", false},
		{"uri_duplicate_digits", uri + "&digits=6&digits=6", false},
		{"uri_duplicate_period", uri + "&period=30&period=30", false},
	}
}

func TestTOTPValidationPreservesCredentialInput(t *testing.T) {
	for _, tc := range totpValidationCases() {
		t.Run(tc.name, func(t *testing.T) {
			input := Credentials{Email: "fixture@example.test", Password: "  opaque password\t ", TOTPSecret: tc.value}
			raw, err := Encode(input)
			if err != nil {
				t.Fatal(err)
			}
			got, err := DecodeCredentials(ojson.ParseBytes(raw))
			if tc.valid {
				if err != nil || got != input {
					t.Fatal("valid credentials were rejected or their original values changed")
				}
			} else if err == nil || err.Error() != "invalid_credentials" || got != (Credentials{}) {
				t.Fatal("invalid TOTP input did not return the fixed credential error")
			}
		})
	}
}

func TestTOTPValidationStartPayload(t *testing.T) {
	for _, remember := range []bool{false, true} {
		input := offlineStart().Credentials
		input.TOTPSecret = "123456"
		raw, err := Encode(map[string]any{"consumer_plugin": "model-quality", "method": "password_totp", "intent": "import", "credentials": input, "remember_credentials": remember})
		if err != nil {
			t.Fatal(err)
		}
		if _, err = DecodeStart(raw); err == nil || err.Error() != "invalid_credentials" {
			t.Fatal("temporary or remembered login accepted an invalid seed")
		}
	}
}

func TestTOTPValidationAccountImportAndEditAreAtomic(t *testing.T) {
	s := offlineService(t, offlineOptions(t))
	input := offlineStart().Credentials
	input.TOTPSecret = " jbsw-y3dp ehpk3pxp== "
	imported, err := s.ImportAccounts([]Credentials{input})
	if err != nil {
		t.Fatal(err)
	}
	account := imported[0]
	before := s.accounts[account.ID]
	stored, err := s.accountCredentials(before)
	if err != nil || stored != input {
		t.Fatal("stored credentials lost their original values")
	}
	valid := input
	valid.Email = "valid-second@example.test"
	invalid := input
	invalid.Email = "invalid-third@example.test"
	invalid.TOTPSecret = "123456"
	if _, err = s.ImportAccounts([]Credentials{valid, invalid}); err == nil || err.Error() != "invalid_credentials" {
		t.Fatal("invalid account batch was accepted")
	}
	invalid.Email = input.Email
	invalid.Password = "replacement must not be stored"
	if _, err = s.UpdateAccount(AccountUpdate{ID: account.ID, Revision: account.Revision, Label: "must not change", Credentials: &invalid}); err == nil || err.Error() != "invalid_credentials" {
		t.Fatal("invalid account edit was accepted")
	}
	if len(s.accounts) != 1 || !reflect.DeepEqual(before, s.accounts[account.ID]) {
		t.Fatal("rejected input changed in-memory credentials or revision")
	}
	if err = s.journal.db.View(func(tx *bolt.Tx) error {
		bucket := tx.Bucket([]byte("accounts"))
		if bucket.Stats().KeyN != 1 {
			t.Error("rejected batch partially persisted an account")
		}
		var persisted storedAccount
		if errLoad := gob.NewDecoder(bytes.NewReader(bucket.Get([]byte(account.ID)))).Decode(&persisted); errLoad != nil {
			return errLoad
		}
		if !reflect.DeepEqual(before, persisted) {
			t.Error("rejected edit changed persistent credentials or revision")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// This optional cross-language check is enabled explicitly for runtime releases;
// ordinary host tests need neither Node nor the separate login-runtime sources.
func TestTOTPValidationWorkerParity(t *testing.T) {
	node := os.Getenv("AUTHLOGIN_TOTP_NODE")
	if node == "" {
		t.Skip("set AUTHLOGIN_TOTP_NODE to check the installed JavaScript worker rules")
	}
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("test source path unavailable")
	}
	worker := filepath.Join(filepath.Dir(source), "..", "..", "login-runtime", "totp.mjs")
	cases := totpValidationCases()
	values := make([]string, len(cases))
	for i, tc := range cases {
		values[i] = tc.value
	}
	raw, err := Encode(values)
	if err != nil {
		t.Fatal(err)
	}
	const script = `import {readFileSync} from 'node:fs';
import {pathToFileURL} from 'node:url';
const {normalizeTotpSecret} = await import(pathToFileURL(process.argv[1]).href);
process.stdout.write(JSON.stringify(JSON.parse(readFileSync(0, 'utf8')).map(value => {
  try { normalizeTotpSecret(value); return true; } catch { return false; }
})));`
	cmd := exec.Command(node, "--input-type=module", "-e", script, worker)
	cmd.Stdin = bytes.NewReader(raw)
	out, err := cmd.Output()
	if err != nil {
		t.Fatal("JavaScript validation fixture failed")
	}
	results := ojson.ParseBytes(out)
	if !results.IsArray() || len(results.Array()) != len(cases) {
		t.Fatal("JavaScript validation fixture returned an invalid result")
	}
	for i, result := range results.Array() {
		if result.Bool() != cases[i].valid || validTOTPSecret(cases[i].value) != result.Bool() {
			t.Errorf("worker and host disagree for case %s", cases[i].name)
		}
	}
}
