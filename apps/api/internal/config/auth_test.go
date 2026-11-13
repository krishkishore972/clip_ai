package config

import (
	"strings"
	"testing"
)

func TestAuthConfiguration(t *testing.T) {
	t.Setenv("CLERK_ISSUER","")
	t.Setenv("CLERK_SECRET_KEY","")
	if _,err:=LoadAuth(); err==nil { t.Fatal("accepted missing auth configuration") }
	t.Setenv("CLERK_ISSUER","https://example.clerk.accounts.dev/")
	t.Setenv("CLERK_SECRET_KEY","sk_test_fake")
	t.Setenv("CLERK_AUTHORIZED_PARTIES","https://one.example, https://two.example")
	t.Setenv("CLERK_AUDIENCE","clip-api")
	cfg,err:=LoadAuth(); if err!=nil || len(cfg.AuthorizedParties)!=2 || cfg.Audience!="clip-api" || strings.HasSuffix(cfg.Issuer,"/") { t.Fatalf("valid config failed: %v",err) }
	for _,issuer:=range []string{"http://host","https://user:secret@host","https://host?secret=1","https://host/path"} {
		t.Setenv("CLERK_ISSUER",issuer)
		if _,err:=LoadAuth(); err==nil || strings.Contains(err.Error(),"secret") { t.Fatal("issuer validation failed or leaked configuration") }
	}
}
