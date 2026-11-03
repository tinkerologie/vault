package credentials

import (
	"strings"
	"testing"
)

var testKey = []byte("0123456789abcdef0123456789abcdef")

// testToken has the exact shape of a sealed value but opens with no key here.
var testToken = sealedPrefix + strings.Repeat("A", 40)

func TestSealOpenRoundTripAndIdempotence(t *testing.T) {
	t.Parallel()
	in := `{"host":"nas","username":"u","password":"s3cret","access_key":"AKIA","url":"https://bob:pw@dav.example/x","n":1234567890123456789}`
	sealed, err := Seal(testKey, in)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"s3cret", "AKIA", "bob:pw"} {
		if strings.Contains(sealed, secret) {
			t.Fatalf("%q left in sealed config: %s", secret, sealed)
		}
	}
	if !strings.Contains(sealed, `"host":"nas"`) || !strings.Contains(sealed, "1234567890123456789") {
		t.Fatalf("non-secret fields changed: %s", sealed)
	}
	again, err := Seal(testKey, sealed)
	if err != nil || again != sealed {
		t.Fatalf("second seal changed the value (err %v)", err)
	}
	opened := Open(testKey, sealed)
	if !strings.Contains(opened, `"password":"s3cret"`) || !strings.Contains(opened, "bob:pw") || IsLocked(opened) {
		t.Fatalf("opened = %s", opened)
	}
}

func TestSealEdgeCases(t *testing.T) {
	t.Parallel()
	if got, _ := Seal(nil, `{"password":"p"}`); got != `{"password":"p"}` {
		t.Errorf("no key should leave the config alone: %q", got)
	}
	if got, _ := Seal(testKey, `{"password":""}`); got != `{"password":""}` {
		t.Errorf("empty secret changed: %q", got)
	}
	if got, _ := Seal(nil, `{"password":"`+RedactionMarker+`","host":"h"}`); strings.Contains(got, RedactionMarker) {
		t.Errorf("marker must never be stored: %q", got)
	}
	if _, err := Seal([]byte("short"), `{"password":"p"}`); err == nil {
		t.Error("bad key should fail rather than store plaintext")
	}
}

func TestOpenFailsClosedPerField(t *testing.T) {
	t.Parallel()
	sealed, err := Seal(testKey, `{"host":"nas","password":"s3cret"}`)
	if err != nil {
		t.Fatal(err)
	}
	other := []byte("ffffffffffffffffffffffffffffffff")
	for name, opened := range map[string]string{"wrong key": Open(other, sealed), "no key": Open(nil, sealed)} {
		if !IsLocked(opened) {
			t.Errorf("%s: not locked: %s", name, opened)
		}
		if !strings.Contains(opened, `"host":"nas"`) {
			t.Errorf("%s: lost non-secret field: %s", name, opened)
		}
		if Open(testKey, opened) != Open(testKey, sealed) {
			t.Errorf("%s: original key no longer opens it", name)
		}
	}
	if IsLocked(`{"password":"plain"}`) {
		t.Error("plaintext is not locked")
	}
	if got := Open(testKey, `{bad `+sealedPrefix); got != `{bad `+sealedPrefix {
		t.Errorf("malformed input changed: %q", got)
	}
	if IsLocked(`{"note":"mentions ` + sealedPrefix + ` inside"}`) {
		t.Error("a value merely containing the prefix is not sealed")
	}
}

func TestMask(t *testing.T) {
	t.Parallel()
	got := Mask(`{"password":"p","api_key":"k","token":"t","access_key":"AKIA","password2":"","url":"https://bob:pw@dav.example/x",` +
		`"nested":{"secret_key":"s","list":["ftp://a:b@h/",{"private_key":"pk"}]},"sealed":"` + testToken + `"}`)
	for _, leak := range []string{`"p"`, `"k"`, `"t"`, "bob", "pw", `"s"`, "a:b", "pk", sealedPrefix} {
		if strings.Contains(got, leak) {
			t.Errorf("%q leaked in %s", leak, got)
		}
	}
	if !strings.Contains(got, "https://dav.example/x") || !strings.Contains(got, "AKIA") {
		t.Errorf("visible fields lost: %s", got)
	}
	if Mask(`{"password":"s3cret"`) != "{}" {
		t.Error("malformed config must not pass through")
	}
	if StripUserinfo("http://a:b@[::1") != "" {
		t.Error("unparseable credentialed URL must be dropped")
	}
	if StripUserinfo("mail me@example") != "mail me@example" {
		t.Error("plain text changed")
	}
}

