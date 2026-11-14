package auth

import (
	"encoding/binary"
	"net/url"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"

	"github.com/thoff/findo/backend/internal/index"
)

// NewWebAuthn builds the relying party for a public URL: the passkey's id is
// the URL's host name and the only accepted origin is the URL itself.
func NewWebAuthn(publicURL *url.URL) (*webauthn.WebAuthn, error) {
	return webauthn.New(&webauthn.Config{
		RPID:          publicURL.Hostname(),
		RPDisplayName: "Findo",
		RPOrigins:     []string{publicURL.Scheme + "://" + publicURL.Host},
	})
}

// RegistrationOptions are the options every passkey registration uses: a
// discoverable credential (so sign-in needs no username), user verification
// required, and no attestation.
func RegistrationOptions(existing []index.Credential) []webauthn.RegistrationOption {
	exclude := make([]protocol.CredentialDescriptor, 0, len(existing))
	for _, c := range existing {
		exclude = append(exclude, protocol.CredentialDescriptor{Type: protocol.PublicKeyCredentialType, CredentialID: c.CredentialID})
	}
	return []webauthn.RegistrationOption{
		webauthn.WithResidentKeyRequirement(protocol.ResidentKeyRequirementRequired),
		webauthn.WithAuthenticatorSelection(protocol.AuthenticatorSelection{
			ResidentKey:      protocol.ResidentKeyRequirementRequired,
			UserVerification: protocol.VerificationRequired,
		}),
		webauthn.WithConveyancePreference(protocol.PreferNoAttestation),
		webauthn.WithExclusions(exclude),
	}
}

// LoginOptions are the options for sign-in.
func LoginOptions() []webauthn.LoginOption {
	return []webauthn.LoginOption{webauthn.WithUserVerification(protocol.VerificationRequired)}
}

// Handle is a user's WebAuthn user handle: their id, as 8 bytes. It's opaque
// to the authenticator and holds nothing personal.
func Handle(userID int64) []byte {
	b := make([]byte, 8)
	binary.BigEndian.PutUint64(b, uint64(userID))
	return b
}

// UserIDFromHandle reverses Handle; ok is false for anything else.
func UserIDFromHandle(h []byte) (int64, bool) {
	if len(h) != 8 {
		return 0, false
	}
	id := binary.BigEndian.Uint64(h)
	return int64(id), id < 1<<62
}

// WebAuthnUser adapts a Findo user and their passkeys to go-webauthn.
type WebAuthnUser struct {
	User        index.User
	Credentials []index.Credential
}

func (u WebAuthnUser) WebAuthnID() []byte          { return Handle(u.User.ID) }
func (u WebAuthnUser) WebAuthnName() string        { return u.User.Username }
func (u WebAuthnUser) WebAuthnDisplayName() string { return u.User.Username }

func (u WebAuthnUser) WebAuthnCredentials() []webauthn.Credential {
	out := make([]webauthn.Credential, 0, len(u.Credentials))
	for _, c := range u.Credentials {
		out = append(out, ToWebAuthn(c))
	}
	return out
}

// ToWebAuthn rebuilds go-webauthn's credential from a stored passkey. Every
// passkey was registered with user verification required, so it is recorded
// as verified; the backup flags must match what the authenticator reports.
func ToWebAuthn(c index.Credential) webauthn.Credential {
	flags := protocol.FlagUserPresent | protocol.FlagUserVerified
	if c.BackupEligible {
		flags |= protocol.FlagBackupEligible
	}
	if c.BackupState {
		flags |= protocol.FlagBackupState
	}
	transports := make([]protocol.AuthenticatorTransport, 0, len(c.Transports))
	for _, t := range c.Transports {
		transports = append(transports, protocol.AuthenticatorTransport(t))
	}
	return webauthn.Credential{
		ID:            c.CredentialID,
		PublicKey:     c.PublicKey,
		Transport:     transports,
		Flags:         webauthn.NewCredentialFlags(flags),
		Authenticator: webauthn.Authenticator{SignCount: c.SignCount},
	}
}

// FromWebAuthn converts a freshly registered credential for storage.
func FromWebAuthn(userID int64, cred *webauthn.Credential, label string) index.Credential {
	transports := make([]string, 0, len(cred.Transport))
	for _, t := range cred.Transport {
		transports = append(transports, string(t))
	}
	return index.Credential{
		UserID:         userID,
		CredentialID:   cred.ID,
		PublicKey:      cred.PublicKey,
		SignCount:      cred.Authenticator.SignCount,
		Transports:     transports,
		BackupEligible: cred.Flags.BackupEligible,
		BackupState:    cred.Flags.BackupState,
		Label:          label,
	}
}
