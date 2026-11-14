package store

import (
	"crypto/sha256"
	"fmt"
	"testing"
)

// schemaFingerprint pins schemaVersion to the schema.sql it was cut from.
// Changing schema.sql without bumping schemaVersion fails here: a database of
// the old shape would otherwise pass the version check.
const schemaFingerprint, fingerprintVersion = "141e809343d06c63", 13

func TestSchemaVersionMatchesSchemaFile(t *testing.T) {
	got := fmt.Sprintf("%x", sha256.Sum256([]byte(schemaSQL)))[:16]
	if got != schemaFingerprint || schemaVersion != fingerprintVersion {
		t.Fatalf("schema.sql (%s) does not match schemaVersion %d: bump schemaVersion and pin the new fingerprint", got, schemaVersion)
	}
}
