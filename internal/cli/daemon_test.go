package cli

import "testing"

func TestValidateListenAddress(t *testing.T) {
	tests := []struct {
		name      string
		addr      string
		hasAPIKey bool
		wantErr   bool
	}{
		{name: "loopback ip without key", addr: "127.0.0.1:24085", hasAPIKey: false, wantErr: false},
		{name: "localhost without key", addr: "localhost:24085", hasAPIKey: false, wantErr: false},
		{name: "ipv6 loopback without key", addr: "[::1]:24085", hasAPIKey: false, wantErr: false},
		{name: "specific lan ip without key", addr: "192.168.20.21:24085", hasAPIKey: false, wantErr: true},
		{name: "wildcard without key", addr: "0.0.0.0:24085", hasAPIKey: false, wantErr: true},
		{name: "empty host without key", addr: ":24085", hasAPIKey: false, wantErr: true},
		{name: "specific lan ip with key", addr: "192.168.20.21:24085", hasAPIKey: true, wantErr: false},
		{name: "wildcard with key", addr: "0.0.0.0:24085", hasAPIKey: true, wantErr: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateListenAddress(tt.addr, tt.hasAPIKey)
			if (err != nil) != tt.wantErr {
				t.Fatalf("validateListenAddress() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
