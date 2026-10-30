package secrets

import (
	"encoding/base64"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"
)

const (
	upper  = "ABCDEFGHIJKLMNOPQRSTUVWXYZ"
	lower  = "abcdefghijklmnopqrstuvwxyz"
	digits = "0123456789"
	base62 = upper + lower + digits
	hex    = digits + "abcdef"
	urlB64 = base62 + "-_"
)

func random(r *rand.Rand, alphabet string, n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = alphabet[r.IntN(len(alphabet))]
	}
	return string(b)
}

type shape struct {
	kind string
	make func(r *rand.Rand) (line, secret string)
}

func prefixed(kind, before, prefix, alphabet string, n int, after string) shape {
	return shape{kind, func(r *rand.Rand) (string, string) {
		secret := prefix + random(r, alphabet, n) + after
		return before + secret, secret
	}}
}

var shapes = []shape{
	prefixed("AWS access key ID", "aws_access_key_id = ", "AKIA", upper+"234567", 16, ""),
	prefixed("AWS access key ID", "role session: ", "ASIA", upper+"234567", 16, ""),
	prefixed("GitHub token", "export GITHUB_TOKEN=", "ghp_", base62, 36, ""),
	prefixed("GitHub token", "oauth: ", "gho_", base62, 36, ""),
	prefixed("GitHub token", "fine-grained ", "github_pat_", base62+"_", 82, ""),
	prefixed("Stripe API key", "STRIPE_SECRET_KEY=", "sk_live_", base62, 99, ""),
	prefixed("Stripe API key", "test mode: ", "sk_test_", base62, 24, ""),
	prefixed("Stripe API key", "restricted ", "rk_live_", base62, 99, ""),
	prefixed("Stripe webhook secret", "STRIPE_WEBHOOK_SECRET=", "whsec_", base62, 32, ""),
	{"Slack token", func(r *rand.Rand) (string, string) {
		secret := "xoxb-" + random(r, digits, 12) + "-" + random(r, digits, 13) + "-" + random(r, base62, 24)
		return "SLACK_BOT_TOKEN=" + secret, secret
	}},
	{"Slack token", func(r *rand.Rand) (string, string) {
		secret := "xoxp-" + random(r, digits, 11) + "-" + random(r, digits, 12) + "-" + random(r, digits, 13) + "-" + random(r, hex, 32)
		return "user token " + secret, secret
	}},
	{"Slack token", func(r *rand.Rand) (string, string) {
		secret := "xapp-1-A" + random(r, upper+digits, 10) + "-" + random(r, digits, 13) + "-" + random(r, hex, 64)
		return "socket mode: " + secret, secret
	}},
	prefixed("Anthropic API key", "ANTHROPIC_API_KEY=", "sk-ant-api03-", urlB64, 93, "AA"),
	{"OpenAI API key", func(r *rand.Rand) (string, string) {
		secret := "sk-proj-" + random(r, urlB64, 74) + "T3BlbkFJ" + random(r, urlB64, 74)
		return "OPENAI_API_KEY=" + secret, secret
	}},
	{"OpenAI API key", func(r *rand.Rand) (string, string) {
		secret := "sk-" + random(r, base62, 20) + "T3BlbkFJ" + random(r, base62, 20)
		return "openai.api_key = '" + secret + "'", secret
	}},
	prefixed("Google API key", "maps: ", "AIza", urlB64, 35, ""),
	{"JSON Web Token", func(r *rand.Rand) (string, string) {
		payload := base64.RawURLEncoding.EncodeToString([]byte(`{"sub":"` + random(r, base62, 12) + `","iat":1767225600}`))
		secret := "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9." + payload + "." + random(r, urlB64, 43)
		return `curl -H "Cookie: session=` + secret + `" https://api.example.com`, secret
	}},
	{"private key", func(r *rand.Rand) (string, string) {
		lines := []string{"-----BEGIN OPENSSH PRIVATE KEY-----"}
		for range 6 {
			lines = append(lines, random(r, base62+"+/", 70))
		}
		secret := strings.Join(append(lines, "-----END OPENSSH PRIVATE KEY-----"), "\n")
		return secret, lines[1]
	}},
	prefixed("Linear API key", "LINEAR_API_KEY=", "lin_api_", base62, 40, ""),
	prefixed("wong token", "", "wong_acme_", hex, 64, " < token.txt"),
	prefixed("wong token", "wong remote https://wong.example.com <<< ", "wong_", hex, 64, ""),
	prefixed("bearer token", "curl -H 'Authorization: Bearer ", "", hex, 40, "' https://api.example.com"),
	prefixed("password in a URL", "DATABASE_URL=postgres://acme:", "", hex, 24, "@db.example.com:5432/acme"),
	prefixed("value of DB_PASSWORD", `DB_PASSWORD="`, "", hex, 32, `"`),
	prefixed("value of client_secret", "client_secret: ", "", hex, 40, ""),
	prefixed("value of api_key", `{"api_key": "`, "", hex, 40, `"}`),
}

func TestScanFindsEachKindWithoutTheSecret(t *testing.T) {
	r := rand.New(rand.NewPCG(15, 2026))
	for _, s := range shapes {
		line, secret := s.make(r)
		text := "---\nname: acme-credentials\ndescription: d\n---\nWhat Acme's deploy needs:\n" + line + "\n"
		found := Scan([]byte(text))
		if !slices.Equal(found, []Finding{{Line: 6, Kind: s.kind}}) {
			t.Errorf("%s: Scan = %v", s.kind, found)
		}
		for _, msg := range []string{List(found), found[0].String()} {
			if strings.Contains(msg, secret) {
				t.Errorf("%s: the message %q holds the secret", s.kind, msg)
			}
		}
	}
}

