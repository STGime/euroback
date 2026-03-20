// gen-test-jwt generates a test JWT for local Postman testing.
// Usage: go run scripts/gen-test-jwt.go
//
// The token is signed with a local RSA key and will be accepted by the gateway
// ONLY if you start the gateway with a matching test JWKS endpoint.
// For now, use this with the Hanko middleware bypassed or mocked.
package main

import (
	"crypto/rand"
	"crypto/rsa"
	"fmt"
	"os"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func main() {
	sub := "postman-test-user-001"
	email := "dev@eurobase.app"

	if len(os.Args) > 1 {
		sub = os.Args[1]
	}
	if len(os.Args) > 2 {
		email = os.Args[2]
	}

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error generating RSA key: %v\n", err)
		os.Exit(1)
	}

	claims := jwt.MapClaims{
		"sub":   sub,
		"email": email,
		"iat":   time.Now().Unix(),
		"exp":   time.Now().Add(24 * time.Hour).Unix(),
		"iss":   "eurobase-local-test",
	}

	token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	tokenStr, err := token.SignedString(key)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error signing token: %v\n", err)
		os.Exit(1)
	}

	fmt.Println(tokenStr)
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "Note: This JWT is signed with a throwaway RSA key.")
	fmt.Fprintln(os.Stderr, "The gateway's Hanko middleware will reject it because it")
	fmt.Fprintln(os.Stderr, "doesn't match the Hanko JWKS. To test locally, you have two options:")
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "  Option A: Use curl to test the unauthenticated endpoints:")
	fmt.Fprintln(os.Stderr, "    curl http://localhost:8080/health")
	fmt.Fprintln(os.Stderr, "    curl -X POST http://localhost:8080/platform/webhooks/hanko ...")
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "  Option B: Temporarily bypass auth in the gateway by commenting")
	fmt.Fprintln(os.Stderr, "    out the hankoAuth.Handler middleware in internal/gateway/router.go")
	fmt.Fprintln(os.Stderr, "    and injecting test claims via a dev-only middleware.")
}
