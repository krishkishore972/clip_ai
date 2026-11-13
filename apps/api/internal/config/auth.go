package config

import (
	"errors"
	"net/url"
	"os"
	"strings"
)

type Auth struct {
	Issuer            string
	SecretKey         string
	Audience          string
	AuthorizedParties []string
}

func LoadAuth() (Auth, error) {
	var cfg Auth
	var err error
	cfg.Issuer, err = required("CLERK_ISSUER")
	if err != nil {
		return cfg, err
	}
	u, err := url.Parse(cfg.Issuer)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return Auth{}, errors.New("config: CLERK_ISSUER must be the HTTPS Clerk Frontend API origin")
	}
	cfg.Issuer = strings.TrimSuffix(cfg.Issuer, "/")
	cfg.SecretKey, err = required("CLERK_SECRET_KEY")
	if err != nil {
		return Auth{}, err
	}
	if !strings.HasPrefix(cfg.SecretKey, "sk_") {
		return Auth{}, errors.New("config: CLERK_SECRET_KEY must be a Clerk secret key")
	}
	cfg.Audience = strings.TrimSpace(os.Getenv("CLERK_AUDIENCE"))
	for _, party := range strings.Split(os.Getenv("CLERK_AUTHORIZED_PARTIES"), ",") {
		if party = strings.TrimSpace(party); party != "" {
			cfg.AuthorizedParties = append(cfg.AuthorizedParties, party)
		}
	}
	return cfg, nil
}
