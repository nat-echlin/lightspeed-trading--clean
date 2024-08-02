package main

import (
	"context"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math/big"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt"
)

// handler for JSON web keys. the string in the keys map is the
// respective keys kid
type verifier struct {
	mu            sync.Mutex
	keys          map[string]JSONWebKey
	jwksFilePath  string
	lastUpdatedAt time.Time
}

// structure format of a json web key
type JSONWebKey struct {
	Alg string   `json:"alg"`
	Kty string   `json:"kty"`
	Use string   `json:"use"`
	N   string   `json:"n"`
	E   string   `json:"e"`
	Kid string   `json:"kid"`
	X5T string   `json:"x5t"`
	X5C []string `json:"x5c"`
}

type ctxKey string

const CtxKeyclaims = ctxKey("claims")

func NewVerifier(jwksFilePath string) *verifier {
	return &verifier{
		mu:            sync.Mutex{},
		keys:          map[string]JSONWebKey{},
		jwksFilePath:  jwksFilePath,
		lastUpdatedAt: time.Now().Add(-301 * time.Second), // so that it will load keys at first received request
	}
}

// loads keys into memory from .json file, if its been longer than 5 mins
// since they were last loaded (prevents unnecessary disk IO)
func (v *verifier) loadKeys() error {
	v.mu.Lock()
	defer v.mu.Unlock()

	// Check if the keys were updated in the last 5 minutes
	if time.Since(v.lastUpdatedAt) < 5*time.Minute {
		return nil
	}

	// Load JWKS keys from the local file.
	raw, err := os.ReadFile(v.jwksFilePath)
	if err != nil {
		return fmt.Errorf("failed to open file, %v", err)
	}

	var jwks map[string]JSONWebKey
	if err := json.Unmarshal(raw, &jwks); err != nil {
		return fmt.Errorf("failed to unmarshal json, %v", err)
	}

	v.keys = jwks
	v.lastUpdatedAt = time.Now()
	return nil
}

// finds a key based on its respective kid. Will collect the jwks
// again if its been less than 5 mins (via v.loadKeys func)
func (v *verifier) GetKeyForKid(ctx context.Context, kid string) (*rsa.PublicKey, error) {
	err := v.loadKeys()
	if err != nil {
		return nil, err
	}

	v.mu.Lock()
	defer v.mu.Unlock()

	jwk, ok := v.keys[kid]
	if !ok {
		return nil, fmt.Errorf("key not found for kid %s", kid)
	}

	// Decode the base64url encoded parts of the key
	nBytes, err := base64.RawURLEncoding.DecodeString(jwk.N)
	if err != nil {
		return nil, fmt.Errorf("failed to decode n from jwk, %v", err)
	}
	eBytes, err := base64.RawURLEncoding.DecodeString(jwk.E)
	if err != nil {
		return nil, fmt.Errorf("failed to decode e from jwk, %v", err)
	}

	// Turn them into big numbers
	n := new(big.Int).SetBytes(nBytes)
	e := new(big.Int).SetBytes(eBytes)

	// Build the public key
	publicKey := &rsa.PublicKey{
		N: n,
		E: int(e.Uint64()), // This will fail with big exponents, but those are extremely rare in the wild
	}

	return publicKey, nil
}

func (v *verifier) KeyFunc(token *jwt.Token) (interface{}, error) {
	// extract kid
	kid, ok := token.Header["kid"].(string)
	if !ok {
		return nil, errors.New("invalid token, no kid claim")
	}

	// get respective key for kid
	key, err := v.GetKeyForKid(context.Background(), kid)
	if err != nil {
		return nil, err
	}

	return key, nil
}

// Parse the token string into a JWT token, providing a callback function to supply the key for validation.
// The callback retrieves the key ID (kid) from the token header and uses it to find the corresponding key.
func (v *verifier) authoriseJWT(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// extract token from header
		tokenString := r.Header.Get("Authorization")
		tokenString = strings.TrimPrefix(tokenString, "Bearer ")

		// parse token
		token, err := jwt.ParseWithClaims(tokenString, &jwt.StandardClaims{}, v.KeyFunc)
		if err != nil {
			log.Printf("failed to auth, %v", err)
			http.Error(w, "Invalid token", http.StatusUnauthorized)
			return
		}

		// check if the JWT is valid
		claims, ok := token.Claims.(*jwt.StandardClaims)
		if ok && token.Valid {
			// put the claims into the context so the next handler can use them
			ctx := context.WithValue(r.Context(), CtxKeyclaims, claims)
			next.ServeHTTP(w, r.WithContext(ctx))
		} else {
			log.Printf("token not valid")
			http.Error(w, "Invalid token", http.StatusUnauthorized)
		}
	})
}

// will log errors
func extractUserID(r *http.Request) (string, error) {
	// get userID from JWT claims
	val := r.Context().Value(CtxKeyclaims)
	if val == nil {
		err := fmt.Errorf("no claims in context. Context: %v", r.Context())
		log.Printf("extractUserID error: %v", err)
		return "", err
	}

	claims, ok := val.(*jwt.StandardClaims)
	if !ok {
		err := fmt.Errorf("claims are not of type *jwt.StandardClaims. Type: %T, Value: %v", val, val)
		log.Printf("extractUserID error: %v", err)
		return "", err
	}

	return claims.Subject, nil
}

func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")

		allowedOrigins := []string{
			"http://localhost:3000",
			"https://dev-lightspeed-web.vercel.app",
			"https://lightspeed-web.vercel.app",
			"https://dev.lightspeedapp.xyz",
			"https://lightspeedapp.xyz",
		}

		// switch origin {
		// case "http://localhost:3000", "https://dev-lightspeed-web.vercel.app", "https://lightspeed-web.vercel.app":
		// 	w.Header().Set("Access-Control-Allow-Origin", origin)
		// default:
		// 	w.Header().Set("Access-Control-Allow-Origin", "none")
		// }

		isAllowed := false
		for _, allowedOrigin := range allowedOrigins {
			if allowedOrigin == origin {
				w.Header().Set("Access-Control-Allow-Origin", origin)
				isAllowed = true
				break
			}
		}
		if !isAllowed {
			w.Header().Set("Access-Control-Allow-Origin", "none")
		}

		w.Header().Set("Access-Control-Allow-Methods", "POST, GET, OPTIONS, PUT, DELETE")
		w.Header().Set("Access-Control-Allow-Headers", "Accept, Content-Type, Content-Length, Accept-Encoding, X-CSRF-Token, Authorization")

		// if it's just an OPTIONS request (a preflight request), nothing other than the headers is needed
		if r.Method == "OPTIONS" {
			return
		}

		next.ServeHTTP(w, r)
	})
}
