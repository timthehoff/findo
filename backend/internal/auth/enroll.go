package auth

import (
	"net/url"
	"time"

	"github.com/thoff/findo/backend/internal/index"
)

// EnrollmentTTL is how long an enrollment link works.
const EnrollmentTTL = 15 * time.Minute

// IssueEnrollment creates a single-use link that lets userID register a
// passkey, and returns it. The token is in the URL fragment so it never reaches
// an access log or a Referer header. Any earlier unused link for the user stops
// working. createdBy is nil when the CLI issues it.
func IssueEnrollment(idx *index.Index, publicURL *url.URL, userID int64, purpose string, createdBy *int64) (link string, expires time.Time, err error) {
	token, hash := NewToken()
	if err := idx.CreateEnrollment(userID, hash, purpose, createdBy, EnrollmentTTL); err != nil {
		return "", time.Time{}, err
	}
	u := *publicURL
	u.Path = "/enroll"
	u.Fragment = token
	return u.String(), time.Now().Add(EnrollmentTTL), nil
}
