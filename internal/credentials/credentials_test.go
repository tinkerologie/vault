package credentials

import (
	"strings"
	"testing"
)

var testKey = []byte("0123456789abcdef0123456789abcdef")

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
	if got, _ := Seal(testKey, "not json"); got != "not json" {
		t.Errorf("non-JSON changed: %q", got)
	}
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
	if !IsLocked(`{"note":"` + sealedPrefix + `"`) {
		t.Error("malformed config holding a sealed value must count as locked")
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
		`"nested":{"secret_key":"s","list":["ftp://a:b@h/",{"private_key":"pk"}]},"sealed":"` + sealedPrefix + `abc"}`)
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
	existing := `{"password":"real","url":"https://bob:pw@dav.example/x","sealed_user":"` + sealedPrefix + `abc","host":"h"}`
	got := Preserve(`{"password":"`+RedactionMarker+`","url":"https://dav.example/x","sealed_user":"`+RedactionMarker+`","host":"`+RedactionMarker+`"}`, existing)
	for _, want := range []string{`"password":"real"`, "bob:pw", `"sealed_user":"` + sealedPrefix + `abc"`, `"host":"` + RedactionMarker + `"`} {
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
		{"s3 endpoint", "s3", `{"endpoint":"https://a","bucket":"b","secret_key":"s"}`, `{"endpoint":"https://evil","bucket":"b","secret_key":"` + m + `"}`, true},
		{"s3 bucket", "s3", `{"bucket":"b","secret_key":"s"}`, `{"bucket":"evil","secret_key":"` + m + `"}`, true},
		{"s3 region", "s3", `{"bucket":"b","region":"r","secret_key":"s"}`, `{"bucket":"b","region":"evil","secret_key":"` + m + `"}`, true},
		{"s3 prefix", "s3", `{"bucket":"b","secret_key":"s"}`, `{"bucket":"b","base_path":"x","secret_key":"` + m + `"}`, false},
		{"nfs no secret", "nfs", `{"host":"a"}`, `{"host":"b"}`, false},
		{"unknown type", "new", `{"server":"a","password":"p"}`, `{"server":"evil","password":"` + m + `"}`, true},
		{"unknown same", "new", `{"server":"a","password":"p"}`, `{"server":"a","password":"` + m + `"}`, false},
		{"marker with nothing stored", "sftp", `{"host":"a"}`, `{"host":"evil","password":"` + m + `"}`, false},
		{"locked secret", "sftp", `{"host":"a","password":"` + sealedPrefix + `x"}`, `{"host":"evil","password":"` + m + `"}`, true},
		{"masked sealed url", "webdav", `{"url":"` + sealedPrefix + `x"}`, `{"url":"` + m + `"}`, false},
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
