package httpapi

import (
	"fmt"

	"github.com/thoff/findo/backend/internal/crypto"
	"github.com/thoff/findo/backend/internal/index"
)

// A volume's SMB password is sealed with its id and owner as authenticated
// data (index.VolumeAAD), so a database writer can't move one user's ciphertext
// onto another volume or owner. An unowned volume, from before accounts, was
// sealed without any.

func sealPassword(masterKey []byte, volumeID int64, owner *int64, password string) ([]byte, error) {
	return crypto.Encrypt(masterKey, []byte(password), index.VolumeAAD(volumeID, owner))
}

func openPassword(masterKey []byte, volumeID int64, owner *int64, passwordEnc []byte) (string, error) {
	plaintext, err := crypto.Decrypt(masterKey, passwordEnc, index.VolumeAAD(volumeID, owner))
	if err != nil {
		return "", fmt.Errorf("decrypt: %w", err)
	}
	return string(plaintext), nil
}

// AdoptUnownedVolumes gives the volumes that predate accounts to ownerID (the
// first admin) and reseals each password for its new owner. It returns how many
// it adopted.
func AdoptUnownedVolumes(idx *index.Index, masterKey []byte, ownerID int64) (int, error) {
	return idx.AdoptUnownedVolumes(ownerID, func(volumeID int64, enc []byte) ([]byte, error) {
		pw, err := openPassword(masterKey, volumeID, nil, enc)
		if err != nil {
			return nil, err
		}
		return sealPassword(masterKey, volumeID, &ownerID, pw)
	})
}