func TestPreserve(t *testing.T) {
	t.Parallel()
	existing := `{"password":"real","url":"https://bob:pw@dav.example/x","sealed_user":"` + testToken + `","host":"h"}`
	got := Preserve(`{"password":"`+RedactionMarker+`","url":"https://dav.example/x","sealed_user":"`+RedactionMarker+`","host":"`+RedactionMarker+`"}`, existing)
	for _, want := range []string{`"password":"real"`, "bob:pw", `"sealed_user":"` + testToken + `"`, `"host":"` + RedactionMarker + `"`} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %s", want, got)
		}
	}
	if got := Preserve(`{"password":"new"}`, existing); got != `{"password":"new"}` {
		t.Errorf("new secret must replace: %s", got)
	}
	if got := Preserve(`{"url":"https://elsewhere/x"}`, existing); strings.Contains(got, "bob") {
		t.Errorf("stored URL credentials must not follow a changed URL: %s", got)
	}
	for _, tc := range [][2]string{{"not json", existing}, {`{"password":"` + RedactionMarker + `"}`, "not json"}} {
		if got := Preserve(tc[0], tc[1]); got != tc[0] {
			t.Errorf("Preserve(%q) = %q", tc[0], got)
		}
	}
}

func TestIsPlaintextRemote(t *testing.T) {
	t.Parallel()
	for url, want := range map[string]bool{
		"http://192.0.2.5:24085": true,
		"HTTP://nas.lan":         true,
		"http://127.0.0.1:24085": false,
		"http://[::1]:24085":     false,
		"http://localhost:24085": false,
		"https://nas.lan":        false,
		"://bad":                 false,
	} {
		if got := IsPlaintextRemote(url); got != want {
			t.Errorf("IsPlaintextRemote(%q) = %v", url, got)
		}
	}
}

