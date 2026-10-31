// Package testsupport: see testsupport.go.
package testsupport

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"

	"github.com/fxamacker/cbor/v2"
	"github.com/go-webauthn/webauthn/protocol"
)

// VirtualAuthenticator is a software passkey (platform authenticator with user
// verification) good enough to drive the relying-party code in tests.
type VirtualAuthenticator struct {
	key        *ecdsa.PrivateKey
	credID     []byte
	UserHandle []byte
	signCount  uint32
	// UV controls the user-verified flag of what it produces.
	UV bool
}

// NewVirtualAuthenticator creates a passkey that verifies the user.
func NewVirtualAuthenticator() *VirtualAuthenticator {
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		panic(err)
	}
	id := make([]byte, 32)
	rand.Read(id)
	return &VirtualAuthenticator{key: k, credID: id, UV: true}
}

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func rpHash(rpID string) []byte { h := sha256.Sum256([]byte(rpID)); return h[:] }

func (v *VirtualAuthenticator) flags(attested bool) byte {
	f := byte(0x01) // user present
	if v.UV {
		f |= 0x04
	}
	if attested {
		f |= 0x40
	}
	return f
}

// Create answers a registration request (returns the request a browser would POST).
func (v *VirtualAuthenticator) Create(creation *protocol.CredentialCreation, origin string) *http.Request {
	switch id := creation.Response.User.ID.(type) {
	case protocol.URLEncodedBase64: // straight from the library
		v.UserHandle = id
	case string: // after a JSON round trip through the HTTP API
		h, err := base64.RawURLEncoding.DecodeString(id)
		if err != nil {
			panic(err)
		}
		v.UserHandle = h
	default:
		panic("unexpected user id type")
	}
	x, y := v.key.PublicKey.X.FillBytes(make([]byte, 32)), v.key.PublicKey.Y.FillBytes(make([]byte, 32))
	cose, err := cbor.Marshal(map[int]any{1: 2, 3: -7, -1: 1, -2: x, -3: y})
	if err != nil {
		panic(err)
	}
	authData := append(rpHash(creation.Response.RelyingParty.ID), v.flags(true))
	authData = binary.BigEndian.AppendUint32(authData, v.signCount)
	authData = append(authData, make([]byte, 16)...) // AAGUID
	authData = binary.BigEndian.AppendUint16(authData, uint16(len(v.credID)))
	authData = append(authData, v.credID...)
	authData = append(authData, cose...)
	att, err := cbor.Marshal(map[string]any{"fmt": "none", "attStmt": map[string]any{}, "authData": authData})
	if err != nil {
		panic(err)
	}
	cd, _ := json.Marshal(map[string]any{"type": "webauthn.create", "challenge": b64(creation.Response.Challenge), "origin": origin})
	return JSONRequest(map[string]any{
		"id": b64(v.credID), "rawId": b64(v.credID), "type": "public-key",
		"response": map[string]any{"attestationObject": b64(att), "clientDataJSON": b64(cd), "transports": []string{"internal"}},
	})
}

// Get answers an authentication request.
func (v *VirtualAuthenticator) Get(assertion *protocol.CredentialAssertion, origin string) *http.Request {
	v.signCount++
	authData := append(rpHash(assertion.Response.RelyingPartyID), v.flags(false))
	authData = binary.BigEndian.AppendUint32(authData, v.signCount)
	cd, _ := json.Marshal(map[string]any{"type": "webauthn.get", "challenge": b64(assertion.Response.Challenge), "origin": origin})
	cdHash := sha256.Sum256(cd)
	digest := sha256.Sum256(append(append([]byte{}, authData...), cdHash[:]...))
	sig, err := ecdsa.SignASN1(rand.Reader, v.key, digest[:])
	if err != nil {
		panic(err)
	}
	return JSONRequest(map[string]any{
		"id": b64(v.credID), "rawId": b64(v.credID), "type": "public-key",
		"response": map[string]any{"authenticatorData": b64(authData), "clientDataJSON": b64(cd), "signature": b64(sig), "userHandle": b64(v.UserHandle)},
	})
}

// JSONRequest builds a POST request with a JSON body.
func JSONRequest(body any) *http.Request {
	b, _ := json.Marshal(body)
	r := httptest.NewRequest("POST", "/finish", strings.NewReader(string(b)))
	r.Header.Set("Content-Type", "application/json")
	return r
}

// CredentialID returns the base64url credential id.
func (v *VirtualAuthenticator) CredentialID() string { return b64(v.credID) }
