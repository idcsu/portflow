package auth

import "testing"

func TestPasswordHash(t *testing.T) {
	hash, err := HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	if hash == "correct horse battery staple" || !CheckPassword(hash, "correct horse battery staple") {
		t.Fatal("password hash did not verify")
	}
	if CheckPassword(hash, "incorrect password") {
		t.Fatal("incorrect password verified")
	}
}

func TestNormalizeUsername(t *testing.T) {
	username, err := NormalizeUsername("  Admin_User  ")
	if err != nil || username != "admin_user" {
		t.Fatalf("unexpected result username=%q err=%v", username, err)
	}
	if _, err := NormalizeUsername("bad user"); err == nil {
		t.Fatal("invalid username accepted")
	}
}

func TestTokensAreRandomAndHashable(t *testing.T) {
	first, err := NewToken(32)
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewToken(32)
	if err != nil {
		t.Fatal(err)
	}
	if first == second || TokenHash(first) == first || TokenHash(first) == TokenHash(second) {
		t.Fatal("token generation or hashing is unsafe")
	}
}