func TestMergeStorageEdit(t *testing.T) {
	t.Parallel()
	m := RedactionMarker
	tests := []struct {
		name, typ, stored, edit string
		wantErr                 bool
	}{
		{"sftp same", "sftp", `{"host":"a","password":"p"}`, `{"host":"A ","port":22,"user":"new","password":"` + m + `"}`, false},
		{"sftp host", "sftp", `{"host":"a","password":"p"}`, `{"host":"evil","password":"` + m + `"}`, true},
		{"sftp port", "sftp", `{"host":"a","port":22,"password":"p"}`, `{"host":"a","port":2222,"password":"` + m + `"}`, true},
		{"sftp key file", "sftp", `{"host":"a","key_file":"/k"}`, `{"host":"evil","key_file":"` + m + `"}`, true},
		{"sftp host key", "sftp", `{"host":"a","host_key":"x","password":"p"}`, `{"host":"a","host_key":"y","password":"` + m + `"}`, true},
		{"sftp retyped", "sftp", `{"host":"a","password":"p"}`, `{"host":"evil","password":"typed"}`, false},
		{"smb host", "smb", `{"host":"a","password":"p"}`, `{"host":"evil","password":"` + m + `"}`, true},
		{"smb share same server", "smb", `{"host":"a","share":"s","password":"p"}`, `{"host":"a","share":"t","password":"` + m + `"}`, false},
		{"webdav host", "webdav", `{"url":"https://a/x","password":"p"}`, `{"url":"https://evil/x","password":"` + m + `"}`, true},
		{"webdav downgrade", "webdav", `{"url":"https://a/x","password":"p"}`, `{"url":"http://a/x","password":"` + m + `"}`, true},
		{"webdav verify off", "webdav", `{"url":"https://a","password":"p"}`, `{"url":"https://a","insecure_skip_verify":true,"password":"` + m + `"}`, true},
		{"webdav path", "webdav", `{"url":"https://a/x","password":"p"}`, `{"url":"https://a:443/y","insecure_skip_verify":false,"password":"` + m + `"}`, false},
		{"webdav bad url", "webdav", `{"url":"https://a","password":"p"}`, `{"url":"http://[bad","password":"` + m + `"}`, true},
		{"webdav url creds stay", "webdav", `{"url":"https://b:c@a/x"}`, `{"url":"https://a/x"}`, false},
		{"webdav url login, same server path", "webdav", `{"url":"https://b:c@a/x"}`, `{"url":"https://a/y"}`, false},
		{"webdav url login, verification off", "webdav", `{"url":"https://b:c@a/x"}`, `{"url":"https://a/x","insecure_skip_verify":true}`, true},
		{"webdav url login, new server", "webdav", `{"url":"https://b:c@a/x"}`, `{"url":"https://evil/x"}`, true},
		{"webdav url login, new server with typed password", "webdav", `{"url":"https://b:c@a/x"}`, `{"url":"https://new/x","username":"b","password":"typed"}`, false},
		{"capitalized host", "smb", `{"host":"a","password":"p"}`, `{"HOST":"evil","password":"` + m + `"}`, true},
		{"second spelling of host", "smb", `{"host":"a","password":"p"}`, `{"host":"a","HOST":"evil","password":"` + m + `"}`, true},
		{"capitalized stored host", "smb", `{"Host":"a","password":"p"}`, `{"host":"a","Password":"` + m + `"}`, false},
		{"capitalized url login", "webdav", `{"URL":"https://b:c@a/x"}`, `{"url":"https://evil/x"}`, true},
		{"unknown type nested values", "new", `{"opts":{"a":1},"password":"p"}`, `{"opts":{"a":2},"password":"` + m + `"}`, true},
		{"unknown type nested same", "new", `{"opts":{"a":[1]},"password":"p"}`, `{"opts":{"a":[1]},"password":"` + m + `"}`, false},
		{"unknown type removed field", "new", `{"server":"a","password":"p"}`, `{"password":"` + m + `"}`, true},
		{"s3 endpoint", "s3", `{"endpoint":"https://a","bucket":"b","secret_key":"s"}`, `{"endpoint":"https://evil","bucket":"b","secret_key":"` + m + `"}`, true},
		{"s3 bucket", "s3", `{"bucket":"b","secret_key":"s"}`, `{"bucket":"evil","secret_key":"` + m + `"}`, true},
		{"s3 region", "s3", `{"bucket":"b","region":"r","secret_key":"s"}`, `{"bucket":"b","region":"evil","secret_key":"` + m + `"}`, true},
		{"s3 prefix", "s3", `{"bucket":"b","secret_key":"s"}`, `{"bucket":"b","base_path":"x","secret_key":"` + m + `"}`, false},
		{"nfs no secret", "nfs", `{"host":"a"}`, `{"host":"b"}`, false},
		{"unknown type", "new", `{"server":"a","password":"p"}`, `{"server":"evil","password":"` + m + `"}`, true},
		{"unknown same", "new", `{"server":"a","password":"p"}`, `{"server":"a","password":"` + m + `"}`, false},
		{"marker with nothing stored", "sftp", `{"host":"a"}`, `{"host":"evil","password":"` + m + `"}`, false},
		{"locked secret", "sftp", `{"host":"a","password":"` + testToken + `"}`, `{"host":"evil","password":"` + m + `"}`, true},
		{"masked sealed url", "webdav", `{"url":"` + testToken + `"}`, `{"url":"` + m + `"}`, false},
		{"not json", "sftp", `{"host":"a"}`, `nope`, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := MergeStorageEdit(tt.typ, tt.typ, tt.edit, tt.stored)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("stored secret would follow the edit: %s", got)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(got, RedactionMarker) && strings.Contains(tt.stored, `"password":"p"`) {
				t.Fatalf("marker not resolved: %s", got)
			}
		})
	}
	if _, err := MergeStorageEdit("sftp", "smb", `{"host":"a","password":"`+m+`"}`, `{"host":"a","password":"p"}`); err == nil {
		t.Fatal("type change must not reuse a stored secret")
	}
	if !HasMarker(`{"password":"`+m+`"}`) || HasMarker(`{"password":"p"}`) || HasMarker(`nope`) {
		t.Fatal("HasMarker")
	}
}

func TestParseObjectRejectsTrailingData(t *testing.T) {
	t.Parallel()
	for _, in := range []string{`{"a":1}garbage`, `{"a":1}{"b":2}`, `{"a":1}}`, `{"a":1}]`, `[]`, `null`, ``} {
		if _, ok := parseObject(in); ok {
			t.Errorf("parseObject(%q) accepted", in)
		}
	}
	if _, ok := parseObject(" {\"a\":1}\n"); !ok {
		t.Error("surrounding whitespace rejected")
	}
}