func TestRandomCredentialsClearTheBar(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 1))
	for _, s := range shapes {
		for range 2000 {
			if line, _ := s.make(r); len(Scan([]byte(line))) == 0 {
				t.Errorf("%s: a random one went unnoticed: Scan found nothing", s.kind)
				break
			}
		}
	}
}

func TestPlaceholdersAreNotSecrets(t *testing.T) {
	for why, text := range map[string]string{
		"a key cut short": "STRIPE_SECRET_KEY=sk_test_xxx",
		"a key of one letter: 0 bits a character, under the 3 a Stripe key needs": "STRIPE_SECRET_KEY=sk_live_" + strings.Repeat("X", 24),
		"a key that ends in an ellipsis":                                          "sk_live_...",
		"a name in angle brackets":                                                "GITHUB_TOKEN=<token>",
		"an environment variable":                                                 "GITHUB_TOKEN=$GITHUB_TOKEN",
		"a template":                                                              "token: ${{ secrets.GITHUB_TOKEN }}",
		"a token of x":                                                            "export GITHUB_TOKEN=ghp_xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx",
		"characters in order, a run of 8 or more":                                 "ghp_1234567890abcdefghijklmnopqrstuvwxyz",
		"wong's token format as the docs write it":                                "wong_<account>_<hex>",
		"a wong token of zeros":                                                   "wong_acme_0000000000000000000000000000000000000000000000000000000000000000",
		"AWS's documented example key ID, ending in EXAMPLE":                      "aws_access_key_id = AKIAIOSFODNN7EXAMPLE",
		"AWS's documented example secret":                                         "aws_secret_access_key = wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY",
		"an AWS key ID of three letters in turn: 1.6 bits, under its bar of 2":    "AKIAABCABCABCABCABCA",
		"a password everyone knows":                                               `password = "changeme"`,
		"a password under 12 characters, too short to tell from a word":           "DB_PASSWORD=p4ssw0rd",
		"a value that asks for the real one":                                      "api_key: your-api-key-here",
		"code that reads the secret":                                              `token := os.Getenv("WONG_TOKEN")`,
		"code that reads the secret from a module":                                "password: process.env.DB_PASSWORD",
		"an issue id: its letters and digits sit in separate runs":                "secret: ENG-15-followup-for-billing",
		"a version: letters and digits mix in a run shorter than 8":               "token_type: oauth2-bearer-v1",
		"a camelCase name: letters without a digit":                               "apiKey: AcmeBillingProduction",
		"a file name with key in it, beside its sha":                              `"projects/acme/duplicate-key.md": "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08",`,
		"a bearer token in angle brackets":                                        "Authorization: Bearer <token>",
		"a bearer token from the environment":                                     "Authorization: Bearer $TOKEN",
		"a bearer token too short to be random":                                   "Authorization: Bearer abc123",
		"a URL with the word password":                                            "postgres://user:password@localhost:5432/acme",
		"a URL with a template":                                                   "postgres://acme:${DB_PASSWORD}@db.example.com/acme",
		"an ssh remote":                                                           "git@github.com:acme/acme-api.git",
		"a Slack token that asks for the real one":                                "xoxb-your-token-here",
		"a Slack token of zeros and x":                                            "xoxb-0000000000-0000000000-" + strings.Repeat("x", 24),
		"a private key with its body left out":                                    "-----BEGIN PRIVATE KEY-----\n...\n-----END PRIVATE KEY-----",
		"a private key with a placeholder for a body":                             "-----BEGIN RSA PRIVATE KEY-----\n<paste the key here, and keep it out of the brain, which syncs to the server>\n-----END RSA PRIVATE KEY-----",
		"an Anthropic key cut short":                                              "sk-ant-api03-...",
		"an OpenAI key of x":                                                      "sk-proj-xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx",
		"a kebab-case name after sk-":                                             "sk-billing-migration-plan-for-the-acme-api-2026",
		"a Google key of X":                                                       "AIzaSyXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX",
		"a Linear key of x":                                                       "lin_api_" + strings.Repeat("x", 40),
		"a commit cited as a source":                                              "  - commit:acme-api@1a2b3c4d5e6f7a8b9c0d1e2f3a4b5c6d7e8f9a0b",
		"prose about a token":                                                     "The deploy token lives in the vault; rotate it with `wong serve --new-token ci`.",
		"prose about a JWT":                                                       "the session token is a JWT signed with HS256, see ENG-15",
		"a JWT with no signature":                                                 "eyJhbGciOiJub25lIn0.eyJzdWIiOiJhY21lIn0.",
	} {
		if found := Scan([]byte(text)); len(found) > 0 {
			t.Errorf("%s: Scan(%q) = %v", why, text, found)
		}
	}
}

func TestScanCountsLinesAndKeepsOneFindingPerSecret(t *testing.T) {
	r := rand.New(rand.NewPCG(7, 7))
	github := "ghp_" + random(r, base62, 36)
	aws := "AKIA" + random(r, upper+"234567", 16)
	text := "---\nname: n\ndescription: d\n---\n" +
		"GITHUB_TOKEN=" + github + "\n" +
		"\n" +
		"Authorization: Bearer " + github + "\n" +
		"aws_access_key_id=" + aws + " and again " + aws + "\n"
	want := []Finding{{5, "GitHub token"}, {7, "GitHub token"}, {8, "AWS access key ID"}}
	if got := Scan([]byte(text)); !slices.Equal(got, want) {
		t.Errorf("Scan = %v, want %v", got, want)
	}
	if got := List(want); got != "line 5 (GitHub token), line 7 (GitHub token), line 8 (AWS access key ID)" {
		t.Errorf("List = %q", got)
	}
}
