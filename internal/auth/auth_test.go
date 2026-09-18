package auth

import "testing"

func TestPasswordRoundTrip(t *testing.T) {
	h, err := HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	if !VerifyPassword(h, "correct horse battery staple") {
		t.Fatal("valid password rejected")
	}
	if VerifyPassword(h, "wrong password") {
		t.Fatal("invalid password accepted")
	}
}
func TestShortPassword(t *testing.T) {
	if _, err := HashPassword("short"); err == nil {
		t.Fatal("expected validation error")
	}
}
func TestTokenHashStable(t *testing.T) {
	if TokenHash("x") != TokenHash("x") || TokenHash("x") == TokenHash("y") {
		t.Fatal("bad token hash")
	}
}
