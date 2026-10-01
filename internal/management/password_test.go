package management

import "testing"

func TestPasswordHash(t *testing.T) {
	hash, err := HashPassword("long enough password")
	if err != nil {
		t.Fatal(err)
	}
	if !VerifyPassword(hash, "long enough password") || VerifyPassword(hash, "wrong password") {
		t.Fatal("password verification")
	}
	if VerifyPassword("$argon2id$v=19$m=999999999,t=9999,p=255$bad$bad", "any") {
		t.Fatal("unbounded hash parameters")
	}
	if _, err := HashPassword("short"); err == nil {
		t.Fatal("short password accepted")
	}
}
