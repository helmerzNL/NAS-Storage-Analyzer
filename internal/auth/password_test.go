package auth

import (
	"errors"
	"strings"
	"testing"
)

func TestMain(m *testing.M) {
	SetFastHashingForTests()
	m.Run()
}

func TestPasswordHashAndVerify(t *testing.T) {
	h := HashPassword("correct horse battery staple")
	if !strings.HasPrefix(h, "$argon2id$v=19$") {
		t.Fatalf("hash = %s", h)
	}
	if !VerifyPassword("correct horse battery staple", h) {
		t.Error("right password rejected")
	}
	for _, bad := range []string{"", "wrong", "correct horse battery stapl", "Correct horse battery staple"} {
		if VerifyPassword(bad, h) {
			t.Errorf("accepted %q", bad)
		}
	}
	if HashPassword("same") == HashPassword("same") {
		t.Error("identical hashes: salt not random")
	}
	for _, junk := range []string{"", "plain", "$argon2id$v=19$m=1,t=1,p=1$$", "$bcrypt$x$y$z$w", "$argon2id$v=18$m=8,t=1,p=1$AAAA$AAAA"} {
		if VerifyPassword("x", junk) {
			t.Errorf("accepted malformed hash %q", junk)
		}
	}
}

func TestPasswordPolicy(t *testing.T) {
	cases := map[string]bool{"short": false, "elevenchars": false, "twelve chars": true, "alice-alice-alice": true}
	for pw, ok := range cases {
		err := CheckPasswordPolicy(pw, "bob")
		if (err == nil) != ok {
			t.Errorf("%q: err = %v", pw, err)
		}
		if err != nil && !errors.Is(err, ErrWeakPassword) {
			t.Errorf("%q: not ErrWeakPassword", pw)
		}
	}
	if CheckPasswordPolicy("Administrator", "administrator") == nil {
		t.Error("password equal to username accepted")
	}
	if CheckPasswordPolicy(strings.Repeat("x", 300), "bob") == nil {
		t.Error("absurdly long password accepted")
	}
}
