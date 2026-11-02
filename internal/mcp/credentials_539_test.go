package mcpserver

import (
	"context"
	"strconv"
	"strings"
	"testing"

	"github.com/tinkerologie/vault/internal/credentials"
	"github.com/tinkerologie/vault/internal/db"
)

func TestMCPReadsMaskCredentials(t *testing.T) {
	session, d := setupTest(t)
	ctx := context.Background()
	destID, err := d.CreateStorageDestination(db.StorageDestination{Name: "dav", Type: "webdav",
		Config: `{"url":"https://bob:urlpass@dav.example/","password":"dest-secret"}`})
	if err != nil {
		t.Fatal(err)
	}
	srcID, err := d.CreateReplicationSource(db.ReplicationSource{Name: "peer", Type: "remote_vault", URL: "http://peer", Config: `{"api_key":"peer-secret"}`})
	if err != nil {
		t.Fatal(err)
	}
	calls := map[string]map[string]any{
		"list_storage":     {},
		"get_storage":      {"id": destID},
		"list_replication": {},
		"get_replication":  {"id": srcID},
	}
	for name, args := range calls {
		got := callTool(t, session, ctx, name, args)
		for _, leak := range []string{"dest-secret", "peer-secret", "urlpass", "bob"} {
			if strings.Contains(got, leak) {
				t.Errorf("%s leaked %q: %s", name, leak, got)
			}
		}
	}
}

func TestMCPUpdateStorageKeepsMaskedSecret(t *testing.T) {
	session, d := setupTest(t)
	id, err := d.CreateStorageDestination(db.StorageDestination{Name: "s", Type: "smb",
		Config: `{"host":"a","share":"b","username":"u","password":"dest-secret"}`})
	if err != nil {
		t.Fatal(err)
	}
	cfg := `{"host":"a","share":"c","username":"u","password":"` + credentials.RedactionMarker + `"}`
	r := callToolRaw(t, session, context.Background(), "update_storage", map[string]any{"id": id, "config": cfg})
	if r.IsError {
		t.Fatalf("update %s: %+v", strconv.FormatInt(id, 10), r.Content)
	}
	dest, err := d.GetStorageDestination(id)
	if err != nil || !strings.Contains(dest.Config, "dest-secret") || !strings.Contains(dest.Config, `"share":"c"`) {
		t.Fatalf("config = %s (err %v)", dest.Config, err)
	}
}

func TestMCPUpdateStorageRefusesMaskedSecretOnNewHost(t *testing.T) {
	session, d := setupTest(t)
	ctx := context.Background()
	id, err := d.CreateStorageDestination(db.StorageDestination{Name: "s", Type: "smb",
		Config: `{"host":"a","share":"b","user":"u","password":"dest-secret"}`})
	if err != nil {
		t.Fatal(err)
	}
	cfg := `{"host":"evil","share":"b","user":"u","password":"` + credentials.RedactionMarker + `"}`
	if r := callToolRaw(t, session, ctx, "update_storage", map[string]any{"id": id, "config": cfg}); !r.IsError {
		t.Fatal("masked secret followed a new host")
	}
	if r := callToolRaw(t, session, ctx, "update_storage", map[string]any{"id": id, "type": "sftp"}); !r.IsError {
		t.Fatal("type change reused the stored secret")
	}
	dest, err := d.GetStorageDestination(id)
	if err != nil || dest.Type != "smb" || !strings.Contains(dest.Config, `"host":"a"`) {
		t.Fatalf("refused edit saved: %+v (err %v)", dest, err)
	}
	if r := callToolRaw(t, session, ctx, "update_storage", map[string]any{"id": id,
		"config": `{"host":"new","share":"b","user":"u","password":"typed"}`}); r.IsError {
		t.Fatalf("retyped secret refused: %+v", r.Content)
	}
}
