// Package jwtauth issues and verifies HS256 JSON Web Tokens for access
// tokens (FEATURES.md §1: "JWT + refresh token rotation"). Hand-rolled
// with stdlib only (crypto/hmac, encoding/json, encoding/base64) rather
// than an external JWT library — matching this repo's convention
// elsewhere of not reaching for a dependency until there's a real reason
// to, and HS256 (header.payload.signature, all base64url, no padding)
// is a small enough spec to implement directly and understand fully.
//
// TODO(real build): HS256 means every service that needs to verify a
// token must share the same secret — fine for this skeleton (one auth
// service, a handful of trusted internal services), but a real
// multi-team build would likely prefer RS256/ES256 (asymmetric: auth
// signs with a private key, every other service verifies with a public
// key it can safely hold) so the signing secret never needs to be
// distributed anywhere except the auth service itself.
package jwtauth

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

var ErrTokenExpired = errors.New("token has expired")
var ErrTokenSignatureInvalid = errors.New("token signature is invalid")
var ErrTokenMalformed = errors.New("token is malformed")

type jwtHeader struct {
	Algorithm string `json:"alg"`
	TokenType string `json:"typ"`
}

// AccessTokenClaims is this service's JWT payload shape. Deliberately
// minimal — just enough for another service to identify who's calling
// and confirm the token hasn't expired. `Subject` deliberately mirrors
// the standard JWT "sub" claim name (RFC 7519) even though every other
// wire type in this repo uses a long descriptive name, since interop
// with any standard JWT tooling/library depends on that exact key.
type AccessTokenClaims struct {
	Subject       string `json:"sub"`
	IssuedAtUnix  int64  `json:"iat"`
	ExpiresAtUnix int64  `json:"exp"`
}

// IssueAccessToken builds and signs a JWT for accountIdentifier, valid
// for tokenLifetime from now.
func IssueAccessToken(accountIdentifier string, signingSecret []byte, tokenLifetime time.Duration, issuedAt time.Time) (string, error) {
	header := jwtHeader{Algorithm: "HS256", TokenType: "JWT"}
	claims := AccessTokenClaims{
		Subject:       accountIdentifier,
		IssuedAtUnix:  issuedAt.Unix(),
		ExpiresAtUnix: issuedAt.Add(tokenLifetime).Unix(),
	}

	encodedHeader, headerEncodeError := encodeJsonSegment(header)
	if headerEncodeError != nil {
		return "", fmt.Errorf("failed to encode JWT header: %w", headerEncodeError)
	}
	encodedClaims, claimsEncodeError := encodeJsonSegment(claims)
	if claimsEncodeError != nil {
		return "", fmt.Errorf("failed to encode JWT claims: %w", claimsEncodeError)
	}

	signingInput := encodedHeader + "." + encodedClaims
	signature := computeHmacSignature(signingInput, signingSecret)

	return signingInput + "." + signature, nil
}

// ParseAndVerifyAccessToken validates the signature and expiry of
// tokenString and, if valid, returns its claims. Returns
// ErrTokenSignatureInvalid for a tampered/wrong-secret token,
// ErrTokenExpired for a structurally valid but expired one, and
// ErrTokenMalformed for anything that doesn't even parse as a 3-segment
// JWT — callers can distinguish "reject and tell the client to log in
// again" (expired) from "this token was never legitimately ours"
// (invalid/malformed) if they want to.
func ParseAndVerifyAccessToken(tokenString string, signingSecret []byte, now time.Time) (AccessTokenClaims, error) {
	var claims AccessTokenClaims

	headerSegment, claimsSegment, signatureSegment, splitError := splitTokenIntoThreeSegments(tokenString)
	if splitError != nil {
		return claims, splitError
	}

	signingInput := headerSegment + "." + claimsSegment
	expectedSignature := computeHmacSignature(signingInput, signingSecret)
	if subtle.ConstantTimeCompare([]byte(expectedSignature), []byte(signatureSegment)) != 1 {
		return claims, ErrTokenSignatureInvalid
	}

	claimsJson, decodeError := base64.RawURLEncoding.DecodeString(claimsSegment)
	if decodeError != nil {
		return claims, fmt.Errorf("%w: %v", ErrTokenMalformed, decodeError)
	}
	if unmarshalError := json.Unmarshal(claimsJson, &claims); unmarshalError != nil {
		return claims, fmt.Errorf("%w: %v", ErrTokenMalformed, unmarshalError)
	}

	if now.Unix() >= claims.ExpiresAtUnix {
		return claims, ErrTokenExpired
	}

	return claims, nil
}

func splitTokenIntoThreeSegments(tokenString string) (header string, claims string, signature string, err error) {
	firstDotIndex := indexOfByte(tokenString, '.')
	if firstDotIndex < 0 {
		return "", "", "", ErrTokenMalformed
	}
	secondDotIndex := indexOfByte(tokenString[firstDotIndex+1:], '.')
	if secondDotIndex < 0 {
		return "", "", "", ErrTokenMalformed
	}
	secondDotIndex += firstDotIndex + 1

	return tokenString[:firstDotIndex], tokenString[firstDotIndex+1 : secondDotIndex], tokenString[secondDotIndex+1:], nil
}

func indexOfByte(s string, target byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == target {
			return i
		}
	}
	return -1
}

func encodeJsonSegment(value any) (string, error) {
	jsonBytes, marshalError := json.Marshal(value)
	if marshalError != nil {
		return "", marshalError
	}
	return base64.RawURLEncoding.EncodeToString(jsonBytes), nil
}

func computeHmacSignature(signingInput string, signingSecret []byte) string {
	hmacHasher := hmac.New(sha256.New, signingSecret)
	hmacHasher.Write([]byte(signingInput))
	return base64.RawURLEncoding.EncodeToString(hmacHasher.Sum(nil))
}
