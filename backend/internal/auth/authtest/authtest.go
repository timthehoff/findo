// Package authtest is a software WebAuthn authenticator for tests: it makes
// real ES256 keys and builds the registration and assertion responses a browser
// would send, so tests exercise the real verification code rather than a mock.
package authtest

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"

	"github.com/go-webauthn/webauthn/protocol/webauthncbor"
)

// Authenticator is one passkey on one device.
type Authenticator struct {
	key        *ecdsa.PrivateKey
	CredID     []byte
	UserHandle []byte
	SignCount  uint32

	// BackupEligible and BackupState set the BE/BS flags it reports, as a synced
	// passkey does.
	BackupEligible bool
	BackupState    bool
	// SkipUserVerification makes it report no UV, as a device without a PIN or
	// biometric would; the server must refuse that.
	SkipUserVerification bool
}

func New() *Authenticator {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		panic(err)
	}
	id := make([]byte, 32)
	if _, err := rand.Read(id); err != nil {
		panic(err)
	}
	return &Authenticator{key: key, CredID: id}
}

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

// creationOptions and requestOptions are the parts of the server's options an
// authenticator reads.
type creationOptions struct {
	PublicKey struct {
		Challenge string `json:"challenge"`
		RP        struct {
			ID string `json:"id"`
		} `json:"rp"`
		User struct {
			ID string `json:"id"`
		} `json:"user"`
		ExcludeCredentials []struct {
			ID string `json:"id"`
		} `json:"excludeCredentials"`
		AuthenticatorSelection struct {
			ResidentKey      string `json:"residentKey"`
			UserVerification string `json:"userVerification"`
		} `json:"authenticatorSelection"`
		Attestation string `json:"attestation"`
	} `json:"publicKey"`
}

type requestOptions struct {
	PublicKey struct {
		Challenge        string `json:"challenge"`
		RPID             string `json:"rpId"`
		UserVerification string `json:"userVerification"`
		AllowCredentials []struct {
			ID string `json:"id"`
		} `json:"allowCredentials"`
	} `json:"publicKey"`
}

func (a *Authenticator) flags(attested bool) byte {
	f := byte(0x01) // user present
	if !a.SkipUserVerification {
		f |= 0x04
	}
	if a.BackupEligible {
		f |= 0x08
	}
	if a.BackupState {
		f |= 0x10
	}
	if attested {
		f |= 0x40
	}
	return f
}

func (a *Authenticator) cosePublicKey() []byte {
	x := a.key.PublicKey.X.FillBytes(make([]byte, 32))
	y := a.key.PublicKey.Y.FillBytes(make([]byte, 32))
	out, err := webauthncbor.Marshal(map[int]interface{}{1: 2, 3: -7, -1: 1, -2: x, -3: y})
	if err != nil {
		panic(err)
	}
	return out
}

func clientData(typ, challenge, origin string) []byte {
	b, _ := json.Marshal(map[string]interface{}{"type": typ, "challenge": challenge, "origin": origin, "crossOrigin": false})
	return b
}

// Register answers the server's registration options (the "options" object from
// /begin) with the credential JSON a browser would send to /finish. It sends
// the user handle the server asked for, so later sign-ins can be discoverable.
func (a *Authenticator) Register(optionsJSON []byte, origin string) ([]byte, error) {
	var o creationOptions
	if err := json.Unmarshal(optionsJSON, &o); err != nil {
		return nil, err
	}
	for _, ex := range o.PublicKey.ExcludeCredentials {
		if ex.ID == b64(a.CredID) {
			return nil, fmt.Errorf("authenticator: this passkey is already registered here")
		}
	}
	if h, err := base64.RawURLEncoding.DecodeString(o.PublicKey.User.ID); err == nil {
		a.UserHandle = h
	}
	rpHash := sha256.Sum256([]byte(o.PublicKey.RP.ID))

	authData := append([]byte{}, rpHash[:]...)
	authData = append(authData, a.flags(true))
	authData = binary.BigEndian.AppendUint32(authData, a.SignCount)
	authData = append(authData, make([]byte, 16)...) // AAGUID
	authData = binary.BigEndian.AppendUint16(authData, uint16(len(a.CredID)))
	authData = append(authData, a.CredID...)
	authData = append(authData, a.cosePublicKey()...)

	att, err := webauthncbor.Marshal(map[string]interface{}{"fmt": "none", "attStmt": map[string]interface{}{}, "authData": authData})
	if err != nil {
		return nil, err
	}
	return json.Marshal(map[string]interface{}{
		"id": b64(a.CredID), "rawId": b64(a.CredID), "type": "public-key",
		"response": map[string]interface{}{
			"clientDataJSON":    b64(clientData("webauthn.create", o.PublicKey.Challenge, origin)),
			"attestationObject": b64(att),
			"transports":        []string{"internal"},
		},
		"clientExtensionResults": map[string]interface{}{},
	})
}

// Assert answers the server's sign-in options with the assertion JSON a browser
// would send to /finish, bumping the sign count first.
func (a *Authenticator) Assert(optionsJSON []byte, origin string) ([]byte, error) {
	var o requestOptions
	if err := json.Unmarshal(optionsJSON, &o); err != nil {
		return nil, err
	}
	return a.assert(o.PublicKey.Challenge, o.PublicKey.RPID, origin, a.UserHandle)
}

// AssertWithHandle is Assert but claiming a different user handle, as a
// tampered or confused client might.
func (a *Authenticator) AssertWithHandle(optionsJSON []byte, origin string, handle []byte) ([]byte, error) {
	var o requestOptions
	if err := json.Unmarshal(optionsJSON, &o); err != nil {
		return nil, err
	}
	return a.assert(o.PublicKey.Challenge, o.PublicKey.RPID, origin, handle)
}

func (a *Authenticator) assert(challenge, rpID, origin string, handle []byte) ([]byte, error) {
	a.SignCount++
	rpHash := sha256.Sum256([]byte(rpID))
	authData := append([]byte{}, rpHash[:]...)
	authData = append(authData, a.flags(false))
	authData = binary.BigEndian.AppendUint32(authData, a.SignCount)

	cd := clientData("webauthn.get", challenge, origin)
	cdHash := sha256.Sum256(cd)
	digest := sha256.Sum256(append(append([]byte{}, authData...), cdHash[:]...))
	sig, err := ecdsa.SignASN1(rand.Reader, a.key, digest[:])
	if err != nil {
		return nil, err
	}
	return json.Marshal(map[string]interface{}{
		"id": b64(a.CredID), "rawId": b64(a.CredID), "type": "public-key",
		"response": map[string]interface{}{
			"clientDataJSON":    b64(cd),
			"authenticatorData": b64(authData),
			"signature":         b64(sig),
			"userHandle":        b64(handle),
		},
		"clientExtensionResults": map[string]interface{}{},
	})
}
