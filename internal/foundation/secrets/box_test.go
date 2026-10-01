package secrets

import (
	"bytes"
	"testing"
)

func TestBoxBindsSecretToOwnerAndPurpose(t *testing.T) {
	box, err := New(bytes.Repeat([]byte{1}, 32))
	if err != nil {
		t.Fatal(err)
	}
	encrypted, err := box.Seal("tenant-a/invitation", []byte("sensitive-token"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encrypted, []byte("sensitive-token")) {
		t.Fatal("plaintext ciphertext")
	}
	if _, err := box.Open("tenant-b/invitation", encrypted); err == nil {
		t.Fatal("cross-owner decryption")
	}
	if _, err := box.Open("tenant-a/provider", encrypted); err == nil {
		t.Fatal("cross-purpose decryption")
	}
	got, err := box.Open("tenant-a/invitation", encrypted)
	if err != nil || string(got) != "sensitive-token" {
		t.Fatal(string(got), err)
	}
	encrypted[len(encrypted)-1] ^= 1
	if _, err := box.Open("tenant-a/invitation", encrypted); err == nil {
		t.Fatal("modified secret accepted")
	}
	if _, err := New([]byte("short")); err == nil {
		t.Fatal("short key accepted")
	}
}
