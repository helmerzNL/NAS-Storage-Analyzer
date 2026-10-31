package auth_test

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

// virtualAuthenticator is a software passkey (platform authenticator with user
// verification) good enough to drive the relying-party code in tests.
type virtualAuthenticator struct {
	key        *ecdsa.PrivateKey
	credID     []byte
	userHandle []byte
	signCount  uint32
	// uv controls the user-verified flag of what it produces.
	uv bool
}

func newVirtualAuthenticator() *virtualAuthenticator {
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		panic(err)
	}
	id := make([]byte, 32)
	rand.Read(id)
	return &virtualAuthenticator{key: k, credID: id, uv: true}
}

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func rpHash(rpID string) []byte { h := sha256.Sum256([]byte(rpID)); return h[:] }

func (v *virtualAuthenticator) flags(attested bool) byte {
	f := byte(0x01) // user present
	if v.uv {
		f |= 0x04
	}
	if attested {
		f |= 0x40
	}
	return f
}

// create answers a registration request.
func (v *virtualAuthenticator) create(creation *protocol.CredentialCreation, origin string) *http.Request {
	v.userHandle = creation.Response.User.ID.(protocol.URLEncodedBase64)
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
	return jsonRequest(map[string]any{
		"id": b64(v.credID), "rawId": b64(v.credID), "type": "public-key",
		"response": map[string]any{"attestationObject": b64(att), "clientDataJSON": b64(cd), "transports": []string{"internal"}},
	})
}

// get answers an authentication request.
func (v *virtualAuthenticator) get(assertion *protocol.CredentialAssertion, origin string) *http.Request {
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
	return jsonRequest(map[string]any{
		"id": b64(v.credID), "rawId": b64(v.credID), "type": "public-key",
		"response": map[string]any{"authenticatorData": b64(authData), "clientDataJSON": b64(cd), "signature": b64(sig), "userHandle": b64(v.userHandle)},
	})
}

func jsonRequest(body any) *http.Request {
	b, _ := json.Marshal(body)
	r := httptest.NewRequest("POST", "/finish", strings.NewReader(string(b)))
	r.Header.Set("Content-Type", "application/json")
	return r
}