func TestSealProtectsMalformedConfigs(t *testing.T) {
	t.Parallel()
	const secret = `{"password":"hunter2"}trailing`
	sealed, err := Seal(testKey, secret)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(sealed, "hunter2") {
		t.Fatalf("malformed config stored in plaintext: %s", sealed)
	}
	if again, _ := Seal(testKey, sealed); again != sealed {
		t.Fatal("not idempotent")
	}
	if Open(testKey, sealed) != secret {
		t.Fatal("did not round trip")
	}
	other := []byte("ffffffffffffffffffffffffffffffff")
	if !IsLocked(Open(other, sealed)) || !IsLocked(Open(nil, sealed)) {
		t.Fatal("whole-config seal must lock without its key")
	}
	if got, _ := Seal(nil, secret); got != secret {
		t.Fatal("no key leaves it alone")
	}
	if _, err := Seal([]byte("short"), secret); err == nil {
		t.Fatal("bad key must fail")
	}
	if got := Mask(sealed); got != "{}" {
		t.Fatalf("mask = %s", got)
	}
}

func TestPrefixLookalikeIsSealedLikeAnyPassword(t *testing.T) {
	t.Parallel()
	for _, literal := range []string{sealedPrefix + "hunter2", sealedPrefix, sealedPrefix + "AAAA"} {
		sealed, err := Seal(testKey, `{"password":"`+literal+`"}`)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(sealed, literal+`"`) {
			t.Fatalf("%q left in plaintext: %s", literal, sealed)
		}
		opened := Open(testKey, sealed)
		if !strings.Contains(opened, literal) || IsLocked(opened) {
			t.Fatalf("%q did not round trip: %s", literal, opened)
		}
	}
}

func TestSecretsInUnexpectedShapes(t *testing.T) {
	t.Parallel()
	sealed, err := Seal(testKey, `{"password":{"value":"hunter2"},"token":["hunter3"],"port":22}`)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(sealed, "hunter") {
		t.Fatalf("nested secret stored in plaintext: %s", sealed)
	}
	masked := Mask(`{"password":{"value":"hunter2"},"token":["hunter3"],"api_key":7}`)
	if strings.Contains(masked, "hunter") {
		t.Fatalf("nested secret shown: %s", masked)
	}
}

func TestPreserveCarriesURLLoginOnSameServerOnly(t *testing.T) {
	t.Parallel()
	stored := `{"url":"https://bob:pw@dav.example/old"}`
	if got := Preserve(`{"url":"https://dav.example/new"}`, stored); !strings.Contains(got, "bob:pw@dav.example/new") {
		t.Errorf("path edit lost the login: %s", got)
	}
	if got := Preserve(`{"url":"https://other.example/new"}`, stored); strings.Contains(got, "bob") {
		t.Errorf("login followed to another server: %s", got)
	}
	if got := Preserve(`{"url":"http://dav.example/old"}`, stored); strings.Contains(got, "bob") {
		t.Errorf("login followed a downgrade: %s", got)
	}
	if got := Preserve(`{"url":"https://amy:new@dav.example/x"}`, stored); strings.Contains(got, "bob") {
		t.Errorf("typed login replaced: %s", got)
	}
}

func TestLockedForValidation(t *testing.T) {
	t.Parallel()
	got := LockedForValidation(`{"host":"a","password":"` + testToken + `"}`)
	if IsLocked(got) || !strings.Contains(got, `"host":"a"`) {
		t.Fatalf("got %s", got)
	}
	if LockedForValidation("nope") != "nope" {
		t.Fatal("malformed input changed")
	}
}

func TestSecretNamesIgnoreCase(t *testing.T) {
	t.Parallel()
	in := `{"host":"nas","Password":"hunter2","API_KEY":"hunter3","Access_Key":"hunter4"}`
	sealed, err := Seal(testKey, in)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(sealed, "hunter") {
		t.Fatalf("capitalized secret stored in plaintext: %s", sealed)
	}
	if opened := Open(testKey, sealed); !strings.Contains(opened, `"Password":"hunter2"`) {
		t.Fatalf("opened = %s", opened)
	}
	masked := Mask(in)
	if strings.Contains(masked, "hunter2") || strings.Contains(masked, "hunter3") {
		t.Fatalf("capitalized secret shown: %s", masked)
	}
	got := Preserve(`{"password":"`+RedactionMarker+`"}`, `{"Password":"hunter2"}`)
	if !strings.Contains(got, "hunter2") {
		t.Fatalf("stored secret not found under another spelling: %s", got)
	}
}
