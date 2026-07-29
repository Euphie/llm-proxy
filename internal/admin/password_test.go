package admin

import "testing"

// Break caught: returning a password unprotected, accepting the wrong password, or reusing a salt.
func TestHashPasswordRoundTrip(t *testing.T) {
	hash, err := HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	if hash == "correct horse battery staple" {
		t.Fatal("password was stored in plaintext")
	}
	if !VerifyPassword(hash, "correct horse battery staple") {
		t.Fatal("correct password rejected")
	}
	if VerifyPassword(hash, "wrong password") {
		t.Fatal("wrong password accepted")
	}
}

func TestHashPasswordUsesFreshSalt(t *testing.T) {
	first, err := HashPassword("same password")
	if err != nil {
		t.Fatal(err)
	}
	second, err := HashPassword("same password")
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("hashes reused a salt")
	}
}

func TestVerifyPasswordRejectsMalformedEncoding(t *testing.T) {
	for _, encoded := range []string{"", "password", "$argon2id$v=19$m=1,t=1,p=1$bad$bad", "$argon2id$v=18$m=65536,t=3,p=2$YWJj$YWJj"} {
		if VerifyPassword(encoded, "password") {
			t.Fatalf("malformed encoding accepted: %q", encoded)
		}
	}
}
